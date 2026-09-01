package tui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/kristyancarvalho/argo/internal/console"
	"github.com/kristyancarvalho/argo/internal/ipc"
)

const (
	tuiETASmoothingFactor = 0.3
	tuiMinimumETASamples  = 2
	tuiETAInterval        = 3 * time.Second
)

type Client interface {
	Status(context.Context) (ipc.Status, error)
	List(context.Context) ([]ipc.Download, error)
	Add(context.Context, string, string) (ipc.AddResponse, error)
	Pause(context.Context, string) (ipc.DownloadActionResponse, error)
	Resume(context.Context, string) (ipc.DownloadActionResponse, error)
	Cancel(context.Context, string) (ipc.DownloadActionResponse, error)
	Remove(context.Context, string) (ipc.DownloadActionResponse, error)
	Clear(context.Context) (ipc.ClearResponse, error)
	Retry(context.Context, string) (ipc.AddResponse, error)
	Priority(context.Context, string, string) (ipc.PriorityResponse, error)
	Profile(context.Context, string) (ipc.ProfileResponse, error)
	Policy(context.Context, string) (ipc.PolicyResponse, error)
}

type snapshotMessage struct {
	status    ipc.Status
	downloads []ipc.Download
	at        time.Time
}

type errorMessage struct {
	err error
}

type refreshMessage struct{}

type actionResultMessage struct {
	message string
	err     error
}

type inputMode int

const (
	inputModeNone inputMode = iota
	inputModeAdd
	inputModeCancel
	inputModeRemove
	inputModeClear
	inputModeProfile
	inputModePolicy
)

type transferPoint struct {
	bytes      int64
	at         time.Time
	status     string
	smoothed   float64
	samples    int
	eta        time.Duration
	etaAt      time.Time
	etaVisible bool
}

type Model struct {
	ctx       context.Context
	client    Client
	status    ipc.Status
	downloads []ipc.Download
	selected  int
	speeds    map[string]int64
	etas      map[string]time.Duration
	previous  map[string]transferPoint
	err       error
	actionErr error
	notice    string
	input     string
	mode      inputMode
	ready     bool
	color     bool
	width     int
	height    int
	offset    int
	help      bool
}

type Options struct {
	Color bool
}

func NewModel(ctx context.Context, client Client) (Model, error) {
	return NewModelWithOptions(ctx, client, Options{})
}

func NewModelWithOptions(ctx context.Context, client Client, options Options) (Model, error) {
	if ctx == nil || client == nil {
		return Model{}, fmt.Errorf("TUI requires context and daemon client")
	}

	return Model{
		ctx:      ctx,
		client:   client,
		speeds:   make(map[string]int64),
		etas:     make(map[string]time.Duration),
		previous: make(map[string]transferPoint),
		color:    options.Color,
	}, nil
}

func (model Model) Init() tea.Cmd {
	return model.loadSnapshot
}

func (model Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.KeyMsg:
		if model.help {
			switch message.String() {
			case "q", "ctrl+c":
				return model, tea.Quit
			case "?", "esc":
				model.help = false
			}

			return model, nil
		}
		if model.mode == inputModeAdd || model.mode == inputModeProfile {
			return model.updateTextInput(message)
		}
		if model.mode == inputModeCancel || model.mode == inputModeRemove || model.mode == inputModeClear {
			return model.updateConfirmation(message)
		}
		if model.mode == inputModePolicy {
			return model.updatePolicySelection(message)
		}
		switch message.String() {
		case "q", "ctrl+c", "esc":
			return model, tea.Quit
		case "up", "k":
			if model.selected > 0 {
				model.selected--
				model.ensureVisible()
			}
		case "down", "j":
			if model.selected+1 < len(model.downloads) {
				model.selected++
				model.ensureVisible()
			}
		case "a":
			model.mode = inputModeAdd
			model.input = ""
			model.clearActionStatus()
		case "f":
			model.mode = inputModeProfile
			model.input = ""
			model.clearActionStatus()
		case "t":
			model.mode = inputModePolicy
			model.clearActionStatus()
		case "p":
			return model.dispatchSelected("pause")
		case "r":
			return model.dispatchSelected("resume")
		case "c":
			if !model.hasSelection() {
				model.actionErr = fmt.Errorf("no download selected")
				break
			}
			model.mode = inputModeCancel
			model.clearActionStatus()
		case "x":
			if !model.hasSelection() {
				model.actionErr = fmt.Errorf("no download selected")
				break
			}
			model.mode = inputModeRemove
			model.clearActionStatus()
		case "C":
			model.mode = inputModeClear
			model.clearActionStatus()
		case "R":
			return model.dispatchSelected("retry")
		case "?":
			model.help = true
		case "1":
			return model.dispatchPriority("low")
		case "2":
			return model.dispatchPriority("normal")
		case "3":
			return model.dispatchPriority("high")
		}
	case snapshotMessage:
		model.applySnapshot(message)
		return model, refreshAfter(time.Second)
	case errorMessage:
		model.err = message.err
		model.ready = true
		return model, refreshAfter(time.Second)
	case actionResultMessage:
		model.actionErr = message.err
		model.notice = message.message
		return model, model.loadSnapshot
	case refreshMessage:
		return model, model.loadSnapshot
	case tea.WindowSizeMsg:
		model.width = message.Width
		model.height = message.Height
		model.ensureVisible()
	}

	return model, nil
}

func (model Model) View() string {
	var view strings.Builder
	width := model.viewWidth()
	title := console.Paint(model.color, console.Cyan, console.Paint(model.color, console.Bold, "Argo"))
	daemon := "Daemon: " + statusValue(model.status.State)
	if !model.ready {
		daemon = "Daemon: connecting"
	}
	spaces := max(1, width-4-utf8.RuneCountInString(daemon))
	view.WriteString(title)
	view.WriteString(strings.Repeat(" ", spaces))
	view.WriteString(console.Paint(model.color, statusColor(model.status.State), daemon))
	view.WriteByte('\n')
	view.WriteString(strings.Repeat("-", width))
	view.WriteByte('\n')
	if model.help {
		model.renderHelp(&view)
		return view.String()
	}
	switch {
	case !model.ready:
		view.WriteString(console.Paint(model.color, console.Yellow, "Connecting to daemon..."))
		view.WriteByte('\n')
	case model.err != nil:
		view.WriteString(console.Paint(model.color, console.Red, "Daemon unavailable: "+model.err.Error()))
		view.WriteByte('\n')
	default:
		model.renderNetworkAndQoS(&view)
		view.WriteByte('\n')
		view.WriteString(console.Paint(model.color, console.Bold, "Downloads"))
		view.WriteByte('\n')
		model.renderDownloads(&view, width)
		model.renderSelected(&view, width)
	}
	if model.mode == inputModeAdd || model.mode == inputModeProfile {
		label := "Add URL: "
		if model.mode == inputModeProfile {
			label = "Profile name: "
		}
		view.WriteString("\n")
		view.WriteString(console.Paint(model.color, console.Cyan, label))
		view.WriteString(model.input)
		view.WriteString("\nEnter submit  Esc cancel\n")
		return view.String()
	}
	if model.mode == inputModeCancel || model.mode == inputModeRemove || model.mode == inputModeClear {
		prompt := "Clear completed, failed, and canceled history? y/N"
		if model.mode == inputModeCancel && model.hasSelection() {
			prompt = fmt.Sprintf("Cancel %s? y/N", model.downloads[model.selected].ID)
		}
		if model.mode == inputModeRemove && model.hasSelection() {
			prompt = fmt.Sprintf("Remove %s from history? y/N", model.downloads[model.selected].ID)
		}
		view.WriteString(console.Paint(model.color, console.Yellow, "\n"+prompt+"\n"))
		return view.String()
	}
	if model.mode == inputModePolicy {
		view.WriteString(console.Paint(model.color, console.Cyan, "\nSelect traffic policy: 1 off  2 balanced  3 throughput  4 latency  5 focus  Esc cancel\n"))
		return view.String()
	}
	if model.actionErr != nil {
		view.WriteString(console.Paint(model.color, console.Red, "\nAction failed: "+model.actionErr.Error()))
		view.WriteByte('\n')
	} else if model.notice != "" {
		view.WriteString("\n")
		view.WriteString(console.Paint(model.color, console.Green, model.notice))
		view.WriteByte('\n')
	}
	if width < 72 {
		view.WriteString(console.Paint(model.color, console.Dim, "\nup/down select  a add\n"))
		view.WriteString(console.Paint(model.color, console.Dim, "p pause  r resume  R retry\n"))
		view.WriteString(console.Paint(model.color, console.Dim, "c cancel  x remove  C clear\n"))
		view.WriteString(console.Paint(model.color, console.Dim, "? help  q quit\n"))
	} else {
		view.WriteString(console.Paint(model.color, console.Dim, "\nup/down select  a add  p pause  r resume  R retry  c cancel  x remove  C clear  ? help  q quit\n"))
	}

	return view.String()
}

func (model Model) renderDownloads(view *strings.Builder, width int) {
	if len(model.downloads) == 0 {
		view.WriteString("  No downloads.\n")
		return
	}
	start, end := model.visibleBounds()
	if start > 0 {
		_, _ = fmt.Fprintf(view, "  ... %d above ...\n", start)
	}
	for index := start; index < end; index++ {
		download := model.downloads[index]
		marker := " "
		if index == model.selected {
			marker = ">"
		}
		speed := model.speeds[download.ID]
		var row string
		if width < 72 {
			filename := truncateRunes(download.Filename, max(12, width-4))
			row = fmt.Sprintf("%s %s\n  %s  %s  %s\n  %s  ETA %s  %s\n",
				marker, filename, shortID(download.ID), download.Status, formatTUIProgress(download),
				formatTUISpeed(speed), model.formatTUIETA(download), download.Priority)
		} else {
			filenameWidth := max(14, width-65)
			row = fmt.Sprintf("%s %-*s %12s %10s %8s %-11s %-6s %s\n",
				marker, filenameWidth, truncateRunes(download.Filename, filenameWidth),
				formatTUIProgress(download), formatTUISpeed(speed), model.formatTUIETA(download),
				download.Status, download.Priority, shortID(download.ID))
		}
		color := statusColor(download.Status)
		if index == model.selected {
			color = console.Cyan
		}
		view.WriteString(console.Paint(model.color, color, row))
	}
	if end < len(model.downloads) {
		_, _ = fmt.Fprintf(view, "  ... %d below ...\n", len(model.downloads)-end)
	}
}

func (model Model) renderSelected(view *strings.Builder, width int) {
	if !model.hasSelection() {
		return
	}
	download := model.downloads[model.selected]
	view.WriteByte('\n')
	view.WriteString(console.Paint(model.color, console.Bold, "Selected"))
	view.WriteByte('\n')
	if model.height > 0 && model.height < 22 {
		_, _ = fmt.Fprintf(view, "  %s  %s  %s\n", shortID(download.ID), download.Status, download.Priority)
		_, _ = fmt.Fprintf(view, "  %s\n", truncateRunes(download.Filename, max(8, width-4)))
		return
	}
	_, _ = fmt.Fprintf(view, "  ID: %s\n  State: %s    Priority: %s\n", download.ID, download.Status, download.Priority)
	_, _ = fmt.Fprintf(view, "  File: %s\n", truncateRunes(download.Filename, max(8, width-8)))
	_, _ = fmt.Fprintf(view, "  Source: %s\n", truncateRunes(download.URL, max(8, width-10)))
}

func (model Model) renderHelp(view *strings.Builder) {
	view.WriteString(console.Paint(model.color, console.Bold, "Help"))
	view.WriteString("\n\n")
	view.WriteString("  up/down or k/j   move selection\n")
	view.WriteString("  a                 add URL\n")
	view.WriteString("  p / r             pause / resume partial transfer\n")
	view.WriteString("  R                 retry as a new transfer\n")
	view.WriteString("  c / x / C         cancel / remove / clear history\n")
	view.WriteString("  1 / 2 / 3         low / normal / high priority\n")
	view.WriteString("  t / f             traffic policy / profile\n")
	view.WriteString("  ? or Esc          close help\n")
	view.WriteString("  q                 quit\n")
}

func (model Model) renderNetworkAndQoS(view *strings.Builder) {
	networkState := model.status.Network.State
	if !model.status.Network.Available {
		networkState = "unavailable"
	} else if !model.status.Network.Connected {
		networkState = "disconnected"
	} else if networkState == "" {
		networkState = "connected"
	}
	if model.height > 0 && model.height < 22 {
		_, _ = fmt.Fprintf(view, "Network: %s  Interface: %s\n", networkState, statusValue(model.status.Network.Interface))
		_, _ = fmt.Fprintf(view, "Profile: %s  Traffic policy: %s\n", statusValue(model.status.ActiveProfile), statusValue(model.status.Traffic.Policy))
		if model.status.Traffic.Error != "" {
			view.WriteString(console.Paint(model.color, console.Red, "QoS error: "+model.status.Traffic.Error))
			view.WriteByte('\n')
		}
		return
	}
	if model.viewWidth() < 72 {
		_, _ = fmt.Fprintf(
			view,
			"Network: %s\nInterface: %s    Metered: %s\nProfile: %s\nTraffic policy: %s\nArgo throughput: %s\n",
			networkState,
			statusValue(model.status.Network.Interface),
			statusValue(model.status.Network.Metered),
			statusValue(model.status.ActiveProfile),
			statusValue(model.status.Traffic.Policy),
			formatTUISpeed(model.totalSpeed()),
		)
	} else {
		_, _ = fmt.Fprintf(
			view,
			"Network: %s  Interface: %s  Metered: %s\nProfile: %s  Traffic policy: %s  Argo throughput: %s\n",
			networkState,
			statusValue(model.status.Network.Interface),
			statusValue(model.status.Network.Metered),
			statusValue(model.status.ActiveProfile),
			statusValue(model.status.Traffic.Policy),
			formatTUISpeed(model.totalSpeed()),
		)
	}
	if model.status.Traffic.CurrentRateBitsPerSecond > 0 {
		_, _ = fmt.Fprintf(view, "Adaptive limit: %d bit/s\n", model.status.Traffic.CurrentRateBitsPerSecond)
	} else {
		view.WriteString("Adaptive limit: unavailable\n")
	}
	if model.status.Traffic.Error != "" {
		view.WriteString(console.Paint(model.color, console.Red, "QoS error: "+model.status.Traffic.Error))
		view.WriteByte('\n')
	}
	if model.status.Traffic.LatencyAvailable {
		_, _ = fmt.Fprintf(view, "Latency: %s", model.status.Traffic.MeasuredLatency)
		if model.status.Traffic.BaselineAvailable {
			_, _ = fmt.Fprintf(view, " (baseline %s)", model.status.Traffic.BaselineLatency)
		}
		if model.status.Traffic.ControllerState != "" {
			_, _ = fmt.Fprintf(view, "  Controller: %s", model.status.Traffic.ControllerState)
		}
		view.WriteByte('\n')
	} else {
		view.WriteString("Latency: unavailable\n")
	}
}

func (model Model) totalSpeed() int64 {
	var total int64
	for _, speed := range model.speeds {
		total += speed
	}

	return total
}

func statusValue(value string) string {
	if value == "" {
		return "unknown"
	}

	return value
}

func (model Model) updateTextInput(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "esc":
		model.mode = inputModeNone
	case "backspace", "delete":
		runes := []rune(model.input)
		if len(runes) > 0 {
			model.input = string(runes[:len(runes)-1])
		}
	case "enter":
		value := strings.TrimSpace(model.input)
		if value == "" {
			model.actionErr = fmt.Errorf("value is required")
			model.mode = inputModeNone
			return model, nil
		}
		mode := model.mode
		model.mode = inputModeNone
		return model, func() tea.Msg {
			if mode == inputModeAdd {
				response, err := model.client.Add(model.ctx, value, "")
				return actionResultMessage{message: "Added " + response.ID, err: err}
			}
			response, err := model.client.Profile(model.ctx, value)
			return actionResultMessage{message: "Active profile: " + response.Name, err: err}
		}
	default:
		if message.Type == tea.KeyRunes {
			model.input += string(message.Runes)
		}
	}

	return model, nil
}

func (model Model) updatePolicySelection(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	if message.Type == tea.KeyEsc {
		model.mode = inputModeNone
		return model, nil
	}
	policies := map[string]string{
		"1": "off", "2": "balanced", "3": "throughput", "4": "latency", "5": "focus",
	}
	policy, exists := policies[message.String()]
	if !exists {
		return model, nil
	}
	model.mode = inputModeNone
	return model, func() tea.Msg {
		response, err := model.client.Policy(model.ctx, policy)
		return actionResultMessage{message: "Traffic policy: " + response.Policy, err: err}
	}
}

func (model Model) updateConfirmation(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "y", "Y":
		mode := model.mode
		model.mode = inputModeNone
		switch mode {
		case inputModeCancel:
			return model.dispatchSelected("cancel")
		case inputModeRemove:
			return model.dispatchSelected("remove")
		case inputModeClear:
			return model.dispatchClear()
		case inputModeNone, inputModeAdd, inputModeProfile, inputModePolicy:
			return model, nil
		}
	case "n", "N", "esc":
		model.mode = inputModeNone
	}

	return model, nil
}

func (model Model) dispatchSelected(action string) (tea.Model, tea.Cmd) {
	if !model.hasSelection() {
		model.actionErr = fmt.Errorf("no download selected")
		return model, nil
	}
	model.clearActionStatus()
	identifier := model.downloads[model.selected].ID
	return model, func() tea.Msg {
		var err error
		switch action {
		case "pause":
			_, err = model.client.Pause(model.ctx, identifier)
		case "resume":
			_, err = model.client.Resume(model.ctx, identifier)
		case "cancel":
			_, err = model.client.Cancel(model.ctx, identifier)
		case "remove":
			_, err = model.client.Remove(model.ctx, identifier)
		case "retry":
			var response ipc.AddResponse
			response, err = model.client.Retry(model.ctx, identifier)
			if err == nil {
				return actionResultMessage{message: fmt.Sprintf("%s: retry created %s", identifier, response.ID)}
			}
		}
		return actionResultMessage{message: fmt.Sprintf("%s: %s", identifier, action), err: err}
	}
}

func (model Model) dispatchClear() (tea.Model, tea.Cmd) {
	model.clearActionStatus()
	return model, func() tea.Msg {
		response, err := model.client.Clear(model.ctx)
		return actionResultMessage{message: fmt.Sprintf("Cleared %d historical downloads", response.Removed), err: err}
	}
}

func (model Model) dispatchPriority(priority string) (tea.Model, tea.Cmd) {
	if !model.hasSelection() {
		model.actionErr = fmt.Errorf("no download selected")
		return model, nil
	}
	model.clearActionStatus()
	identifier := model.downloads[model.selected].ID
	return model, func() tea.Msg {
		_, err := model.client.Priority(model.ctx, identifier, priority)
		return actionResultMessage{message: fmt.Sprintf("%s: priority %s", identifier, priority), err: err}
	}
}

func (model Model) hasSelection() bool {
	return model.selected >= 0 && model.selected < len(model.downloads)
}

func (model *Model) clearActionStatus() {
	model.actionErr = nil
	model.notice = ""
}

func (model Model) loadSnapshot() tea.Msg {
	status, err := model.client.Status(model.ctx)
	if err != nil {
		return errorMessage{err: err}
	}
	downloads, err := model.client.List(model.ctx)
	if err != nil {
		return errorMessage{err: err}
	}

	return snapshotMessage{status: status, downloads: downloads, at: time.Now().UTC()}
}

func (model *Model) applySnapshot(message snapshotMessage) {
	model.status = message.status
	model.downloads = append([]ipc.Download(nil), message.downloads...)
	model.err = nil
	model.ready = true
	if model.selected >= len(model.downloads) && model.selected > 0 {
		model.selected = len(model.downloads) - 1
	}
	model.ensureVisible()
	current := make(map[string]transferPoint, len(model.downloads))
	currentSpeeds := make(map[string]int64, len(model.downloads))
	currentETAs := make(map[string]time.Duration, len(model.downloads))
	for _, download := range model.downloads {
		point := transferPoint{bytes: download.DownloadedBytes, at: message.at, status: download.Status}
		previous, exists := model.previous[download.ID]
		reset := !exists || !point.at.After(previous.at) || point.bytes < previous.bytes ||
			(previous.status != "downloading" && point.status == "downloading")
		if !reset {
			speed := int64(float64(point.bytes-previous.bytes) / point.at.Sub(previous.at).Seconds())
			if speed > 0 {
				currentSpeeds[download.ID] = speed
			}
			point.smoothed = previous.smoothed
			point.samples = previous.samples
			point.eta = previous.eta
			point.etaAt = previous.etaAt
			point.etaVisible = previous.etaVisible
			if download.Status == "completed" {
				point.eta = 0
				point.etaAt = message.at
				point.etaVisible = true
			} else if download.Status == "downloading" && speed > 0 && download.TotalSize >= download.DownloadedBytes {
				if point.samples == 0 {
					point.smoothed = float64(speed)
				} else {
					point.smoothed = tuiETASmoothingFactor*float64(speed) + (1-tuiETASmoothingFactor)*point.smoothed
				}
				point.samples++
				if point.samples >= tuiMinimumETASamples && (!point.etaVisible || message.at.Sub(point.etaAt) >= tuiETAInterval) {
					seconds := float64(download.TotalSize-download.DownloadedBytes) / point.smoothed
					point.eta = time.Duration(seconds * float64(time.Second))
					point.etaAt = message.at
					point.etaVisible = true
				}
			} else {
				point.smoothed = 0
				point.samples = 0
				point.etaVisible = false
			}
		}
		if download.Status == "completed" {
			point.eta = 0
			point.etaAt = message.at
			point.etaVisible = true
		}
		if point.etaVisible {
			currentETAs[download.ID] = point.eta
		}
		current[download.ID] = point
	}
	model.speeds = currentSpeeds
	model.etas = currentETAs
	model.previous = current
}

func refreshAfter(delay time.Duration) tea.Cmd {
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return refreshMessage{}
	})
}

func (model Model) viewWidth() int {
	if model.width <= 0 {
		return 100
	}

	return max(32, model.width)
}

func (model Model) listCapacity() int {
	height := model.height
	if height <= 0 {
		height = 30
	}
	capacity := max(1, height-17)
	if model.viewWidth() < 72 {
		capacity = max(1, capacity/3)
	}

	return min(8, capacity)
}

func (model Model) visibleBounds() (int, int) {
	start := min(model.offset, max(0, len(model.downloads)-1))
	end := min(len(model.downloads), start+model.listCapacity())

	return start, end
}

func (model *Model) ensureVisible() {
	if len(model.downloads) == 0 {
		model.selected = 0
		model.offset = 0
		return
	}
	model.selected = min(max(0, model.selected), len(model.downloads)-1)
	capacity := model.listCapacity()
	if model.selected < model.offset {
		model.offset = model.selected
	}
	if model.selected >= model.offset+capacity {
		model.offset = model.selected - capacity + 1
	}
	model.offset = min(model.offset, max(0, len(model.downloads)-capacity))
}

func statusColor(status string) console.Code {
	switch status {
	case "running", "completed":
		return console.Green
	case "queued", "resolving":
		return console.Cyan
	case "downloading":
		return console.Blue
	case "paused":
		return console.Yellow
	case "failed", "canceled":
		return console.Red
	default:
		return ""
	}
}

func shortID(value string) string {
	return truncateRunes(value, 8)
}

func truncateRunes(value string, maximum int) string {
	if maximum <= 0 {
		return ""
	}
	if utf8.RuneCountInString(value) <= maximum {
		return value
	}
	runes := []rune(value)
	if maximum <= 3 {
		return string(runes[:maximum])
	}

	return string(runes[:maximum-1]) + "…"
}

func formatTUIProgress(download ipc.Download) string {
	if download.TotalSize < 0 {
		return fmt.Sprintf("%s/?", formatTUIBytes(download.DownloadedBytes))
	}

	return fmt.Sprintf("%s/%s", formatTUIBytes(download.DownloadedBytes), formatTUIBytes(download.TotalSize))
}

func formatTUISpeed(speed int64) string {
	if speed <= 0 {
		return "--"
	}

	return formatTUIBytes(speed) + "/s"
}

func (model Model) formatTUIETA(download ipc.Download) string {
	eta, exists := model.etas[download.ID]
	if !exists {
		return "--"
	}

	return eta.Round(time.Second).String()
}

func formatTUIBytes(value int64) string {
	if value < 1_000 {
		return fmt.Sprintf("%dB", value)
	}
	if value < 1_000_000 {
		return fmt.Sprintf("%.1fKB", float64(value)/1_000)
	}
	if value < 1_000_000_000 {
		return fmt.Sprintf("%.1fMB", float64(value)/1_000_000)
	}

	return fmt.Sprintf("%.1fGB", float64(value)/1_000_000_000)
}

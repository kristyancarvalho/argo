package tui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/kristyancarvalho/argo/internal/ipc"
)

const maximumFilenameRunes = 32

type Client interface {
	Status(context.Context) (ipc.Status, error)
	List(context.Context) ([]ipc.Download, error)
	Add(context.Context, string, string) (ipc.AddResponse, error)
	Pause(context.Context, string) (ipc.DownloadActionResponse, error)
	Resume(context.Context, string) (ipc.DownloadActionResponse, error)
	Cancel(context.Context, string) (ipc.DownloadActionResponse, error)
	Priority(context.Context, string, string) (ipc.PriorityResponse, error)
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
)

type transferPoint struct {
	bytes int64
	at    time.Time
}

type Model struct {
	ctx       context.Context
	client    Client
	status    ipc.Status
	downloads []ipc.Download
	selected  int
	speeds    map[string]int64
	previous  map[string]transferPoint
	err       error
	actionErr error
	notice    string
	input     string
	mode      inputMode
	ready     bool
}

func NewModel(ctx context.Context, client Client) (Model, error) {
	if ctx == nil || client == nil {
		return Model{}, fmt.Errorf("TUI requires context and daemon client")
	}

	return Model{
		ctx:      ctx,
		client:   client,
		speeds:   make(map[string]int64),
		previous: make(map[string]transferPoint),
	}, nil
}

func (model Model) Init() tea.Cmd {
	return model.loadSnapshot
}

func (model Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.KeyMsg:
		if model.mode == inputModeAdd {
			return model.updateAddInput(message)
		}
		if model.mode == inputModeCancel {
			return model.updateCancelConfirmation(message)
		}
		switch message.String() {
		case "q", "ctrl+c", "esc":
			return model, tea.Quit
		case "up", "k":
			if model.selected > 0 {
				model.selected--
			}
		case "down", "j":
			if model.selected+1 < len(model.downloads) {
				model.selected++
			}
		case "a":
			model.mode = inputModeAdd
			model.input = ""
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
	}

	return model, nil
}

func (model Model) View() string {
	var view strings.Builder
	view.WriteString("Argo\n\n")
	switch {
	case !model.ready:
		view.WriteString("Connecting to daemon…\n")
	case model.err != nil:
		view.WriteString("Daemon unavailable: ")
		view.WriteString(model.err.Error())
		view.WriteByte('\n')
	default:
		view.WriteString("Daemon: ")
		view.WriteString(model.status.State)
		view.WriteString("\n\n")
		view.WriteString("  ID                               FILENAME                         PROGRESS       SPEED       ETA      STATUS       PRIORITY\n")
		if len(model.downloads) == 0 {
			view.WriteString("  No downloads.\n")
		}
		for index, download := range model.downloads {
			marker := " "
			if index == model.selected {
				marker = ">"
			}
			speed := model.speeds[download.ID]
			_, _ = fmt.Fprintf(
				&view,
				"%s %-32s %-32s %-14s %-11s %-8s %-12s %s\n",
				marker,
				download.ID,
				truncateFilename(download.Filename),
				formatTUIProgress(download),
				formatTUISpeed(speed),
				formatTUIETA(download, speed),
				download.Status,
				download.Priority,
			)
		}
	}
	if model.mode == inputModeAdd {
		view.WriteString("\nAdd URL: ")
		view.WriteString(model.input)
		view.WriteString("\nEnter submit  Esc cancel\n")
		return view.String()
	}
	if model.mode == inputModeCancel {
		_, _ = fmt.Fprintf(&view, "\nCancel %s? y/N\n", model.downloads[model.selected].ID)
		return view.String()
	}
	if model.actionErr != nil {
		view.WriteString("\nAction failed: ")
		view.WriteString(model.actionErr.Error())
		view.WriteByte('\n')
	} else if model.notice != "" {
		view.WriteString("\n")
		view.WriteString(model.notice)
		view.WriteByte('\n')
	}
	view.WriteString("\n↑/k ↓/j select  a add  p pause  r resume  c cancel  1/2/3 priority  q quit\n")

	return view.String()
}

func (model Model) updateAddInput(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "esc":
		model.mode = inputModeNone
	case "backspace", "delete":
		runes := []rune(model.input)
		if len(runes) > 0 {
			model.input = string(runes[:len(runes)-1])
		}
	case "enter":
		url := strings.TrimSpace(model.input)
		if url == "" {
			model.actionErr = fmt.Errorf("URL is required")
			model.mode = inputModeNone
			return model, nil
		}
		model.mode = inputModeNone
		return model, func() tea.Msg {
			response, err := model.client.Add(model.ctx, url, "")
			return actionResultMessage{message: "Added " + response.ID, err: err}
		}
	default:
		if message.Type == tea.KeyRunes {
			model.input += string(message.Runes)
		}
	}

	return model, nil
}

func (model Model) updateCancelConfirmation(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "y", "Y":
		model.mode = inputModeNone
		return model.dispatchSelected("cancel")
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
		}
		return actionResultMessage{message: fmt.Sprintf("%s: %s", identifier, action), err: err}
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
	current := make(map[string]transferPoint, len(model.downloads))
	for _, download := range model.downloads {
		point := transferPoint{bytes: download.DownloadedBytes, at: message.at}
		current[download.ID] = point
		previous, exists := model.previous[download.ID]
		if !exists || !point.at.After(previous.at) || point.bytes < previous.bytes {
			delete(model.speeds, download.ID)
			continue
		}
		model.speeds[download.ID] = int64(float64(point.bytes-previous.bytes) / point.at.Sub(previous.at).Seconds())
	}
	for identifier := range model.speeds {
		if _, exists := current[identifier]; !exists {
			delete(model.speeds, identifier)
		}
	}
	model.previous = current
}

func refreshAfter(delay time.Duration) tea.Cmd {
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return refreshMessage{}
	})
}

func truncateFilename(value string) string {
	if utf8.RuneCountInString(value) <= maximumFilenameRunes {
		return value
	}
	runes := []rune(value)

	return string(runes[:maximumFilenameRunes-1]) + "…"
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

func formatTUIETA(download ipc.Download, speed int64) string {
	if download.TotalSize < 0 || speed <= 0 || download.DownloadedBytes >= download.TotalSize {
		return "--"
	}

	remaining := download.TotalSize - download.DownloadedBytes
	seconds := remaining / speed
	if remaining%speed != 0 {
		seconds++
	}

	return (time.Duration(seconds) * time.Second).String()
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

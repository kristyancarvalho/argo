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
		}
	case snapshotMessage:
		model.applySnapshot(message)
		return model, refreshAfter(time.Second)
	case errorMessage:
		model.err = message.err
		model.ready = true
		return model, refreshAfter(time.Second)
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
	view.WriteString("\n↑/k ↓/j select  q quit\n")

	return view.String()
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

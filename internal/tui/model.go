package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/kristyancarvalho/argo/internal/ipc"
)

type Client interface {
	Status(context.Context) (ipc.Status, error)
}

type statusMessage struct {
	status ipc.Status
}

type errorMessage struct {
	err error
}

type Model struct {
	ctx    context.Context
	client Client
	status ipc.Status
	err    error
	ready  bool
}

func NewModel(ctx context.Context, client Client) (Model, error) {
	if ctx == nil || client == nil {
		return Model{}, fmt.Errorf("TUI requires context and daemon client")
	}

	return Model{ctx: ctx, client: client}, nil
}

func (model Model) Init() tea.Cmd {
	return model.loadStatus
}

func (model Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.KeyMsg:
		switch message.String() {
		case "q", "ctrl+c", "esc":
			return model, tea.Quit
		}
	case statusMessage:
		model.status = message.status
		model.err = nil
		model.ready = true
	case errorMessage:
		model.err = message.err
		model.ready = true
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
		view.WriteByte('\n')
	}
	view.WriteString("\nq quit\n")

	return view.String()
}

func (model Model) loadStatus() tea.Msg {
	status, err := model.client.Status(model.ctx)
	if err != nil {
		return errorMessage{err: err}
	}

	return statusMessage{status: status}
}

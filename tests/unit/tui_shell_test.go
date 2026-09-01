package unit_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/kristyancarvalho/argo/internal/ipc"
	"github.com/kristyancarvalho/argo/internal/tui"
)

type tuiStatusClient struct {
	status ipc.Status
	err    error
}

func (client tuiStatusClient) Status(context.Context) (ipc.Status, error) {
	return client.status, client.err
}

func TestTUIModelInitializationLoadsDaemonStatus(t *testing.T) {
	model, err := tui.NewModel(context.Background(), tuiStatusClient{
		status: ipc.Status{State: "running"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(model.View(), "Connecting to daemon") {
		t.Fatalf("unexpected initial view %q", model.View())
	}
	message := model.Init()()
	updated, _ := model.Update(message)
	view := updated.(tui.Model).View()
	if !strings.Contains(view, "Daemon: running") {
		t.Fatalf("unexpected loaded view %q", view)
	}
}

func TestTUIModelExitKeysQuit(t *testing.T) {
	model, err := tui.NewModel(context.Background(), tuiStatusClient{})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'q'}},
		{Type: tea.KeyCtrlC},
		{Type: tea.KeyEsc},
	} {
		_, command := model.Update(key)
		if command == nil {
			t.Fatalf("key %q did not produce a quit command", key.String())
		}
		if _, valid := command().(tea.QuitMsg); !valid {
			t.Fatalf("key %q produced a non-quit message", key.String())
		}
	}
}

func TestTUIModelShowsIPCUnavailableState(t *testing.T) {
	model, err := tui.NewModel(context.Background(), tuiStatusClient{err: errors.New("connection refused")})
	if err != nil {
		t.Fatal(err)
	}
	updated, _ := model.Update(model.Init()())
	view := updated.(tui.Model).View()
	if !strings.Contains(view, "Daemon unavailable: connection refused") {
		t.Fatalf("unexpected unavailable view %q", view)
	}
}

func TestTUIModelRejectsMissingDependencies(t *testing.T) {
	if _, err := tui.NewModel(context.Background(), nil); err == nil {
		t.Fatal("nil client was accepted")
	}
}

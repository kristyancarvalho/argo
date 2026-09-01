package unit_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/kristyancarvalho/argo/internal/ipc"
	"github.com/kristyancarvalho/argo/internal/tui"
)

type tuiStatusClient struct {
	status    ipc.Status
	downloads [][]ipc.Download
	listCall  int
	err       error
	actionErr error
	actions   []string
}

func (client *tuiStatusClient) Status(context.Context) (ipc.Status, error) {
	return client.status, client.err
}

func (client *tuiStatusClient) List(context.Context) ([]ipc.Download, error) {
	if client.err != nil || len(client.downloads) == 0 {
		return nil, client.err
	}
	index := client.listCall
	if index >= len(client.downloads) {
		index = len(client.downloads) - 1
	}
	client.listCall++

	return client.downloads[index], nil
}

func (client *tuiStatusClient) Add(_ context.Context, rawURL, _ string) (ipc.AddResponse, error) {
	client.actions = append(client.actions, "add:"+rawURL)
	return ipc.AddResponse{ID: "added"}, client.actionErr
}

func (client *tuiStatusClient) Pause(_ context.Context, id string) (ipc.DownloadActionResponse, error) {
	client.actions = append(client.actions, "pause:"+id)
	return ipc.DownloadActionResponse{ID: id}, client.actionErr
}

func (client *tuiStatusClient) Resume(_ context.Context, id string) (ipc.DownloadActionResponse, error) {
	client.actions = append(client.actions, "resume:"+id)
	return ipc.DownloadActionResponse{ID: id}, client.actionErr
}

func (client *tuiStatusClient) Cancel(_ context.Context, id string) (ipc.DownloadActionResponse, error) {
	client.actions = append(client.actions, "cancel:"+id)
	return ipc.DownloadActionResponse{ID: id}, client.actionErr
}

func (client *tuiStatusClient) Priority(_ context.Context, id, priority string) (ipc.PriorityResponse, error) {
	client.actions = append(client.actions, "priority:"+id+":"+priority)
	return ipc.PriorityResponse{ID: id, Priority: priority}, client.actionErr
}

func TestTUIModelInitializationLoadsDaemonStatus(t *testing.T) {
	model, err := tui.NewModel(context.Background(), &tuiStatusClient{
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
	model, err := tui.NewModel(context.Background(), &tuiStatusClient{})
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
	model, err := tui.NewModel(context.Background(), &tuiStatusClient{err: errors.New("connection refused")})
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

func TestTUIDownloadListEmpty(t *testing.T) {
	model := loadTUIModel(t, &tuiStatusClient{status: ipc.Status{State: "running"}})
	if !strings.Contains(model.View(), "No downloads.") {
		t.Fatalf("unexpected empty list view %q", model.View())
	}
}

func TestTUIDownloadListRendersAndSelectsMultipleDownloads(t *testing.T) {
	client := &tuiStatusClient{
		status: ipc.Status{State: "running"},
		downloads: [][]ipc.Download{{
			{ID: "first", Filename: "first.bin", DownloadedBytes: 500, TotalSize: 1_000, Status: "downloading", Priority: "high"},
			{ID: "second", Filename: "second.bin", DownloadedBytes: 1_000, TotalSize: 2_000, Status: "paused", Priority: "low"},
		}},
	}
	model := loadTUIModel(t, client)
	view := model.View()
	for _, value := range []string{"first", "first.bin", "500B/1.0KB", "downloading", "high", "second.bin", "paused", "low"} {
		if !strings.Contains(view, value) {
			t.Fatalf("list view %q does not contain %q", view, value)
		}
	}
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	if !strings.Contains(updated.(tui.Model).View(), "> second") {
		t.Fatalf("second download was not selected: %q", updated.(tui.Model).View())
	}
}

func TestTUIDownloadListHandlesUnknownSizeAndLongFilename(t *testing.T) {
	longName := strings.Repeat("α", 40) + ".bin"
	model := loadTUIModel(t, &tuiStatusClient{
		status: ipc.Status{State: "running"},
		downloads: [][]ipc.Download{{{
			ID: "unknown", Filename: longName, DownloadedBytes: 1_500, TotalSize: -1,
			Status: "downloading", Priority: "normal",
		}}},
	})
	view := model.View()
	if !strings.Contains(view, "1.5KB/?") || !strings.Contains(view, "…") || strings.Contains(view, longName) {
		t.Fatalf("unexpected unknown-size long-name view %q", view)
	}
}

func TestTUIDownloadListCalculatesSpeedAndETA(t *testing.T) {
	client := &tuiStatusClient{
		status: ipc.Status{State: "running"},
		downloads: [][]ipc.Download{
			{{ID: "speed", Filename: "speed.bin", DownloadedBytes: 0, TotalSize: 10_000, Status: "downloading", Priority: "normal"}},
			{{ID: "speed", Filename: "speed.bin", DownloadedBytes: 1_000, TotalSize: 10_000, Status: "downloading", Priority: "normal"}},
		},
	}
	model := loadTUIModel(t, client)
	time.Sleep(10 * time.Millisecond)
	updated, _ := model.Update(model.Init()())
	view := updated.(tui.Model).View()
	if !strings.Contains(view, "/s") || strings.Contains(view, "speed.bin      1.0KB/10.0KB    --") {
		t.Fatalf("speed and ETA were not rendered: %q", view)
	}
}

func TestTUIActionsDispatch(t *testing.T) {
	client := &tuiStatusClient{status: ipc.Status{State: "running"}, downloads: [][]ipc.Download{{{ID: "one"}}}}
	model := loadTUIModel(t, client)
	for _, key := range []string{"p", "r", "1", "2", "3"} {
		updated, command := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		model = updated.(tui.Model)
		if command == nil {
			t.Fatalf("key %q did not dispatch", key)
		}
		updated, _ = model.Update(command())
		model = updated.(tui.Model)
	}
	model, _ = enterTUIAdd(model, "https://example.com/file")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	model = updated.(tui.Model)
	if !strings.Contains(model.View(), "Cancel one? y/N") {
		t.Fatalf("cancel confirmation not shown: %q", model.View())
	}
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if command == nil {
		t.Fatal("confirmed cancel did not dispatch")
	}
	_, _ = updated.(tui.Model).Update(command())
	want := []string{"pause:one", "resume:one", "priority:one:low", "priority:one:normal", "priority:one:high", "add:https://example.com/file", "cancel:one"}
	if strings.Join(client.actions, ",") != strings.Join(want, ",") {
		t.Fatalf("actions = %v, want %v", client.actions, want)
	}
}

func TestTUIActionRejectsInvalidSelection(t *testing.T) {
	client := &tuiStatusClient{status: ipc.Status{State: "running"}}
	model := loadTUIModel(t, client)
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if command != nil || len(client.actions) != 0 || !strings.Contains(updated.(tui.Model).View(), "no download selected") {
		t.Fatalf("invalid selection was not rejected: %q", updated.(tui.Model).View())
	}
}

func TestTUIActionShowsDaemonError(t *testing.T) {
	client := &tuiStatusClient{
		status: ipc.Status{State: "running"}, downloads: [][]ipc.Download{{{ID: "one"}}}, actionErr: errors.New("daemon rejected action"),
	}
	model := loadTUIModel(t, client)
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	updated, _ = updated.(tui.Model).Update(command())
	if !strings.Contains(updated.(tui.Model).View(), "Action failed: daemon rejected action") {
		t.Fatalf("action error not shown: %q", updated.(tui.Model).View())
	}
}

func enterTUIAdd(model tui.Model, rawURL string) (tui.Model, tea.Cmd) {
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	updated, _ = updated.(tui.Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(rawURL)})
	updated, command := updated.(tui.Model).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if command != nil {
		updated, _ = updated.(tui.Model).Update(command())
	}

	return updated.(tui.Model), command
}

func loadTUIModel(t *testing.T, client *tuiStatusClient) tui.Model {
	t.Helper()
	model, err := tui.NewModel(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	updated, _ := model.Update(model.Init()())

	return updated.(tui.Model)
}

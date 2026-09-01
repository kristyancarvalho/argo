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

func (client *tuiStatusClient) Remove(_ context.Context, id string) (ipc.DownloadActionResponse, error) {
	client.actions = append(client.actions, "remove:"+id)
	return ipc.DownloadActionResponse{ID: id, Status: "removed"}, client.actionErr
}

func (client *tuiStatusClient) Clear(context.Context) (ipc.ClearResponse, error) {
	client.actions = append(client.actions, "clear")
	return ipc.ClearResponse{Removed: 2}, client.actionErr
}

func (client *tuiStatusClient) Retry(_ context.Context, id string) (ipc.AddResponse, error) {
	client.actions = append(client.actions, "retry:"+id)
	return ipc.AddResponse{ID: "retried"}, client.actionErr
}

func (client *tuiStatusClient) Priority(_ context.Context, id, priority string) (ipc.PriorityResponse, error) {
	client.actions = append(client.actions, "priority:"+id+":"+priority)
	return ipc.PriorityResponse{ID: id, Priority: priority}, client.actionErr
}

func (client *tuiStatusClient) Profile(_ context.Context, name string) (ipc.ProfileResponse, error) {
	client.actions = append(client.actions, "profile:"+name)
	return ipc.ProfileResponse{Name: name}, client.actionErr
}

func (client *tuiStatusClient) Policy(_ context.Context, policy string) (ipc.PolicyResponse, error) {
	client.actions = append(client.actions, "policy:"+policy)
	return ipc.PolicyResponse{Policy: policy, Applied: true}, client.actionErr
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

func TestTUIModelColorRendering(t *testing.T) {
	model, err := tui.NewModelWithOptions(
		context.Background(),
		&tuiStatusClient{status: ipc.Status{State: "running"}},
		tui.Options{Color: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	updated, _ := model.Update(model.Init()())
	view := updated.(tui.Model).View()
	if !strings.Contains(view, "\x1b[") || !strings.Contains(view, "Argo") || !strings.Contains(view, "running") {
		t.Fatalf("colored TUI view is incomplete: %q", view)
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
	if !strings.Contains(view, "1.5KB/?") || !strings.Contains(view, "…") {
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
	if !strings.Contains(view, "/s") || strings.Contains(view, "Argo throughput: --") || strings.Contains(view, "speed.bin      1.0KB/10.0KB    --") {
		t.Fatalf("speed and ETA were not rendered: %q", view)
	}
}

func TestTUINetworkAndQoSConnected(t *testing.T) {
	model := loadTUIModel(t, &tuiStatusClient{status: ipc.Status{
		State: "running", Network: ipc.NetworkStatus{
			Available: true, Connected: true, State: "connected-global", Interface: "wlan0", Metered: "no",
		}, ActiveProfile: "gaming", Traffic: ipc.TrafficStatus{Policy: "balanced"},
	}})
	view := model.View()
	for _, value := range []string{"Network: connected-global", "Interface: wlan0", "Metered: no", "Profile: gaming", "Traffic policy: balanced"} {
		if !strings.Contains(view, value) {
			t.Fatalf("network view %q does not contain %q", view, value)
		}
	}
}

func TestTUINetworkDisconnectedAndQoSUnavailable(t *testing.T) {
	model := loadTUIModel(t, &tuiStatusClient{status: ipc.Status{
		State: "running", Network: ipc.NetworkStatus{Available: true, State: "disconnected"},
	}})
	view := model.View()
	for _, value := range []string{"Network: disconnected", "Interface: unknown", "Traffic policy: unknown", "Adaptive limit: unavailable", "Latency: unavailable"} {
		if !strings.Contains(view, value) {
			t.Fatalf("unavailable view %q does not contain %q", view, value)
		}
	}
}

func TestTUIAdaptivePolicyActive(t *testing.T) {
	model := loadTUIModel(t, &tuiStatusClient{status: ipc.Status{
		State: "running", Network: ipc.NetworkStatus{Available: true, Connected: true}, ActiveProfile: "responsive",
		Traffic: ipc.TrafficStatus{
			Policy: "latency", CurrentRateBitsPerSecond: 25_000_000, MeasuredLatency: 24 * time.Millisecond,
			LatencyAvailable: true, BaselineLatency: 18 * time.Millisecond, BaselineAvailable: true, ControllerState: "stable",
		},
	}})
	view := model.View()
	for _, value := range []string{"Adaptive limit: 25000000 bit/s", "Latency: 24ms (baseline 18ms)", "Controller: stable"} {
		if !strings.Contains(view, value) {
			t.Fatalf("adaptive view %q does not contain %q", view, value)
		}
	}
}

func TestTUIShowsQoSError(t *testing.T) {
	model := loadTUIModel(t, &tuiStatusClient{status: ipc.Status{
		State: "running", Traffic: ipc.TrafficStatus{Policy: "throughput", Error: "helper unavailable"},
	}})
	if !strings.Contains(model.View(), "QoS error: helper unavailable") {
		t.Fatalf("QoS error is missing from TUI: %q", model.View())
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

func TestTUILifecycleActionsRequireConfirmation(t *testing.T) {
	client := &tuiStatusClient{status: ipc.Status{State: "running"}, downloads: [][]ipc.Download{{{ID: "one", Status: "completed"}}}}
	model := loadTUIModel(t, client)
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	model = updated.(tui.Model)
	if !strings.Contains(model.View(), "Remove one from history? y/N") {
		t.Fatalf("remove confirmation is missing: %q", model.View())
	}
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if command == nil {
		t.Fatal("confirmed remove did not dispatch")
	}
	_ = command()
	model = updated.(tui.Model)
	updated, command = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	if command == nil {
		t.Fatal("retry did not dispatch")
	}
	_ = command()
	model = updated.(tui.Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'C'}})
	model = updated.(tui.Model)
	if !strings.Contains(model.View(), "Clear completed, failed, and canceled history? y/N") {
		t.Fatalf("clear confirmation is missing: %q", model.View())
	}
	_, command = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if command == nil {
		t.Fatal("confirmed clear did not dispatch")
	}
	_ = command()
	if strings.Join(client.actions, ",") != "remove:one,retry:one,clear" {
		t.Fatalf("lifecycle actions = %v", client.actions)
	}
}

func TestTUIHelpViewAndResponsiveViewport(t *testing.T) {
	downloads := make([]ipc.Download, 12)
	for index := range downloads {
		downloads[index] = ipc.Download{
			ID: strings.Repeat(string(rune('a'+index)), 32), Filename: strings.Repeat("long-name-", 6) + string(rune('a'+index)) + ".bin",
			URL: "https://example.test/" + strings.Repeat("path/", 12), Status: "downloading", Priority: "normal",
			DownloadedBytes: int64(index), TotalSize: 100,
		}
	}
	model := loadTUIModel(t, &tuiStatusClient{status: ipc.Status{State: "running"}, downloads: [][]ipc.Download{downloads}})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 48, Height: 18})
	model = updated.(tui.Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	help := updated.(tui.Model).View()
	if !strings.Contains(help, "Help") || !strings.Contains(help, "retry as a new transfer") {
		t.Fatalf("help view is incomplete: %q", help)
	}
	updated, _ = updated.(tui.Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	model = updated.(tui.Model)
	for index := 0; index < 10; index++ {
		updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
		model = updated.(tui.Model)
	}
	view := model.View()
	if !strings.Contains(view, "> long-name-") || !strings.Contains(view, "  kkkkkkk") || !strings.Contains(view, "above") {
		t.Fatalf("selection was not kept in the viewport: %q", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if len([]rune(line)) > 48 {
			t.Fatalf("narrow view line exceeds width: %d %q", len([]rune(line)), line)
		}
	}
}

func TestTUIStatesRemainDistinctWithoutColor(t *testing.T) {
	states := []string{"downloading", "paused", "completed", "failed", "canceled"}
	downloads := make([]ipc.Download, len(states))
	for index, state := range states {
		downloads[index] = ipc.Download{ID: state, Filename: state + ".bin", Status: state, Priority: "normal"}
	}
	model := loadTUIModel(t, &tuiStatusClient{status: ipc.Status{State: "running"}, downloads: [][]ipc.Download{downloads}})
	view := model.View()
	for _, state := range states {
		if !strings.Contains(view, state) {
			t.Fatalf("state %q is not visible without color: %q", state, view)
		}
	}
	if !strings.Contains(view, "0s") {
		t.Fatalf("completed ETA is not zero: %q", view)
	}
}

func TestTUIShortensListIDAndKeepsFullSelectedID(t *testing.T) {
	identifier := strings.Repeat("a", 32)
	model := loadTUIModel(t, &tuiStatusClient{
		status:    ipc.Status{State: "running"},
		downloads: [][]ipc.Download{{{ID: identifier, Filename: "file.bin", Status: "paused", Priority: "normal"}}},
	})
	view := model.View()
	if strings.Count(view, identifier) != 1 || !strings.Contains(view, "aaaaaaa…") {
		t.Fatalf("list/detail ID hierarchy is incorrect: %q", view)
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

func TestTUIPolicySelection(t *testing.T) {
	client := &tuiStatusClient{status: ipc.Status{State: "running"}}
	model := loadTUIModel(t, client)
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	if !strings.Contains(updated.(tui.Model).View(), "Select traffic policy") {
		t.Fatalf("policy selection not shown: %q", updated.(tui.Model).View())
	}
	updated, command := updated.(tui.Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'4'}})
	if command == nil {
		t.Fatal("policy selection did not dispatch")
	}
	updated, _ = updated.(tui.Model).Update(command())
	if strings.Join(client.actions, ",") != "policy:latency" || !strings.Contains(updated.(tui.Model).View(), "Traffic policy: latency") {
		t.Fatalf("unexpected policy result: actions=%v view=%q", client.actions, updated.(tui.Model).View())
	}
}

func TestTUIProfileSelection(t *testing.T) {
	client := &tuiStatusClient{status: ipc.Status{State: "running"}}
	model := loadTUIModel(t, client)
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	updated, _ = updated.(tui.Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("gaming")})
	updated, command := updated.(tui.Model).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if command == nil {
		t.Fatal("profile selection did not dispatch")
	}
	updated, _ = updated.(tui.Model).Update(command())
	if strings.Join(client.actions, ",") != "profile:gaming" || !strings.Contains(updated.(tui.Model).View(), "Active profile: gaming") {
		t.Fatalf("unexpected profile result: actions=%v view=%q", client.actions, updated.(tui.Model).View())
	}
}

func TestTUIPolicyHelperUnavailableDoesNotExit(t *testing.T) {
	client := &tuiStatusClient{status: ipc.Status{State: "running"}, actionErr: errors.New("privileged helper unavailable")}
	model := loadTUIModel(t, client)
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	updated, command := updated.(tui.Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	updated, followup := updated.(tui.Model).Update(command())
	if followup == nil || !strings.Contains(updated.(tui.Model).View(), "Action failed: privileged helper unavailable") {
		t.Fatalf("helper failure terminated or was hidden: %q", updated.(tui.Model).View())
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

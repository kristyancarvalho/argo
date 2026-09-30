package unit_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
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

func TestTUICtrlCQuitsFromEveryModalMode(t *testing.T) {
	for _, key := range []rune{'a', 'f', 'c', 'x', 'C', 't', '?'} {
		t.Run(string(key), func(t *testing.T) {
			client := &tuiStatusClient{
				status:    ipc.Status{State: "running"},
				downloads: [][]ipc.Download{{{ID: "one", Status: "completed"}}},
			}
			model := loadTUIModel(t, client)
			updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
			_, command := updated.(tui.Model).Update(tea.KeyMsg{Type: tea.KeyCtrlC})
			if command == nil {
				t.Fatalf("Ctrl+C in mode %q did not request exit", string(key))
			}
			if _, valid := command().(tea.QuitMsg); !valid {
				t.Fatalf("Ctrl+C in mode %q returned a non-quit message", string(key))
			}
		})
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

func TestTUIActionsKeepSinglePeriodicRefresh(t *testing.T) {
	client := &tuiStatusClient{
		status:    ipc.Status{State: "running"},
		downloads: [][]ipc.Download{{{ID: "one", Status: "paused"}}},
	}
	scheduled := 0
	model, err := tui.NewModelWithOptions(context.Background(), client, tui.Options{
		Tick: func(_ time.Duration, callback func(time.Time) tea.Msg) tea.Cmd {
			scheduled++
			return func() tea.Msg { return callback(time.Unix(int64(scheduled), 0)) }
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, periodic := model.Update(model.Init()())
	model = updated.(tui.Model)
	if periodic == nil || scheduled != 1 {
		t.Fatalf("initial snapshot scheduled %d periodic refreshes", scheduled)
	}
	for range 200 {
		updated, action := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
		if action == nil {
			t.Fatal("pause action was not dispatched")
		}
		updated, reload := updated.(tui.Model).Update(action())
		if reload == nil {
			t.Fatal("action result did not request a current snapshot")
		}
		updated, duplicate := updated.(tui.Model).Update(reload())
		if duplicate != nil {
			t.Fatal("action snapshot created another periodic refresh")
		}
		model = updated.(tui.Model)
	}
	if scheduled != 1 {
		t.Fatalf("200 actions left %d periodic refreshes", scheduled)
	}
	if client.listCall != 201 {
		t.Fatalf("200 actions made %d list requests, expected 201 including initial load", client.listCall)
	}
	updated, reload := model.Update(periodic())
	if reload == nil {
		t.Fatal("periodic refresh did not load a snapshot")
	}
	updated, next := updated.(tui.Model).Update(reload())
	if next == nil || scheduled != 2 {
		t.Fatalf("completed periodic cycle scheduled %d refreshes", scheduled)
	}
	_, stale := updated.(tui.Model).Update(periodic())
	if stale != nil {
		t.Fatal("stale periodic message started another refresh cycle")
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

func TestTUILargeETAIsUnknownInsteadOfNegative(t *testing.T) {
	client := &tuiStatusClient{
		status: ipc.Status{State: "running"},
		downloads: [][]ipc.Download{
			{{ID: "large", Filename: "large", TotalSize: math.MaxInt64, Status: "downloading"}},
			{{ID: "large", Filename: "large", DownloadedBytes: 1, TotalSize: math.MaxInt64, Status: "downloading"}},
			{{ID: "large", Filename: "large", DownloadedBytes: 2, TotalSize: math.MaxInt64, Status: "downloading"}},
		},
	}
	model := loadTUIModel(t, client)
	for range 2 {
		time.Sleep(5 * time.Millisecond)
		updated, _ := model.Update(model.Init()())
		model = updated.(tui.Model)
	}
	view := model.View()
	if strings.Contains(view, "-2562047") || !strings.Contains(view, "-- downloading") {
		t.Fatalf("large ETA was not rendered as unknown: %q", view)
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

func TestTUIBackgroundPolicyShowsAdaptiveDiagnostics(t *testing.T) {
	model := loadTUIModel(t, &tuiStatusClient{status: ipc.Status{
		State: "running", Traffic: ipc.TrafficStatus{
			Policy: "background", CurrentRateBitsPerSecond: 25_000_000,
			MeasuredLatency: 24 * time.Millisecond, LatencyAvailable: true,
			BaselineLatency: 18 * time.Millisecond, BaselineAvailable: true,
			ControllerState: "stable",
		},
	}})
	view := model.View()
	for _, value := range []string{"Traffic policy: background", "Adaptive limit: 25000000 bit/s", "Latency: 24ms (baseline 18ms)", "Controller: stable"} {
		if !strings.Contains(view, value) {
			t.Fatalf("background view %q does not contain %q", view, value)
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

func TestTUIRedactsSourceAndDiagnosticSecrets(t *testing.T) {
	secretURL := "https://user:password@example.test/file?token=secret"
	model := loadTUIModel(t, &tuiStatusClient{
		status: ipc.Status{State: "running", Network: ipc.NetworkStatus{
			Error: "GET " + secretURL + ": failed",
		}},
		downloads: [][]ipc.Download{{{ID: "one", URL: secretURL, Filename: "file", Status: "failed"}}},
	})
	view := model.View()
	if strings.Contains(view, "password") || strings.Contains(view, "token=secret") ||
		!strings.Contains(view, "https://redacted@example.test/file?redacted") {
		t.Fatalf("TUI exposed source credentials: %q", view)
	}
}

func TestTUIEscapesUntrustedTerminalControls(t *testing.T) {
	model := loadTUIModel(t, &tuiStatusClient{
		status:    ipc.Status{State: "run\x1b\nFAKE\u202e"},
		downloads: [][]ipc.Download{{{ID: "one", Filename: "file\x1b]0;title\a\nFAKE\r\t\u202e.iso", Status: "failed"}}},
	})
	view := model.View()
	for _, character := range []rune{'\x1b', '\a', '\r', '\t', '\u202e'} {
		if strings.ContainsRune(view, character) {
			t.Fatalf("TUI contains control U+%04X: %q", character, view)
		}
	}
	for _, line := range strings.Split(view, "\n") {
		if line == "FAKE" {
			t.Fatalf("TUI filename fabricated a row: %q", view)
		}
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

func TestTUIConfirmationNeverTargetsReplacementRow(t *testing.T) {
	for _, key := range []rune{'c', 'x'} {
		t.Run(string(key), func(t *testing.T) {
			status := "downloading"
			if key == 'x' {
				status = "completed"
			}
			client := &tuiStatusClient{
				status: ipc.Status{State: "running"},
				downloads: [][]ipc.Download{
					{{ID: "original", Status: status}, {ID: "replacement", Status: status}},
					{{ID: "replacement", Status: status}},
				},
			}
			model := loadTUIModel(t, client)
			updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
			model = updated.(tui.Model)
			updated, _ = model.Update(model.Init()())
			model = updated.(tui.Model)
			if !strings.Contains(model.View(), "confirmation canceled") {
				t.Fatalf("removed confirmation target remained active: %q", model.View())
			}
			_, command := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
			if command == nil {
				t.Fatal("invalid confirmation did not return an actionable result")
			}
			_ = command()
			if len(client.actions) != 0 {
				t.Fatalf("replacement row received actions: %v", client.actions)
			}
		})
	}
}

func TestTUIConfirmationAndSelectionFollowIdentityAcrossReorder(t *testing.T) {
	client := &tuiStatusClient{
		status: ipc.Status{State: "running"},
		downloads: [][]ipc.Download{
			{{ID: "original", Filename: "original.bin", Status: "downloading"}, {ID: "other", Filename: "other.bin", Status: "downloading"}},
			{{ID: "other", Filename: "other.bin", Status: "downloading"}, {ID: "original", Filename: "original.bin", Status: "downloading"}},
		},
	}
	model := loadTUIModel(t, client)
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	model = updated.(tui.Model)
	updated, _ = model.Update(model.Init()())
	model = updated.(tui.Model)
	if !strings.Contains(model.View(), "Cancel original? y/N") || !strings.Contains(model.View(), "> original.bin") {
		t.Fatalf("confirmation or selection did not follow identity: %q", model.View())
	}
	_, command := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if command == nil {
		t.Fatal("stable confirmation did not dispatch")
	}
	_ = command()
	if strings.Join(client.actions, ",") != "cancel:original" {
		t.Fatalf("confirmation targeted %v", client.actions)
	}
}

func TestTUIConfirmationIsCanceledAfterIncompatibleCompletion(t *testing.T) {
	client := &tuiStatusClient{
		status: ipc.Status{State: "running"},
		downloads: [][]ipc.Download{
			{{ID: "original", Status: "downloading"}},
			{{ID: "original", Status: "completed"}},
		},
	}
	model := loadTUIModel(t, client)
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	updated, _ = updated.(tui.Model).Update(model.Init()())
	model = updated.(tui.Model)
	if !strings.Contains(model.View(), "confirmation canceled") || strings.Contains(model.View(), "Cancel original? y/N") {
		t.Fatalf("completed target retained cancel confirmation: %q", model.View())
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

func TestTUIViewStaysInsideTerminalCellAndLineBounds(t *testing.T) {
	downloads := make([]ipc.Download, 20)
	for index := range downloads {
		downloads[index] = ipc.Download{
			ID:        strings.Repeat(string(rune('a'+index)), 32),
			Filename:  strings.Repeat("界e\u0301", 30),
			Status:    "downloading",
			Priority:  "normal",
			TotalSize: 100,
		}
	}
	client := &tuiStatusClient{
		status: ipc.Status{
			State:         "running",
			ActiveProfile: strings.Repeat("wide-profile-", 20),
			Network:       ipc.NetworkStatus{Available: true, Connected: true, State: "connected", Interface: strings.Repeat("interface", 20)},
			Traffic:       ipc.TrafficStatus{Policy: "throughput", Error: strings.Repeat("helper error ", 20)},
		},
		downloads: [][]ipc.Download{downloads},
	}
	base := loadTUIModel(t, client)
	for _, dimensions := range [][2]int{{20, 12}, {32, 12}, {71, 24}, {72, 24}, {80, 24}} {
		width, height := dimensions[0], dimensions[1]
		t.Run(fmt.Sprintf("%dx%d", width, height), func(t *testing.T) {
			updated, _ := base.Update(tea.WindowSizeMsg{Width: width, Height: height})
			model := updated.(tui.Model)
			for range 15 {
				updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
				model = updated.(tui.Model)
			}
			assertTUIViewBounds(t, model.View(), width, height)
			if !strings.Contains(ansi.Strip(model.View()), ">") {
				t.Fatalf("selected row is not visible: %q", model.View())
			}
			for _, key := range []rune{'?', 'a', 'f', 'x', 'C', 't'} {
				modal, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
				assertTUIViewBounds(t, modal.(tui.Model).View(), width, height)
			}
		})
	}
}

func TestTUIViewBoundsLongDaemonAndActionErrors(t *testing.T) {
	width, height := 20, 8
	unavailable := loadTUIModel(t, &tuiStatusClient{err: errors.New(strings.Repeat("connection failure ", 30))})
	updated, _ := unavailable.Update(tea.WindowSizeMsg{Width: width, Height: height})
	assertTUIViewBounds(t, updated.(tui.Model).View(), width, height)

	client := &tuiStatusClient{
		status:    ipc.Status{State: "running"},
		downloads: [][]ipc.Download{{{ID: "one", Filename: "file", Status: "downloading"}}},
		actionErr: errors.New(strings.Repeat("action failure ", 30)),
	}
	model := loadTUIModel(t, client)
	updated, _ = model.Update(tea.WindowSizeMsg{Width: width, Height: height})
	updated, command := updated.(tui.Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	updated, _ = updated.(tui.Model).Update(command())
	assertTUIViewBounds(t, updated.(tui.Model).View(), width, height)
}

func assertTUIViewBounds(t *testing.T, view string, width, height int) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(view, "\n"), "\n")
	if len(lines) > height {
		t.Fatalf("view has %d lines for height %d: %q", len(lines), height, view)
	}
	for _, line := range lines {
		if measured := ansi.StringWidth(line); measured > width {
			t.Fatalf("view line occupies %d cells for width %d: %q", measured, width, line)
		}
	}
}

func TestTUIStatesRemainDistinctWithoutColor(t *testing.T) {
	states := []string{"downloading", "verifying", "paused", "completed", "failed", "canceled"}
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

func TestTUIBackgroundPolicySelection(t *testing.T) {
	client := &tuiStatusClient{status: ipc.Status{State: "running"}}
	model := loadTUIModel(t, client)
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	updated, command := updated.(tui.Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'6'}})
	if command == nil {
		t.Fatal("background policy selection did not dispatch")
	}
	updated, _ = updated.(tui.Model).Update(command())
	if strings.Join(client.actions, ",") != "policy:background" || !strings.Contains(updated.(tui.Model).View(), "Traffic policy: background") {
		t.Fatalf("unexpected background result: actions=%v view=%q", client.actions, updated.(tui.Model).View())
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

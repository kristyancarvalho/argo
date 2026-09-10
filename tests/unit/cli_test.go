package unit_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/kristyancarvalho/argo/internal/cli"
	"github.com/kristyancarvalho/argo/internal/ipc"
)

type cliClient struct {
	called string
	watch  bool
}

type unsafeTerminalClient struct {
	*cliClient
}

const unsafeTerminalName = "safe\x1b[31m\x1b]0;title\a\nFAKE\r\t\u202e.iso"

func (client *unsafeTerminalClient) Add(context.Context, string, string) (ipc.AddResponse, error) {
	return ipc.AddResponse{ID: "id", Filename: unsafeTerminalName, Status: "queued"}, nil
}

func (client *unsafeTerminalClient) AddWithChecksum(context.Context, string, string, string) (ipc.AddResponse, error) {
	return ipc.AddResponse{ID: "id", Filename: unsafeTerminalName, Status: "queued"}, nil
}

func (client *unsafeTerminalClient) List(context.Context) ([]ipc.Download, error) {
	return []ipc.Download{{ID: "id", Filename: unsafeTerminalName, Status: "queued", TotalSize: -1}}, nil
}

func (client *unsafeTerminalClient) Show(context.Context, string) (ipc.Download, error) {
	return ipc.Download{ID: "id", Filename: unsafeTerminalName, URL: "https://example.test/file", Status: "queued"}, nil
}

func TestCLIHumanCommandsEscapeUntrustedFilenameControls(t *testing.T) {
	client := &unsafeTerminalClient{cliClient: &cliClient{}}
	for _, arguments := range [][]string{{"add", "https://example.test/file"}, {"list"}, {"show", "id"}, {"watch"}} {
		var output bytes.Buffer
		if err := cli.Run(context.Background(), client, &output, arguments); err != nil {
			t.Fatalf("%v returned %v", arguments, err)
		}
		assertTerminalSafeOutput(t, output.String())
	}
}

func assertTerminalSafeOutput(t *testing.T, output string) {
	t.Helper()
	for _, character := range []rune{'\x1b', '\a', '\r', '\t', '\u202e'} {
		if strings.ContainsRune(output, character) {
			t.Fatalf("output contains control U+%04X: %q", character, output)
		}
	}
	for _, line := range strings.Split(output, "\n") {
		if line == "FAKE" {
			t.Fatalf("filename fabricated an output row: %q", output)
		}
	}
}

func (client *cliClient) Add(_ context.Context, rawURL, _ string) (ipc.AddResponse, error) {
	client.called = "add:" + rawURL
	return ipc.AddResponse{ID: "download-id", Filename: "file.bin", Status: "queued"}, nil
}

func (client *cliClient) AddWithChecksum(_ context.Context, rawURL, _, checksum string) (ipc.AddResponse, error) {
	client.called = "add:" + rawURL
	if checksum != "" {
		client.called += ":" + checksum
	}
	return ipc.AddResponse{ID: "download-id", Filename: "file.bin", Status: "queued"}, nil
}

func (client *cliClient) List(context.Context) ([]ipc.Download, error) {
	client.called = "list"
	status := "downloading"
	if client.watch {
		status = "completed"
	}
	return []ipc.Download{{
		ID:              "download-id",
		Filename:        "file.bin",
		Status:          status,
		DownloadedBytes: 5,
		TotalSize:       10,
	}}, nil
}

func (client *cliClient) Show(_ context.Context, id string) (ipc.Download, error) {
	client.called = "show:" + id
	return ipc.Download{
		ID:              id,
		URL:             "https://example.test/file.bin",
		Destination:     "/tmp",
		Filename:        "file.bin",
		Status:          "paused",
		Priority:        "normal",
		DownloadedBytes: 5,
		TotalSize:       10,
	}, nil
}

func (client *cliClient) Pause(_ context.Context, id string) (ipc.DownloadActionResponse, error) {
	client.called = "pause:" + id
	return ipc.DownloadActionResponse{ID: id, Status: "paused"}, nil
}

func (client *cliClient) Resume(_ context.Context, id string) (ipc.DownloadActionResponse, error) {
	client.called = "resume:" + id
	return ipc.DownloadActionResponse{ID: id, Status: "downloading"}, nil
}

func (client *cliClient) Cancel(_ context.Context, id string) (ipc.DownloadActionResponse, error) {
	client.called = "cancel:" + id
	return ipc.DownloadActionResponse{ID: id, Status: "canceled"}, nil
}

func (client *cliClient) Remove(_ context.Context, id string) (ipc.DownloadActionResponse, error) {
	client.called = "remove:" + id
	return ipc.DownloadActionResponse{ID: id, Status: "removed"}, nil
}

func (client *cliClient) Clear(context.Context) (ipc.ClearResponse, error) {
	client.called = "clear"
	return ipc.ClearResponse{Removed: 2}, nil
}

func (client *cliClient) Retry(_ context.Context, id string) (ipc.AddResponse, error) {
	client.called = "retry:" + id
	return ipc.AddResponse{ID: "retry-id", Filename: "file.bin", Status: "queued"}, nil
}

func (client *cliClient) Verify(_ context.Context, id string) (ipc.VerifyResponse, error) {
	client.called = "verify:" + id
	return ipc.VerifyResponse{ID: id, Checksum: "sha256:abcd", Matched: true}, nil
}

func (client *cliClient) Priority(_ context.Context, id, priority string) (ipc.PriorityResponse, error) {
	client.called = "priority:" + id + ":" + priority
	return ipc.PriorityResponse{ID: id, Priority: priority}, nil
}

func (client *cliClient) Status(context.Context) (ipc.Status, error) {
	client.called = "status"
	return ipc.Status{
		State:           "running",
		PID:             42,
		StartedAt:       time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC),
		ProtocolVersion: ipc.ProtocolVersion,
		Network: ipc.NetworkStatus{
			Available:      true,
			Connected:      true,
			State:          "connected-global",
			ConnectionType: "802-11-wireless",
			Interface:      "wlan0",
			Metered:        "no",
		},
		ActiveProfile: "gaming",
		Traffic: ipc.TrafficStatus{
			Policy: "latency", Applied: true, CurrentRateBitsPerSecond: 50_000_000,
			MeasuredLatency: 25 * time.Millisecond, LatencyAvailable: true,
			BaselineLatency: 20 * time.Millisecond, BaselineAvailable: true,
			ControllerState: "stable",
		},
	}, nil
}

func TestCLIStatusShowsNetworkAndProfile(t *testing.T) {
	client := &cliClient{}
	var output bytes.Buffer
	if err := cli.Run(context.Background(), client, &output, []string{"status"}); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		"Network: connected-global",
		"Interface: wlan0",
		"Connection type: 802-11-wireless",
		"Metered: no",
		"Active profile: gaming",
		"Traffic policy: latency",
		"QoS shaping: active",
		"Current limit: 50000000 bit/s",
		"Measured latency: 25ms",
		"Baseline latency: 20ms",
		"Controller: stable",
	} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("status output %q does not contain %q", output.String(), value)
		}
	}
}

type secretShowClient struct {
	*cliClient
}

func (client *secretShowClient) Show(context.Context, string) (ipc.Download, error) {
	return ipc.Download{
		ID: "secret", URL: "https://user:password@example.test/file?token=secret",
		Error: "GET https://user:password@example.test/file?token=secret: failed",
	}, nil
}

func TestCLIShowRedactsSourceAndErrorSecrets(t *testing.T) {
	var output bytes.Buffer
	client := &secretShowClient{cliClient: &cliClient{}}
	if err := cli.Run(context.Background(), client, &output, []string{"show", "secret"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "password") || strings.Contains(output.String(), "token=secret") ||
		!strings.Contains(output.String(), "https://redacted@example.test/file?redacted") {
		t.Fatalf("show output did not redact secrets: %q", output.String())
	}
}

type unavailableNetworkClient struct {
	*cliClient
}

func (client *unavailableNetworkClient) Status(context.Context) (ipc.Status, error) {
	status, err := client.cliClient.Status(context.Background())
	status.Network.Available = false
	status.Network.Error = "NetworkManager disconnected"

	return status, err
}

func TestCLIStatusShowsNetworkObserverFailure(t *testing.T) {
	client := &unavailableNetworkClient{cliClient: &cliClient{}}
	var output bytes.Buffer
	if err := cli.Run(context.Background(), client, &output, []string{"status"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Network: unavailable") ||
		!strings.Contains(output.String(), "Network error: NetworkManager disconnected") {
		t.Fatalf("status output does not expose observer failure: %q", output.String())
	}
}

func (client *cliClient) Profile(_ context.Context, name string) (ipc.ProfileResponse, error) {
	client.called = "profile:" + name
	return ipc.ProfileResponse{Name: name, Policy: "balanced", PolicyApplied: true}, nil
}

func TestCLIShowsQoSDiagnostics(t *testing.T) {
	client := &cliClient{}
	response := ipc.ProfileResponse{Name: "gaming", Policy: "throughput", QoSError: "helper unavailable"}
	client.called = ""
	var output bytes.Buffer
	wrapper := &profileDiagnosticClient{cliClient: client, response: response}
	if err := cli.Run(context.Background(), wrapper, &output, []string{"profile", "gaming"}); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"Active profile: gaming", "Traffic policy: throughput (inactive)", "QoS warning: helper unavailable"} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("profile output %q does not contain %q", output.String(), value)
		}
	}
	output.Reset()
	statusClient := &statusDiagnosticClient{cliClient: client}
	if err := cli.Run(context.Background(), statusClient, &output, []string{"status"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "QoS error: helper unavailable") {
		t.Fatalf("status output hid QoS error: %q", output.String())
	}
}

type profileDiagnosticClient struct {
	*cliClient
	response ipc.ProfileResponse
}

func (client *profileDiagnosticClient) Profile(context.Context, string) (ipc.ProfileResponse, error) {
	return client.response, nil
}

type statusDiagnosticClient struct {
	*cliClient
}

func (client *statusDiagnosticClient) Status(context.Context) (ipc.Status, error) {
	status, err := client.cliClient.Status(context.Background())
	status.Traffic.Error = "helper unavailable"

	return status, err
}

func (client *cliClient) Policy(_ context.Context, policy string) (ipc.PolicyResponse, error) {
	client.called = "policy:" + policy
	return ipc.PolicyResponse{Policy: policy, Applied: policy != "off"}, nil
}

func TestCLICommands(t *testing.T) {
	tests := []struct {
		name           string
		arguments      []string
		expectedCall   string
		expectedOutput string
	}{
		{"add", []string{"add", "https://example.test/file.bin"}, "add:https://example.test/file.bin", "Added download-id"},
		{"add checksum", []string{"add", "--checksum", "sha256:abcd", "https://example.test/file.bin"}, "add:https://example.test/file.bin:sha256:abcd", "Added download-id"},
		{"list", []string{"list"}, "list", "downloading"},
		{"show", []string{"show", "download-id"}, "show:download-id", "Status: paused"},
		{"pause", []string{"pause", "download-id"}, "pause:download-id", "download-id: paused"},
		{"resume", []string{"resume", "download-id"}, "resume:download-id", "download-id: downloading"},
		{"cancel", []string{"cancel", "download-id"}, "cancel:download-id", "download-id: canceled"},
		{"remove", []string{"remove", "download-id"}, "remove:download-id", "download-id: removed"},
		{"clear", []string{"clear"}, "clear", "Removed 2 historical downloads"},
		{"retry", []string{"retry", "download-id"}, "retry:download-id", "Added retry-id"},
		{"verify", []string{"verify", "download-id"}, "verify:download-id", "Verified download-id"},
		{"priority", []string{"priority", "download-id", "high"}, "priority:download-id:high", "download-id: high"},
		{"status", []string{"status"}, "status", "Daemon: running"},
		{"profile", []string{"profile", "gaming"}, "profile:gaming", "Active profile: gaming"},
		{"policy", []string{"policy", "balanced"}, "policy:balanced", "Traffic policy: balanced (active)"},
		{"policy off", []string{"policy", "off"}, "policy:off", "Traffic policy: off (inactive)"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &cliClient{}
			var output bytes.Buffer
			if err := cli.Run(context.Background(), client, &output, test.arguments); err != nil {
				t.Fatal(err)
			}
			if client.called != test.expectedCall {
				t.Fatalf("called %q, expected %q", client.called, test.expectedCall)
			}
			if !strings.Contains(output.String(), test.expectedOutput) {
				t.Fatalf("output %q does not contain %q", output.String(), test.expectedOutput)
			}
		})
	}
}

func TestCLIRejectsInvalidArguments(t *testing.T) {
	tests := [][]string{
		nil,
		{"unknown"},
		{"add"},
		{"add", "one", "two"},
		{"add", "--checksum"},
		{"add", "--checksum", "one", "--checksum", "two", "url"},
		{"list", "extra"},
		{"show"},
		{"pause"},
		{"resume"},
		{"cancel"},
		{"remove"},
		{"clear", "extra"},
		{"retry"},
		{"verify"},
		{"verify", "one", "two"},
		{"priority"},
		{"priority", "download-id"},
		{"priority", "download-id", "high", "extra"},
		{"watch", "extra"},
		{"status", "extra"},
		{"profile"},
		{"profile", "one", "two"},
		{"policy"},
		{"policy", "off", "extra"},
	}

	for _, arguments := range tests {
		t.Run(strings.Join(arguments, "_"), func(t *testing.T) {
			client := &cliClient{}
			err := cli.Run(context.Background(), client, &bytes.Buffer{}, arguments)
			var usageError cli.UsageError
			if !errors.As(err, &usageError) {
				t.Fatalf("arguments %v returned %T, expected UsageError", arguments, err)
			}
			if client.called != "" {
				t.Fatalf("invalid arguments called %q", client.called)
			}
		})
	}
}

func TestCLIHelpAliases(t *testing.T) {
	for _, command := range []string{"help", "-h", "--help"} {
		var output bytes.Buffer
		if err := cli.Run(context.Background(), nil, &output, []string{command}); err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		for _, value := range []string{
			"Usage:", "add [--checksum sha256:<hex>] <url>", "verify <id>", "Traffic policies:",
			"Priorities only order queued downloads inside Argo", "privileged argo-qosd helper",
		} {
			if !strings.Contains(output.String(), value) {
				t.Fatalf("help output %q does not contain %q", output.String(), value)
			}
		}
	}
	err := cli.Run(context.Background(), nil, &bytes.Buffer{}, []string{"help", "status"})
	var usageError cli.UsageError
	if !errors.As(err, &usageError) {
		t.Fatalf("help operand returned %T, expected UsageError", err)
	}
}

func TestCLIColorRenderingCanBeEnabled(t *testing.T) {
	client := &cliClient{}
	var colored bytes.Buffer
	if err := cli.RunWithOptions(
		context.Background(), client, &colored, []string{"status"}, cli.Options{Color: true},
	); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(colored.String(), "\x1b[") || !strings.Contains(colored.String(), "running") {
		t.Fatalf("colored output is missing ANSI styling: %q", colored.String())
	}
	var plain bytes.Buffer
	if err := cli.Run(context.Background(), client, &plain, []string{"status"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain.String(), "\x1b[") {
		t.Fatalf("plain output contains ANSI styling: %q", plain.String())
	}
	colored.Reset()
	if err := cli.RunWithOptions(
		context.Background(), nil, &colored, []string{"--help"}, cli.Options{Color: true},
	); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(colored.String(), "\x1b[") || !strings.Contains(colored.String(), "Traffic policies") {
		t.Fatalf("colored help is missing styling: %q", colored.String())
	}
}

func TestCLIWatchCompletedDownload(t *testing.T) {
	client := &cliClient{watch: true}
	var output bytes.Buffer
	if err := cli.Run(context.Background(), client, &output, []string{"watch"}); err != nil {
		t.Fatal(err)
	}
	if client.called != "list" {
		t.Fatalf("watch called %q, expected list", client.called)
	}
	if !strings.Contains(output.String(), "completed") || !strings.Contains(output.String(), "total 0 B/s") {
		t.Fatalf("unexpected watch output %q", output.String())
	}
}

type redrawWatchClient struct {
	*cliClient
	calls int
}

type geometryWatchClient struct {
	*cliClient
	calls int
}

func (client *redrawWatchClient) List(context.Context) ([]ipc.Download, error) {
	client.calls++
	status := "downloading"
	bytes := int64(5)
	if client.calls > 1 {
		status = "completed"
		bytes = 10
	}

	return []ipc.Download{{
		ID: "download-id", Filename: "file.bin", Status: status, DownloadedBytes: bytes, TotalSize: 10,
	}}, nil
}

func TestCLIWatchRedrawsInteractiveRegion(t *testing.T) {
	client := &redrawWatchClient{cliClient: &cliClient{}}
	var output bytes.Buffer
	if err := cli.RunWithOptions(
		context.Background(), client, &output, []string{"watch"}, cli.Options{Interactive: true},
	); err != nil {
		t.Fatal(err)
	}
	if client.calls != 2 {
		t.Fatalf("interactive watch made %d list calls", client.calls)
	}
	if !strings.Contains(output.String(), "\x1b[") || !strings.Contains(output.String(), "\x1b[2K") {
		t.Fatalf("interactive watch did not redraw in place: %q", output.String())
	}
}

func TestCLIWatchNonInteractiveEmitsOnePlainSnapshot(t *testing.T) {
	client := &redrawWatchClient{cliClient: &cliClient{}}
	var output bytes.Buffer
	if err := cli.Run(context.Background(), client, &output, []string{"watch"}); err != nil {
		t.Fatal(err)
	}
	if client.calls != 1 {
		t.Fatalf("non-interactive watch made %d list calls", client.calls)
	}
	if strings.Contains(output.String(), "\x1b[") || !strings.Contains(output.String(), "downloading") {
		t.Fatalf("non-interactive watch output is not a plain snapshot: %q", output.String())
	}
}

func TestCLIWatchFitsTerminalWidthAndHeight(t *testing.T) {
	for _, width := range []int{32, 80, 120} {
		t.Run(fmt.Sprintf("width-%d", width), func(t *testing.T) {
			client := &geometryWatchClient{cliClient: &cliClient{}, calls: 1}
			var output bytes.Buffer
			if err := cli.RunWithOptions(
				context.Background(), client, &output, []string{"watch"}, cli.Options{
					Interactive: true,
					TerminalSize: func() (int, int) {
						return width, 6
					},
				},
			); err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
			if len(lines) > 5 {
				t.Fatalf("rendered %d rows into a six-row terminal", len(lines))
			}
			for _, line := range lines {
				if measured := ansi.StringWidth(ansi.Strip(line)); measured > width {
					t.Fatalf("line occupies %d cells at width %d: %q", measured, width, line)
				}
			}
			if !strings.Contains(output.String(), "more") {
				t.Fatalf("oversized list was not visibly clipped: %q", output.String())
			}
		})
	}
}

func TestCLIWatchClearsAndRecomputesRegionAfterResize(t *testing.T) {
	client := &geometryWatchClient{cliClient: &cliClient{}}
	sizes := [][2]int{{120, 7}, {32, 5}}
	index := 0
	var output bytes.Buffer
	if err := cli.RunWithOptions(
		context.Background(), client, &output, []string{"watch"}, cli.Options{
			Interactive: true,
			TerminalSize: func() (int, int) {
				size := sizes[min(index, len(sizes)-1)]
				index++
				return size[0], size[1]
			},
		},
	); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "\x1b[2J\x1b[H") {
		t.Fatalf("resize did not clear stale wrapped rows: %q", output.String())
	}
	afterResize := output.String()[strings.LastIndex(output.String(), "\x1b[H")+len("\x1b[H"):]
	for _, line := range strings.Split(strings.TrimSuffix(afterResize, "\n"), "\n") {
		if measured := ansi.StringWidth(ansi.Strip(line)); measured > 32 {
			t.Fatalf("resized line occupies %d cells: %q", measured, line)
		}
	}
}

func (client *geometryWatchClient) List(context.Context) ([]ipc.Download, error) {
	client.calls++
	status := "downloading"
	if client.calls > 1 {
		status = "completed"
	}
	downloads := make([]ipc.Download, 0, 20)
	for index := range 20 {
		downloads = append(downloads, ipc.Download{
			ID:              fmt.Sprintf("download-%02d", index),
			Filename:        strings.Repeat("界", 80) + fmt.Sprintf("-%02d.bin", index),
			Status:          status,
			Priority:        "normal",
			DownloadedBytes: int64(index),
			TotalSize:       100,
		})
	}

	return downloads, nil
}

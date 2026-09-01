package unit_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/cli"
	"github.com/kristyancarvalho/argo/internal/ipc"
)

type cliClient struct {
	called string
	watch  bool
}

func (client *cliClient) Add(_ context.Context, rawURL, _ string) (ipc.AddResponse, error) {
	client.called = "add:" + rawURL
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
		{"list", []string{"list"}, "list", "downloading"},
		{"show", []string{"show", "download-id"}, "show:download-id", "Status: paused"},
		{"pause", []string{"pause", "download-id"}, "pause:download-id", "download-id: paused"},
		{"resume", []string{"resume", "download-id"}, "resume:download-id", "download-id: downloading"},
		{"cancel", []string{"cancel", "download-id"}, "cancel:download-id", "download-id: canceled"},
		{"remove", []string{"remove", "download-id"}, "remove:download-id", "download-id: removed"},
		{"clear", []string{"clear"}, "clear", "Removed 2 historical downloads"},
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
		{"list", "extra"},
		{"show"},
		{"pause"},
		{"resume"},
		{"cancel"},
		{"remove"},
		{"clear", "extra"},
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
			"Usage:", "add <url>", "Traffic policies:",
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

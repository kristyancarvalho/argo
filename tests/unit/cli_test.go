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
	} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("status output %q does not contain %q", output.String(), value)
		}
	}
}

func (client *cliClient) Profile(_ context.Context, name string) (ipc.ProfileResponse, error) {
	client.called = "profile:" + name
	return ipc.ProfileResponse{Name: name}, nil
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

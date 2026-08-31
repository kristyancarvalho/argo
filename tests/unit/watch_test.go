package unit_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/cli"
	"github.com/kristyancarvalho/argo/internal/ipc"
)

type watchSequenceClient struct {
	snapshots [][]ipc.Download
	index     int
}

func (client *watchSequenceClient) List(context.Context) ([]ipc.Download, error) {
	index := client.index
	if index >= len(client.snapshots) {
		index = len(client.snapshots) - 1
	}
	client.index++

	return client.snapshots[index], nil
}

func TestWatchProgressStreamUnknownETAAndCompletedTransition(t *testing.T) {
	startedAt := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	ticks := make(chan time.Time, 2)
	ticks <- startedAt.Add(time.Second)
	ticks <- startedAt.Add(2 * time.Second)
	client := &watchSequenceClient{snapshots: [][]ipc.Download{
		{
			{ID: "known", Filename: "known.bin", Status: "downloading", DownloadedBytes: 100, TotalSize: 400},
			{ID: "unknown", Filename: "unknown.bin", Status: "downloading", DownloadedBytes: 50, TotalSize: -1},
		},
		{
			{ID: "known", Filename: "known.bin", Status: "downloading", DownloadedBytes: 200, TotalSize: 400},
			{ID: "unknown", Filename: "unknown.bin", Status: "downloading", DownloadedBytes: 250, TotalSize: -1},
		},
		{
			{ID: "known", Filename: "known.bin", Status: "completed", DownloadedBytes: 400, TotalSize: 400},
			{ID: "unknown", Filename: "unknown.bin", Status: "completed", DownloadedBytes: 300, TotalSize: -1},
		},
	}}
	watcher := cli.NewWatcherWithOptions(client, cli.WatchOptions{
		Now:   func() time.Time { return startedAt },
		Ticks: ticks,
	})
	snapshots := make([]cli.WatchSnapshot, 0, 3)
	if err := watcher.Stream(context.Background(), func(snapshot cli.WatchSnapshot) error {
		snapshots = append(snapshots, snapshot)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if len(snapshots) != 3 {
		t.Fatalf("received %d snapshots, expected 3", len(snapshots))
	}
	if snapshots[0].Downloads[0].ETASeconds != nil || snapshots[0].Downloads[1].ETASeconds != nil {
		t.Fatal("initial snapshot unexpectedly calculated an ETA")
	}
	second := snapshots[1]
	if second.AggregateBytesPerSecond != 300 {
		t.Fatalf("aggregate speed is %.0f, expected 300", second.AggregateBytesPerSecond)
	}
	if second.Downloads[0].BytesPerSecond != 100 || second.Downloads[1].BytesPerSecond != 200 {
		t.Fatalf("unexpected per-download speeds: %+v", second.Downloads)
	}
	if second.Downloads[0].ETASeconds == nil || *second.Downloads[0].ETASeconds != 2 {
		t.Fatalf("known ETA is %v, expected 2", second.Downloads[0].ETASeconds)
	}
	if second.Downloads[1].ETASeconds != nil {
		t.Fatalf("unknown-size ETA is %v, expected nil", second.Downloads[1].ETASeconds)
	}
	for _, download := range snapshots[2].Downloads {
		if download.Status != "completed" || download.ETASeconds == nil || *download.ETASeconds != 0 {
			t.Fatalf("unexpected completed transition: %+v", download)
		}
	}
}

func TestWatchSnapshotHasStableStructuredFields(t *testing.T) {
	snapshot := cli.WatchSnapshot{
		Timestamp:               time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC),
		AggregateBytesPerSecond: 128,
		Downloads: []cli.WatchDownload{{
			ID:              "download",
			Filename:        "file.bin",
			Status:          "downloading",
			Priority:        "normal",
			DownloadedBytes: 64,
			TotalSize:       256,
			BytesPerSecond:  64,
		}},
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"timestamp", "aggregate_bytes_per_second", "downloads"} {
		if _, exists := fields[field]; !exists {
			t.Fatalf("structured snapshot is missing %q: %s", field, encoded)
		}
	}
	downloads, valid := fields["downloads"].([]any)
	if !valid || len(downloads) != 1 {
		t.Fatalf("structured downloads have unexpected form: %s", encoded)
	}
	download, valid := downloads[0].(map[string]any)
	if !valid {
		t.Fatalf("structured download has unexpected form: %s", encoded)
	}
	for _, field := range []string{
		"id",
		"filename",
		"status",
		"priority",
		"downloaded_bytes",
		"total_size",
		"bytes_per_second",
		"eta_seconds",
	} {
		if _, exists := download[field]; !exists {
			t.Fatalf("structured download is missing %q: %s", field, encoded)
		}
	}
}

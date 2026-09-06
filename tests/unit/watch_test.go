package unit_test

import (
	"context"
	"encoding/json"
	"math"
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
	ticks := make(chan time.Time, 3)
	ticks <- startedAt.Add(time.Second)
	ticks <- startedAt.Add(2 * time.Second)
	ticks <- startedAt.Add(3 * time.Second)
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
			{ID: "known", Filename: "known.bin", Status: "downloading", DownloadedBytes: 300, TotalSize: 400},
			{ID: "unknown", Filename: "unknown.bin", Status: "downloading", DownloadedBytes: 275, TotalSize: -1},
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

	if len(snapshots) != 4 {
		t.Fatalf("received %d snapshots, expected 4", len(snapshots))
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
	if second.Downloads[0].ETASeconds != nil {
		t.Fatalf("ETA was calculated from only one speed sample: %v", second.Downloads[0].ETASeconds)
	}
	if second.Downloads[1].ETASeconds != nil {
		t.Fatalf("unknown-size ETA is %v, expected nil", second.Downloads[1].ETASeconds)
	}
	if snapshots[2].Downloads[0].ETASeconds == nil || *snapshots[2].Downloads[0].ETASeconds != 1 {
		t.Fatalf("smoothed ETA is %v, expected 1", snapshots[2].Downloads[0].ETASeconds)
	}
	for _, download := range snapshots[3].Downloads {
		if download.Status != "completed" || download.ETASeconds == nil || *download.ETASeconds != 0 {
			t.Fatalf("unexpected completed transition: %+v", download)
		}
	}
}

func TestWatchETASmoothingAndDisplayDebounce(t *testing.T) {
	startedAt := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	ticks := make(chan time.Time, 5)
	for _, offset := range []time.Duration{time.Second, 2 * time.Second, 3 * time.Second, 5 * time.Second, 6 * time.Second} {
		ticks <- startedAt.Add(offset)
	}
	client := &watchSequenceClient{snapshots: [][]ipc.Download{
		{{ID: "variable", Status: "downloading", DownloadedBytes: 0, TotalSize: 10_000}},
		{{ID: "variable", Status: "downloading", DownloadedBytes: 100, TotalSize: 10_000}},
		{{ID: "variable", Status: "downloading", DownloadedBytes: 1_000, TotalSize: 10_000}},
		{{ID: "variable", Status: "downloading", DownloadedBytes: 1_100, TotalSize: 10_000}},
		{{ID: "variable", Status: "downloading", DownloadedBytes: 2_000, TotalSize: 10_000}},
		{{ID: "variable", Status: "completed", DownloadedBytes: 10_000, TotalSize: 10_000}},
	}}
	watcher := cli.NewWatcherWithOptions(client, cli.WatchOptions{
		Now: func() time.Time { return startedAt }, Ticks: ticks, ETAInterval: 3 * time.Second,
	})
	var snapshots []cli.WatchSnapshot
	if err := watcher.Stream(context.Background(), func(snapshot cli.WatchSnapshot) error {
		snapshots = append(snapshots, snapshot)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	firstVisible := snapshots[2].Downloads[0].ETASeconds
	debounced := snapshots[3].Downloads[0].ETASeconds
	updated := snapshots[4].Downloads[0].ETASeconds
	if firstVisible == nil || debounced == nil || updated == nil {
		t.Fatalf("expected visible ETAs: %v %v %v", firstVisible, debounced, updated)
	}
	if *firstVisible != *debounced {
		t.Fatalf("ETA changed inside debounce window: %.3f to %.3f", *firstVisible, *debounced)
	}
	if *updated == *debounced {
		t.Fatalf("ETA did not update after debounce window: %.3f", *updated)
	}
}

func TestWatchZeroSpeedAndResumedSamplesRequireReliableData(t *testing.T) {
	startedAt := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	ticks := make(chan time.Time, 4)
	for index := 1; index <= 4; index++ {
		ticks <- startedAt.Add(time.Duration(index) * time.Second)
	}
	client := &watchSequenceClient{snapshots: [][]ipc.Download{
		{{ID: "resume", Status: "paused", DownloadedBytes: 100, TotalSize: 500}},
		{{ID: "resume", Status: "downloading", DownloadedBytes: 100, TotalSize: 500}},
		{{ID: "resume", Status: "downloading", DownloadedBytes: 200, TotalSize: 500}},
		{{ID: "resume", Status: "downloading", DownloadedBytes: 300, TotalSize: 500}},
		{{ID: "resume", Status: "completed", DownloadedBytes: 500, TotalSize: 500}},
	}}
	watcher := cli.NewWatcherWithOptions(client, cli.WatchOptions{Now: func() time.Time { return startedAt }, Ticks: ticks})
	var snapshots []cli.WatchSnapshot
	if err := watcher.Stream(context.Background(), func(snapshot cli.WatchSnapshot) error {
		snapshots = append(snapshots, snapshot)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		if snapshots[index].Downloads[0].ETASeconds != nil {
			t.Fatalf("resumed snapshot %d has premature ETA %v", index, snapshots[index].Downloads[0].ETASeconds)
		}
	}
	if snapshots[3].Downloads[0].ETASeconds == nil {
		t.Fatal("resumed download did not produce ETA after two reliable samples")
	}
}

func TestWatchLargeETAIsUnknownInsteadOfOverflowing(t *testing.T) {
	startedAt := time.Date(2026, 9, 6, 1, 2, 3, 0, time.UTC)
	ticks := make(chan time.Time, 3)
	for index := 1; index <= 3; index++ {
		ticks <- startedAt.Add(time.Duration(index) * time.Second)
	}
	client := &watchSequenceClient{snapshots: [][]ipc.Download{
		{{ID: "large", Status: "downloading", TotalSize: math.MaxInt64}},
		{{ID: "large", Status: "downloading", DownloadedBytes: 1, TotalSize: math.MaxInt64}},
		{{ID: "large", Status: "downloading", DownloadedBytes: 2, TotalSize: math.MaxInt64}},
		{{ID: "large", Status: "completed", DownloadedBytes: math.MaxInt64, TotalSize: math.MaxInt64}},
	}}
	watcher := cli.NewWatcherWithOptions(client, cli.WatchOptions{
		Now: func() time.Time { return startedAt }, Ticks: ticks,
	})
	var snapshots []cli.WatchSnapshot
	if err := watcher.Stream(context.Background(), func(snapshot cli.WatchSnapshot) error {
		snapshots = append(snapshots, snapshot)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if snapshots[2].Downloads[0].ETASeconds != nil {
		t.Fatalf("unrepresentable ETA was exposed as %v", *snapshots[2].Downloads[0].ETASeconds)
	}
	completed := snapshots[3].Downloads[0].ETASeconds
	if completed == nil || *completed != 0 {
		t.Fatalf("completed ETA is %v", completed)
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

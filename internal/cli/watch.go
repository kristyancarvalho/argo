package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/kristyancarvalho/argo/internal/ipc"
)

const DefaultWatchInterval = time.Second

type WatchDownload struct {
	ID              string   `json:"id"`
	Filename        string   `json:"filename"`
	Status          string   `json:"status"`
	Priority        string   `json:"priority"`
	DownloadedBytes int64    `json:"downloaded_bytes"`
	TotalSize       int64    `json:"total_size"`
	BytesPerSecond  float64  `json:"bytes_per_second"`
	ETASeconds      *float64 `json:"eta_seconds"`
}

type WatchSnapshot struct {
	Timestamp               time.Time       `json:"timestamp"`
	AggregateBytesPerSecond float64         `json:"aggregate_bytes_per_second"`
	Downloads               []WatchDownload `json:"downloads"`
}

type WatchOptions struct {
	Interval time.Duration
	Now      func() time.Time
	Ticks    <-chan time.Time
}

type WatchClient interface {
	List(context.Context) ([]ipc.Download, error)
}

type Watcher struct {
	client   WatchClient
	interval time.Duration
	now      func() time.Time
	ticks    <-chan time.Time
}

type watchSample struct {
	downloaded int64
	timestamp  time.Time
}

func NewWatcher(client WatchClient) *Watcher {
	return NewWatcherWithOptions(client, WatchOptions{})
}

func NewWatcherWithOptions(client WatchClient, options WatchOptions) *Watcher {
	if options.Interval <= 0 {
		options.Interval = DefaultWatchInterval
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}

	return &Watcher{
		client:   client,
		interval: options.Interval,
		now:      options.Now,
		ticks:    options.Ticks,
	}
}

func (watcher *Watcher) Stream(ctx context.Context, emit func(WatchSnapshot) error) error {
	ticks := watcher.ticks
	var ticker *time.Ticker
	if ticks == nil {
		ticker = time.NewTicker(watcher.interval)
		defer ticker.Stop()
		ticks = ticker.C
	}
	previous := make(map[string]watchSample)
	timestamp := watcher.now()
	for {
		downloads, err := watcher.client.List(ctx)
		if err != nil {
			return err
		}
		snapshot := buildWatchSnapshot(downloads, previous, timestamp)
		if err := emit(snapshot); err != nil {
			return err
		}
		if allDownloadsTerminal(downloads) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case tick, open := <-ticks:
			if !open {
				return fmt.Errorf("watch tick stream closed")
			}
			timestamp = tick.UTC()
		}
	}
}

func buildWatchSnapshot(
	downloads []ipc.Download,
	previous map[string]watchSample,
	timestamp time.Time,
) WatchSnapshot {
	snapshot := WatchSnapshot{
		Timestamp: timestamp,
		Downloads: make([]WatchDownload, 0, len(downloads)),
	}
	for _, download := range downloads {
		speed := downloadSpeed(download, previous[download.ID], timestamp)
		progress := WatchDownload{
			ID:              download.ID,
			Filename:        download.Filename,
			Status:          download.Status,
			Priority:        download.Priority,
			DownloadedBytes: download.DownloadedBytes,
			TotalSize:       download.TotalSize,
			BytesPerSecond:  speed,
			ETASeconds:      downloadETA(download, speed),
		}
		snapshot.Downloads = append(snapshot.Downloads, progress)
		snapshot.AggregateBytesPerSecond += speed
		previous[download.ID] = watchSample{
			downloaded: download.DownloadedBytes,
			timestamp:  timestamp,
		}
	}

	return snapshot
}

func downloadSpeed(download ipc.Download, previous watchSample, timestamp time.Time) float64 {
	elapsed := timestamp.Sub(previous.timestamp).Seconds()
	delta := download.DownloadedBytes - previous.downloaded
	if previous.timestamp.IsZero() || elapsed <= 0 || delta <= 0 {
		return 0
	}

	return float64(delta) / elapsed
}

func downloadETA(download ipc.Download, speed float64) *float64 {
	if download.Status == "completed" {
		eta := float64(0)
		return &eta
	}
	remaining := download.TotalSize - download.DownloadedBytes
	if download.TotalSize < 0 || remaining < 0 || speed <= 0 {
		return nil
	}
	eta := float64(remaining) / speed

	return &eta
}

func allDownloadsTerminal(downloads []ipc.Download) bool {
	if len(downloads) == 0 {
		return true
	}
	for _, download := range downloads {
		switch download.Status {
		case "completed", "failed", "canceled":
		default:
			return false
		}
	}

	return true
}

func renderWatchSnapshot(output io.Writer, snapshot WatchSnapshot) error {
	if _, err := fmt.Fprintf(
		output,
		"%s total %s/s\n",
		snapshot.Timestamp.Format(time.RFC3339),
		formatRate(snapshot.AggregateBytesPerSecond),
	); err != nil {
		return err
	}
	for _, download := range snapshot.Downloads {
		eta := "unknown"
		if download.ETASeconds != nil {
			eta = time.Duration(*download.ETASeconds * float64(time.Second)).Round(time.Second).String()
		}
		if _, err := fmt.Fprintf(
			output,
			"%s %s %d/%d bytes %s/s ETA %s %s\n",
			download.ID,
			download.Status,
			download.DownloadedBytes,
			download.TotalSize,
			formatRate(download.BytesPerSecond),
			eta,
			download.Filename,
		); err != nil {
			return err
		}
	}

	return nil
}

func formatRate(bytesPerSecond float64) string {
	return fmt.Sprintf("%.0f B", bytesPerSecond)
}

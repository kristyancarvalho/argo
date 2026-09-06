package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/kristyancarvalho/argo/internal/diagnostic"
	"github.com/kristyancarvalho/argo/internal/ipc"
)

const (
	DefaultWatchInterval = time.Second
	DefaultETAInterval   = 3 * time.Second
	etaSmoothingFactor   = 0.3
	minimumETASamples    = 2
)

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
	Interval    time.Duration
	ETAInterval time.Duration
	Now         func() time.Time
	Ticks       <-chan time.Time
}

type WatchClient interface {
	List(context.Context) ([]ipc.Download, error)
}

type Watcher struct {
	client   WatchClient
	interval time.Duration
	now      func() time.Time
	ticks    <-chan time.Time
	etaDelay time.Duration
}

type watchSample struct {
	downloaded int64
	timestamp  time.Time
	status     string
	smoothed   float64
	samples    int
	visibleETA float64
	etaVisible bool
	etaUpdated time.Time
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
	if options.ETAInterval <= 0 {
		options.ETAInterval = DefaultETAInterval
	}

	return &Watcher{
		client:   client,
		interval: options.Interval,
		now:      options.Now,
		ticks:    options.Ticks,
		etaDelay: options.ETAInterval,
	}
}

func (watcher *Watcher) Snapshot(ctx context.Context) (WatchSnapshot, error) {
	downloads, err := watcher.client.List(ctx)
	if err != nil {
		return WatchSnapshot{}, err
	}

	return buildWatchSnapshot(downloads, make(map[string]watchSample), watcher.now(), watcher.etaDelay), nil
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
		snapshot := buildWatchSnapshot(downloads, previous, timestamp, watcher.etaDelay)
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
	etaDelay time.Duration,
) WatchSnapshot {
	snapshot := WatchSnapshot{
		Timestamp: timestamp,
		Downloads: make([]WatchDownload, 0, len(downloads)),
	}
	for _, download := range downloads {
		state := previous[download.ID]
		speed, eta, state := downloadMetrics(download, state, timestamp, etaDelay)
		progress := WatchDownload{
			ID:              download.ID,
			Filename:        download.Filename,
			Status:          download.Status,
			Priority:        download.Priority,
			DownloadedBytes: download.DownloadedBytes,
			TotalSize:       download.TotalSize,
			BytesPerSecond:  speed,
			ETASeconds:      eta,
		}
		snapshot.Downloads = append(snapshot.Downloads, progress)
		snapshot.AggregateBytesPerSecond += speed
		previous[download.ID] = state
	}

	return snapshot
}

func downloadMetrics(
	download ipc.Download,
	previous watchSample,
	timestamp time.Time,
	etaDelay time.Duration,
) (float64, *float64, watchSample) {
	reset := previous.timestamp.IsZero() || !timestamp.After(previous.timestamp) ||
		download.DownloadedBytes < previous.downloaded ||
		(previous.status != "downloading" && download.Status == "downloading")
	if reset {
		previous = watchSample{}
	}
	speed := downloadSpeed(download, previous, timestamp)
	current := previous
	current.downloaded = download.DownloadedBytes
	current.timestamp = timestamp
	current.status = download.Status
	if download.Status == "completed" {
		eta := float64(0)
		current.visibleETA = eta
		current.etaVisible = true
		current.etaUpdated = timestamp

		return speed, &eta, current
	}
	if download.Status != "downloading" || speed <= 0 || download.TotalSize < 0 ||
		download.DownloadedBytes > download.TotalSize {
		current.smoothed = 0
		current.samples = 0
		current.etaVisible = false

		return speed, nil, current
	}
	if current.samples == 0 {
		current.smoothed = speed
	} else {
		current.smoothed = etaSmoothingFactor*speed + (1-etaSmoothingFactor)*current.smoothed
	}
	current.samples++
	if current.samples < minimumETASamples || current.smoothed <= 0 {
		return speed, nil, current
	}
	remaining := download.TotalSize - download.DownloadedBytes
	candidate := float64(remaining) / current.smoothed
	if !current.etaVisible || timestamp.Sub(current.etaUpdated) >= etaDelay {
		current.visibleETA = candidate
		current.etaVisible = true
		current.etaUpdated = timestamp
	}
	eta := current.visibleETA

	return speed, &eta, current
}

func downloadSpeed(download ipc.Download, previous watchSample, timestamp time.Time) float64 {
	elapsed := timestamp.Sub(previous.timestamp).Seconds()
	delta := download.DownloadedBytes - previous.downloaded
	if previous.timestamp.IsZero() || elapsed <= 0 || delta <= 0 {
		return 0
	}

	return float64(delta) / elapsed
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
			diagnostic.Display(download.ID),
			diagnostic.Display(download.Status),
			download.DownloadedBytes,
			download.TotalSize,
			formatRate(download.BytesPerSecond),
			eta,
			diagnostic.Display(download.Filename),
		); err != nil {
			return err
		}
	}

	return nil
}

func formatRate(bytesPerSecond float64) string {
	return fmt.Sprintf("%.0f B", bytesPerSecond)
}

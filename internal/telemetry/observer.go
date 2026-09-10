package telemetry

import (
	"context"
	"fmt"
	"time"

	"github.com/kristyancarvalho/argo/internal/model"
)

type DownloadSource interface {
	Downloads(context.Context) ([]model.Download, error)
}

type Observer struct {
	source    DownloadSource
	collector *Collector
	interval  time.Duration
	now       func() time.Time
}

func NewObserver(source DownloadSource, collector *Collector, interval time.Duration) (*Observer, error) {
	if source == nil || collector == nil {
		return nil, fmt.Errorf("telemetry observer requires download source and collector")
	}
	if interval <= 0 {
		return nil, fmt.Errorf("telemetry sample interval must be positive")
	}

	return &Observer{source: source, collector: collector, interval: interval, now: time.Now}, nil
}

func (observer *Observer) Observe(ctx context.Context, emit func(Snapshot) error) error {
	if emit == nil {
		return fmt.Errorf("telemetry observer callback is required")
	}
	ticker := time.NewTicker(observer.interval)
	defer ticker.Stop()
	for {
		if err := observer.sample(ctx, emit); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (observer *Observer) sample(ctx context.Context, emit func(Snapshot) error) error {
	downloads, err := observer.source.Downloads(ctx)
	if err != nil {
		return fmt.Errorf("read downloads for telemetry: %w", err)
	}
	now := observer.now().UTC()
	observations := make([]TransferObservation, 0, len(downloads))
	for _, download := range downloads {
		switch download.Status {
		case model.StatusQueued, model.StatusResolving, model.StatusDownloading, model.StatusVerifying:
			observations = append(observations, TransferObservation{
				ID:              download.ID,
				DownloadedBytes: download.DownloadedBytes,
				ObservedAt:      now,
			})
		case model.StatusPaused, model.StatusCompleted, model.StatusFailed, model.StatusCanceled:
		}
	}
	snapshot := observer.collector.Sample(ctx, observations)

	return emit(snapshot)
}

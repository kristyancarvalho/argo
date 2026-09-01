package integration_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/model"
	"github.com/kristyancarvalho/argo/internal/telemetry"
)

type telemetryDownloadSource struct {
	downloads []model.Download
}

func (source telemetryDownloadSource) Downloads(context.Context) ([]model.Download, error) {
	return source.downloads, nil
}

func TestTelemetryObserverSamplesActiveDownloadsImmediately(t *testing.T) {
	sentinel := errors.New("sample received")
	source := telemetryDownloadSource{downloads: []model.Download{{
		ID: "download", Status: model.StatusDownloading, DownloadedBytes: 100,
	}}}
	observer, err := telemetry.NewObserver(
		source,
		telemetry.NewCollector(telemetry.NewSampler(0), nil),
		time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	err = observer.Observe(context.Background(), func(snapshot telemetry.Snapshot) error {
		if snapshot.ActiveTransfers != 1 || snapshot.AggregateBytesPerSecond != 0 {
			t.Fatalf("unexpected first observer snapshot: %+v", snapshot)
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("observer returned %v", err)
	}
}

func TestTelemetryObserverValidatesDependencies(t *testing.T) {
	collector := telemetry.NewCollector(nil, nil)
	if _, err := telemetry.NewObserver(nil, collector, time.Second); err == nil {
		t.Fatal("nil source was accepted")
	}
	if _, err := telemetry.NewObserver(telemetryDownloadSource{}, nil, time.Second); err == nil {
		t.Fatal("nil collector was accepted")
	}
	if _, err := telemetry.NewObserver(telemetryDownloadSource{}, collector, 0); err == nil {
		t.Fatal("zero interval was accepted")
	}
}

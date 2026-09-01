package unit_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/model"
	"github.com/kristyancarvalho/argo/internal/telemetry"
)

func TestTelemetryAggregatesTransferSamples(t *testing.T) {
	sampler := telemetry.NewSampler(0)
	started := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	first := model.DownloadID("first")
	second := model.DownloadID("second")
	sampler.Sample([]telemetry.TransferObservation{
		{ID: first, DownloadedBytes: 10, ObservedAt: started},
		{ID: second, DownloadedBytes: 20, ObservedAt: started},
	})
	snapshot := sampler.Sample([]telemetry.TransferObservation{
		{ID: first, DownloadedBytes: 110, ObservedAt: started.Add(time.Second)},
		{ID: second, DownloadedBytes: 220, ObservedAt: started.Add(time.Second)},
	})
	if snapshot.Transfers[first].BytesPerSecond != 100 ||
		snapshot.Transfers[second].BytesPerSecond != 200 ||
		snapshot.ActiveTransfers != 2 || snapshot.AggregateBytesPerSecond != 300 {
		t.Fatalf("unexpected telemetry snapshot: %+v", snapshot)
	}
}

func TestTelemetryDropsMissingSamples(t *testing.T) {
	sampler := telemetry.NewSampler(0)
	started := time.Now()
	first := model.DownloadID("first")
	second := model.DownloadID("second")
	sampler.Sample([]telemetry.TransferObservation{
		{ID: first, ObservedAt: started},
		{ID: second, ObservedAt: started},
	})
	snapshot := sampler.Sample([]telemetry.TransferObservation{
		{ID: first, DownloadedBytes: 50, ObservedAt: started.Add(time.Second)},
	})
	if len(snapshot.Transfers) != 1 || snapshot.AggregateBytesPerSecond != 50 {
		t.Fatalf("unexpected partial telemetry: %+v", snapshot)
	}
	if snapshot := sampler.Sample(nil); len(snapshot.Transfers) != 0 || snapshot.AggregateBytesPerSecond != 0 {
		t.Fatalf("missing telemetry was retained: %+v", snapshot)
	}
	snapshot = sampler.Sample([]telemetry.TransferObservation{
		{ID: second, DownloadedBytes: 100, ObservedAt: started.Add(2 * time.Second)},
	})
	if len(snapshot.Transfers) != 0 {
		t.Fatalf("returning transfer did not require a fresh baseline: %+v", snapshot)
	}
}

func TestTelemetryRejectsOutliersAndRecovers(t *testing.T) {
	sampler := telemetry.NewSampler(100)
	identifier := model.DownloadID("download")
	started := time.Now()
	sampler.Sample([]telemetry.TransferObservation{{ID: identifier, ObservedAt: started}})
	snapshot := sampler.Sample([]telemetry.TransferObservation{{
		ID: identifier, DownloadedBytes: 1_000, ObservedAt: started.Add(time.Second),
	}})
	if len(snapshot.Transfers) != 0 || snapshot.AggregateBytesPerSecond != 0 {
		t.Fatalf("outlier was retained: %+v", snapshot)
	}
	snapshot = sampler.Sample([]telemetry.TransferObservation{{
		ID: identifier, DownloadedBytes: 1_050, ObservedAt: started.Add(2 * time.Second),
	}})
	if snapshot.Transfers[identifier].BytesPerSecond != 50 {
		t.Fatalf("sampler did not recover after outlier: %+v", snapshot)
	}
}

func TestTelemetryResetClearsTransferBaselines(t *testing.T) {
	sampler := telemetry.NewSampler(0)
	identifier := model.DownloadID("download")
	started := time.Now()
	sampler.Sample([]telemetry.TransferObservation{{ID: identifier, ObservedAt: started}})
	sampler.Reset()
	snapshot := sampler.Sample([]telemetry.TransferObservation{{
		ID: identifier, DownloadedBytes: 100, ObservedAt: started.Add(time.Second),
	}})
	if len(snapshot.Transfers) != 0 {
		t.Fatalf("reset retained a transfer baseline: %+v", snapshot)
	}
	snapshot = sampler.Sample([]telemetry.TransferObservation{{
		ID: identifier, DownloadedBytes: 120, ObservedAt: started.Add(2 * time.Second),
	}})
	if snapshot.Transfers[identifier].BytesPerSecond != 20 {
		t.Fatalf("sampler did not establish a new baseline: %+v", snapshot)
	}
}

type latencyProbe struct {
	latency time.Duration
	err     error
}

func (probe latencyProbe) Measure(context.Context) (time.Duration, error) {
	return probe.latency, probe.err
}

func TestTelemetryCollectorReportsOptionalLatency(t *testing.T) {
	available := telemetry.NewCollector(nil, latencyProbe{latency: 25 * time.Millisecond})
	snapshot := available.Sample(context.Background(), nil)
	if !snapshot.LatencyAvailable || snapshot.Latency != 25*time.Millisecond {
		t.Fatalf("latency is unavailable: %+v", snapshot)
	}
	missing := telemetry.NewCollector(nil, latencyProbe{err: errors.New("unavailable")})
	snapshot = missing.Sample(context.Background(), nil)
	if snapshot.LatencyAvailable || snapshot.Latency != 0 {
		t.Fatalf("missing latency was reported as available: %+v", snapshot)
	}
}

func TestTCPProbeConfigurationValidation(t *testing.T) {
	for _, target := range []string{"", "example.test", ":443", "example.test:"} {
		if _, err := telemetry.NewTCPProbe(target, time.Second); err == nil {
			t.Fatalf("target %q was accepted", target)
		}
	}
	if _, err := telemetry.NewTCPProbe("example.test:443", 0); err == nil {
		t.Fatal("zero timeout was accepted")
	}
}

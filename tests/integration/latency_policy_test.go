package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/daemon"
	"github.com/kristyancarvalho/argo/internal/network"
	"github.com/kristyancarvalho/argo/internal/qos"
	"github.com/kristyancarvalho/argo/internal/telemetry"
)

type controlledTelemetryObserver struct {
	updates chan controlledTelemetryUpdate
}

type controlledTelemetryUpdate struct {
	snapshot telemetry.Snapshot
	applied  chan struct{}
}

func (observer *controlledTelemetryObserver) Observe(
	ctx context.Context,
	emit func(telemetry.Snapshot) error,
) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case update := <-observer.updates:
			_ = emit(update.snapshot)
			close(update.applied)
		}
	}
}

func (observer *controlledTelemetryObserver) send(t *testing.T, snapshot telemetry.Snapshot) {
	t.Helper()
	update := controlledTelemetryUpdate{snapshot: snapshot, applied: make(chan struct{})}
	observer.updates <- update
	<-update.applied
}

func TestLatencyPolicyRespondsToCongestionAndRecovery(t *testing.T) {
	baseline, err := telemetry.NewBaselineEstimator(telemetry.BaselineOptions{Manual: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	service, engine, observer, backend := newLatencyPolicyService(t, baseline, "adaptive")
	defer closeSchedulerService(t, service)
	identifier := addScheduledDownload(t, service, "adaptive")
	assertStartedDownload(t, engine, identifier)
	response := selectTrafficPolicy(t, service, qos.PolicyLatency)
	if !response.Applied {
		t.Fatal("latency policy was not activated")
	}
	waitForTrafficPolicy(t, backend, qos.PolicyLatency, 60_000_000)

	started := time.Now().UTC()
	for index := range 2 {
		observer.send(t, adaptiveSnapshot(50*time.Millisecond, started.Add(time.Duration(index)*time.Second)))
	}
	waitForTrafficPolicy(t, backend, qos.PolicyLatency, 50_000_000)
	for index := range 2 {
		observer.send(t, adaptiveSnapshot(21*time.Millisecond, started.Add(time.Duration(index+2)*time.Second)))
	}
	waitForTrafficPolicy(t, backend, qos.PolicyLatency, 55_000_000)
	engine.release("adaptive")
}

func TestLatencyPolicyFallsBackWithoutBaseline(t *testing.T) {
	baseline, err := telemetry.NewBaselineEstimator(telemetry.BaselineOptions{
		MinimumSamples: 3,
		WindowSize:     3,
	})
	if err != nil {
		t.Fatal(err)
	}
	service, engine, observer, backend := newLatencyPolicyService(t, baseline, "missing-baseline")
	defer closeSchedulerService(t, service)
	identifier := addScheduledDownload(t, service, "missing-baseline")
	assertStartedDownload(t, engine, identifier)
	selectTrafficPolicy(t, service, qos.PolicyLatency)
	started := time.Now().UTC()
	for index := range 2 {
		observer.send(t, adaptiveSnapshot(25*time.Millisecond, started.Add(time.Duration(index)*time.Second)))
	}
	waitForTrafficPolicy(t, backend, qos.PolicyLatency, 50_000_000)
	engine.release("missing-baseline")
}

func newLatencyPolicyService(
	t *testing.T,
	baseline *telemetry.BaselineEstimator,
	release string,
) (*daemon.Service, *controlledDownloadEngine, *controlledTelemetryObserver, *trafficPolicyBackend) {
	t.Helper()
	adaptive, err := qos.NewAdaptiveController(qos.AdaptiveOptions{
		MinimumRateBitsPerSecond:  20_000_000,
		MaximumRateBitsPerSecond:  80_000_000,
		InitialRateBitsPerSecond:  60_000_000,
		IncreaseStepBitsPerSecond: 5_000_000,
		DecreaseStepBitsPerSecond: 10_000_000,
		AcceptableLatencyIncrease: 10 * time.Millisecond,
		RequiredSamples:           2,
	})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := qos.NewLatencyPolicy(baseline, adaptive)
	if err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t)
	engine := newControlledDownloadEngine(store, release)
	networkObserver := newControlledNetworkObserver()
	telemetryObserver := &controlledTelemetryObserver{updates: make(chan controlledTelemetryUpdate, 8)}
	backend := &trafficPolicyBackend{}
	service, err := daemon.NewServiceWithOptions(context.Background(), store, engine, daemon.ServiceOptions{
		MaximumConcurrentDownloads: 1,
		NetworkObserver:            networkObserver,
		TrafficLinkRate:            100_000_000,
		TrafficCgroup:              qos.CgroupSelector{Path: "argo.service", Level: 1},
		TrafficBackend:             backend,
		TelemetryObserver:          telemetryObserver,
		LatencyPolicy:              policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	networkObserver.send(t, network.Snapshot{Connected: true, Interface: "eth0"})

	return service, engine, telemetryObserver, backend
}

func adaptiveSnapshot(latency time.Duration, at time.Time) telemetry.Snapshot {
	return telemetry.Snapshot{
		ActiveTransfers:         1,
		Latency:                 latency,
		LatencyAvailable:        true,
		LatencySampledAt:        at,
		AggregateBytesPerSecond: 1,
	}
}

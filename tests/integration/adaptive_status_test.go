package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/daemon"
	"github.com/kristyancarvalho/argo/internal/ipc"
	"github.com/kristyancarvalho/argo/internal/network"
	"github.com/kristyancarvalho/argo/internal/qos"
	"github.com/kristyancarvalho/argo/internal/telemetry"
)

func TestAdaptiveStatusReportsAvailableTelemetry(t *testing.T) {
	baseline, err := telemetry.NewBaselineEstimator(telemetry.BaselineOptions{Manual: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	service, engine, observer, _ := newLatencyPolicyService(t, baseline, "status-available")
	defer closeSchedulerService(t, service)
	identifier := addScheduledDownload(t, service, "status-available")
	assertStartedDownload(t, engine, identifier)
	selectTrafficPolicy(t, service, qos.PolicyLatency)
	observer.send(t, adaptiveSnapshot(25*time.Millisecond, time.Now().UTC()))
	status := adaptiveServiceStatus(t, service)
	if status.Traffic.Policy != "latency" || !status.Traffic.Applied ||
		status.Traffic.CurrentRateBitsPerSecond != 60_000_000 ||
		!status.Traffic.LatencyAvailable || status.Traffic.MeasuredLatency != 25*time.Millisecond ||
		!status.Traffic.BaselineAvailable || status.Traffic.BaselineLatency != 20*time.Millisecond ||
		status.Traffic.ControllerState != string(qos.AdaptiveStable) {
		t.Fatalf("unexpected available adaptive status: %+v", status.Traffic)
	}
	engine.release("status-available")
}

func TestAdaptiveStatusReportsMissingTelemetry(t *testing.T) {
	baseline, err := telemetry.NewBaselineEstimator(telemetry.BaselineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service, engine, observer, _ := newLatencyPolicyService(t, baseline, "status-missing")
	defer closeSchedulerService(t, service)
	identifier := addScheduledDownload(t, service, "status-missing")
	assertStartedDownload(t, engine, identifier)
	selectTrafficPolicy(t, service, qos.PolicyLatency)
	observer.send(t, telemetry.Snapshot{})
	status := adaptiveServiceStatus(t, service)
	if status.Traffic.Policy != "latency" || status.Traffic.LatencyAvailable ||
		status.Traffic.BaselineAvailable ||
		status.Traffic.ControllerState != string(qos.AdaptiveTelemetryMissing) {
		t.Fatalf("unexpected missing adaptive status: %+v", status.Traffic)
	}
	engine.release("status-missing")
}

func TestAdaptiveStatusIsQuietWhenPolicyIsOff(t *testing.T) {
	baseline, err := telemetry.NewBaselineEstimator(telemetry.BaselineOptions{Manual: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	service, _, _, _ := newLatencyPolicyService(t, baseline, "unused")
	defer closeSchedulerService(t, service)
	status := adaptiveServiceStatus(t, service)
	if status.Traffic.Policy != "off" || status.Traffic.Applied ||
		status.Traffic.CurrentRateBitsPerSecond != 0 || status.Traffic.LatencyAvailable ||
		status.Traffic.BaselineAvailable || status.Traffic.ControllerState != "" {
		t.Fatalf("unexpected off policy status: %+v", status.Traffic)
	}
}

func TestBackgroundStatusReportsAdaptiveDiagnostics(t *testing.T) {
	baseline, err := telemetry.NewBaselineEstimator(telemetry.BaselineOptions{Manual: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	controller, err := qos.NewBackgroundController(20_000_000, 80_000_000, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	background, err := qos.NewLatencyPolicy(baseline, controller)
	if err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t)
	engine := newControlledDownloadEngine(store, "background-status")
	networkObserver := newControlledNetworkObserver()
	telemetryObserver := &controlledTelemetryObserver{updates: make(chan controlledTelemetryUpdate, 2)}
	service, err := daemon.NewServiceWithOptions(context.Background(), store, engine, daemon.ServiceOptions{
		MaximumConcurrentDownloads: 1,
		NetworkObserver:            networkObserver,
		TrafficLinkRate:            100_000_000,
		TrafficCgroup:              qos.CgroupSelector{Path: "argo.service", Level: 1},
		TrafficBackend:             &trafficPolicyBackend{},
		TelemetryObserver:          telemetryObserver,
		BackgroundPolicy:           background,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeSchedulerService(t, service)
	networkObserver.send(t, network.Snapshot{Connected: true, Interface: "eth0"})
	identifier := addScheduledDownload(t, service, "background-status")
	assertStartedDownload(t, engine, identifier)
	selectTrafficPolicy(t, service, qos.PolicyBackground)
	telemetryObserver.send(t, adaptiveSnapshot(25*time.Millisecond, time.Now().UTC()))
	status := adaptiveServiceStatus(t, service)
	if status.Traffic.Policy != "background" || !status.Traffic.Applied ||
		status.Traffic.CurrentRateBitsPerSecond != 20_000_000 || !status.Traffic.LatencyAvailable ||
		!status.Traffic.BaselineAvailable || status.Traffic.ControllerState == "" {
		t.Fatalf("unexpected background diagnostics: %+v", status.Traffic)
	}
	engine.release("background-status")
}

func adaptiveServiceStatus(t *testing.T, service ipc.Handler) ipc.Status {
	t.Helper()
	result, err := service.Handle(context.Background(), ipc.Request{Operation: ipc.OperationStatus})
	if err != nil {
		t.Fatal(err)
	}
	status, valid := result.(ipc.Status)
	if !valid {
		t.Fatalf("status response has type %T", result)
	}

	return status
}

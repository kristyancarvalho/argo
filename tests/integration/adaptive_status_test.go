package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/ipc"
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

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/daemon"
	"github.com/kristyancarvalho/argo/internal/ipc"
	"github.com/kristyancarvalho/argo/internal/model"
	"github.com/kristyancarvalho/argo/internal/network"
	"github.com/kristyancarvalho/argo/internal/qos"
	"github.com/kristyancarvalho/argo/internal/telemetry"
)

func TestProfileSwitchAppliesAndPersistsSettings(t *testing.T) {
	store := openTestStore(t)
	engine := newControlledDownloadEngine(store, "blocker", "waiting", "configured")
	service := newProfileService(t, store, engine, nil)
	defer closeSchedulerService(t, service)

	blocker := addScheduledDownload(t, service, "blocker")
	assertStartedDownload(t, engine, blocker)
	waiting := addScheduledDownload(t, service, "waiting")
	assertNoStartedDownload(t, engine)
	response := switchProfile(t, service, "work")
	if response.Name != "work" || response.BytesPerSecond != 60_000_000 ||
		response.DefaultPriority != string(model.PriorityLow) ||
		response.MaxConcurrentDownloads != 2 || response.PauseOnMetered {
		t.Fatalf("unexpected profile response: %+v", response)
	}
	assertStartedDownload(t, engine, waiting)
	if engine.currentRateLimit() != 60_000_000 {
		t.Fatalf("rate limit is %d", engine.currentRateLimit())
	}
	configured := addScheduledDownload(t, service, "configured")
	download, err := store.Download(context.Background(), configured)
	if err != nil {
		t.Fatal(err)
	}
	if download.Priority != model.PriorityLow {
		t.Fatalf("configured priority is %s", download.Priority)
	}
	active, err := store.ActiveProfile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if active != "work" {
		t.Fatalf("active profile is %q", active)
	}
	engine.release("blocker")
	engine.release("waiting")
	assertStartedDownload(t, engine, configured)
	engine.release("configured")
}

func TestAdaptiveProfileSwitchReconcilesActiveDownload(t *testing.T) {
	store := openTestStore(t)
	engine := newControlledDownloadEngine(store, "adaptive-profile")
	observer := newControlledNetworkObserver()
	backend := &trafficPolicyBackend{}
	low := newProfileLatencyPolicy(t, 10_000_000, 40_000_000)
	high := newProfileLatencyPolicy(t, 20_000_000, 70_000_000)
	service, err := daemon.NewServiceWithOptions(context.Background(), store, engine, daemon.ServiceOptions{
		MaximumConcurrentDownloads: 1,
		NetworkObserver:            observer,
		TrafficLinkRate:            100_000_000,
		TrafficCgroupID:            42,
		TrafficBackend:             backend,
		Profiles: map[string]daemon.Profile{
			"responsive": {
				Name: "responsive", BytesPerSecond: 1, DefaultPriority: model.PriorityNormal,
				MaximumConcurrentDownloads: 1, Policy: "latency", LatencyPolicy: low,
			},
			"fast": {
				Name: "fast", BytesPerSecond: 1, DefaultPriority: model.PriorityNormal,
				MaximumConcurrentDownloads: 1, Policy: "latency", LatencyPolicy: high,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeSchedulerService(t, service)
	observer.send(t, network.Snapshot{Connected: true, Interface: "eth0"})
	identifier := addScheduledDownload(t, service, "adaptive-profile")
	assertStartedDownload(t, engine, identifier)
	switchProfile(t, service, "responsive")
	waitForTrafficPolicy(t, backend, qos.PolicyLatency, 40_000_000)
	switchProfile(t, service, "fast")
	waitForTrafficPolicy(t, backend, qos.PolicyLatency, 70_000_000)
	engine.release("adaptive-profile")
}

func newProfileLatencyPolicy(t *testing.T, minimum, maximum uint64) *qos.LatencyPolicy {
	t.Helper()
	baseline, err := telemetry.NewBaselineEstimator(telemetry.BaselineOptions{Manual: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	controller, err := qos.NewAdaptiveController(qos.AdaptiveOptions{
		MinimumRateBitsPerSecond: minimum, MaximumRateBitsPerSecond: maximum,
		InitialRateBitsPerSecond: maximum, IncreaseStepBitsPerSecond: 1,
		DecreaseStepBitsPerSecond: 1, AcceptableLatencyIncrease: 10 * time.Millisecond,
		RequiredSamples: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := qos.NewLatencyPolicy(baseline, controller)
	if err != nil {
		t.Fatal(err)
	}

	return policy
}

func TestUnknownProfileIsRejected(t *testing.T) {
	store := openTestStore(t)
	engine := newControlledDownloadEngine(store)
	service := newProfileService(t, store, engine, nil)
	defer closeSchedulerService(t, service)

	payload, err := json.Marshal(ipc.ProfileRequest{Name: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Handle(context.Background(), ipc.Request{
		Operation: ipc.OperationProfile,
		Payload:   payload,
	})
	var unknown daemon.UnknownProfileError
	if !errors.As(err, &unknown) || unknown.Name != "missing" {
		t.Fatalf("unknown profile returned %v", err)
	}
}

func TestPersistedProfileIsRestoredAtStartup(t *testing.T) {
	store := openTestStore(t)
	if err := store.SetActiveProfile(context.Background(), "gaming"); err != nil {
		t.Fatal(err)
	}
	engine := newControlledDownloadEngine(store, "restored")
	service := newProfileService(t, store, engine, nil)
	defer closeSchedulerService(t, service)
	if engine.currentRateLimit() != 30_000_000 {
		t.Fatalf("restored rate limit is %d", engine.currentRateLimit())
	}
	identifier := addScheduledDownload(t, service, "restored")
	assertStartedDownload(t, engine, identifier)
	download, err := store.Download(context.Background(), identifier)
	if err != nil {
		t.Fatal(err)
	}
	if download.Priority != model.PriorityHigh {
		t.Fatalf("restored default priority is %s", download.Priority)
	}
	engine.release("restored")
}

func TestProfileSelectsMeteredBehavior(t *testing.T) {
	store := openTestStore(t)
	engine := newControlledDownloadEngine(store, "metered-profile")
	observer := newControlledNetworkObserver()
	service := newProfileService(t, store, engine, observer)
	defer closeSchedulerService(t, service)
	switchProfile(t, service, "gaming")
	identifier := addScheduledDownload(t, service, "metered-profile")
	assertStartedDownload(t, engine, identifier)
	observer.send(t, network.Snapshot{Metered: network.MeteredYes})
	waitForDownload(t, store, identifier, func(download model.Download) bool {
		return download.Status == model.StatusPaused
	})
}

func newProfileService(
	t *testing.T,
	store daemon.Store,
	engine *controlledDownloadEngine,
	observer daemon.NetworkObserver,
) *daemon.Service {
	t.Helper()
	service, err := daemon.NewServiceWithOptions(context.Background(), store, engine, daemon.ServiceOptions{
		MaximumConcurrentDownloads: 1,
		NetworkObserver:            observer,
		Profiles: map[string]daemon.Profile{
			"gaming": {
				Name:                       "gaming",
				BytesPerSecond:             30_000_000,
				DefaultPriority:            model.PriorityHigh,
				MaximumConcurrentDownloads: 1,
				PauseOnMetered:             true,
				ResumeAfterMetered:         true,
				Policy:                     "latency",
			},
			"work": {
				Name:                       "work",
				BytesPerSecond:             60_000_000,
				DefaultPriority:            model.PriorityLow,
				MaximumConcurrentDownloads: 2,
				Policy:                     "balanced",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	return service
}

func switchProfile(t *testing.T, service *daemon.Service, name string) ipc.ProfileResponse {
	t.Helper()
	payload, err := json.Marshal(ipc.ProfileRequest{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Handle(context.Background(), ipc.Request{
		Operation: ipc.OperationProfile,
		Payload:   payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	response, valid := result.(ipc.ProfileResponse)
	if !valid {
		t.Fatalf("profile response has type %T", result)
	}

	return response
}

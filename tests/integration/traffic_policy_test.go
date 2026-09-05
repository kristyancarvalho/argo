package integration_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/daemon"
	"github.com/kristyancarvalho/argo/internal/ipc"
	"github.com/kristyancarvalho/argo/internal/model"
	"github.com/kristyancarvalho/argo/internal/network"
	"github.com/kristyancarvalho/argo/internal/qos"
)

type trafficPolicyBackend struct {
	mutex   sync.Mutex
	applied []qos.DesiredState
	removed []string
}

func (backend *trafficPolicyBackend) Apply(_ context.Context, state qos.DesiredState) error {
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	backend.applied = append(backend.applied, state)

	return nil
}

func (backend *trafficPolicyBackend) Remove(_ context.Context, interfaceName string) error {
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	backend.removed = append(backend.removed, interfaceName)

	return nil
}

func (backend *trafficPolicyBackend) snapshot() ([]qos.DesiredState, []string) {
	backend.mutex.Lock()
	defer backend.mutex.Unlock()

	return append([]qos.DesiredState(nil), backend.applied...), append([]string(nil), backend.removed...)
}

func TestTrafficPoliciesSwitchIndependentlyFromDownloadPriority(t *testing.T) {
	store := openTestStore(t)
	engine := newControlledDownloadEngine(store, "traffic-policy")
	observer := newControlledNetworkObserver()
	backend := &trafficPolicyBackend{}
	service, err := daemon.NewServiceWithOptions(context.Background(), store, engine, daemon.ServiceOptions{
		MaximumConcurrentDownloads: 1,
		NetworkObserver:            observer,
		TrafficPolicy:              qos.PolicyOff,
		TrafficLinkRate:            100_000_000,
		TrafficCgroup:              qos.CgroupSelector{Path: "argo.service", Level: 1},
		TrafficBackend:             backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeSchedulerService(t, service)
	observer.send(t, network.Snapshot{Connected: true, Interface: "eth0"})

	response := selectTrafficPolicy(t, service, qos.PolicyBalanced)
	if response.Applied {
		t.Fatal("policy was applied without an active download")
	}
	identifier := addScheduledDownload(t, service, "traffic-policy")
	assertStartedDownload(t, engine, identifier)
	waitForTrafficPolicy(t, backend, qos.PolicyBalanced, 50_000_000)

	response = selectTrafficPolicy(t, service, qos.PolicyThroughput)
	if !response.Applied {
		t.Fatal("throughput policy was not applied")
	}
	waitForTrafficPolicy(t, backend, qos.PolicyThroughput, 80_000_000)
	download, err := store.Download(context.Background(), identifier)
	if err != nil {
		t.Fatal(err)
	}
	if download.Priority != model.PriorityNormal {
		t.Fatalf("traffic policy changed download priority to %s", download.Priority)
	}

	response = selectTrafficPolicy(t, service, qos.PolicyOff)
	if response.Applied {
		t.Fatal("off policy remained applied")
	}
	applied, removed := backend.snapshot()
	if len(applied) < 2 || len(removed) != 1 || removed[0] != "eth0" {
		t.Fatalf("unexpected backend operations: applied %+v, removed %+v", applied, removed)
	}
	engine.release("traffic-policy")
}

func waitForTrafficPolicy(
	t *testing.T,
	backend *trafficPolicyBackend,
	policy qos.Policy,
	rate uint64,
) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		applied, _ := backend.snapshot()
		if len(applied) > 0 {
			last := applied[len(applied)-1]
			if last.Policy == policy && last.ArgoRateBitsPerSecond == rate {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	applied, _ := backend.snapshot()
	t.Fatalf("policy %s at rate %d was not applied: %+v", policy, rate, applied)
}

func selectTrafficPolicy(t *testing.T, service *daemon.Service, policy qos.Policy) ipc.PolicyResponse {
	t.Helper()
	payload, err := json.Marshal(ipc.PolicyRequest{Policy: string(policy)})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Handle(context.Background(), ipc.Request{
		Operation: ipc.OperationPolicy,
		Payload:   payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	response, valid := result.(ipc.PolicyResponse)
	if !valid {
		t.Fatalf("policy response has type %T", result)
	}

	return response
}

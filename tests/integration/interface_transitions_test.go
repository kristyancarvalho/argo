package integration_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/kristyancarvalho/argo/internal/daemon"
	"github.com/kristyancarvalho/argo/internal/model"
	"github.com/kristyancarvalho/argo/internal/network"
	"github.com/kristyancarvalho/argo/internal/qos"
)

func TestQoSFollowsInterfaceTransitionsAndDisconnects(t *testing.T) {
	store := openTestStore(t)
	engine := newControlledDownloadEngine(store, "transition")
	observer := newControlledNetworkObserver()
	backend := &trafficPolicyBackend{}
	service, err := daemon.NewServiceWithOptions(context.Background(), store, engine, daemon.ServiceOptions{
		MaximumConcurrentDownloads: 1,
		NetworkObserver:            observer,
		TrafficPolicy:              qos.PolicyBalanced,
		TrafficLinkRate:            100_000_000,
		TrafficCgroupID:            42,
		TrafficBackend:             backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeSchedulerService(t, service)
	identifier := addScheduledDownload(t, service, "transition")
	assertStartedDownload(t, engine, identifier)

	observer.send(t, network.Snapshot{Connected: true, Interface: "eth0"})
	waitForTrafficPolicy(t, backend, qos.PolicyBalanced, 50_000_000)
	observer.send(t, network.Snapshot{Connected: true, Interface: "wlan0"})
	applied, removed := backend.snapshot()
	if len(applied) != 2 || applied[1].Interface != "wlan0" ||
		len(removed) != 1 || removed[0] != "eth0" {
		t.Fatalf("unexpected Ethernet to Wi-Fi operations: applied %+v, removed %+v", applied, removed)
	}

	observer.send(t, network.Snapshot{})
	observer.send(t, network.Snapshot{})
	applied, removed = backend.snapshot()
	if len(removed) != 2 || removed[1] != "wlan0" {
		t.Fatalf("disconnect left unexpected state: applied %+v, removed %+v", applied, removed)
	}
	observer.send(t, network.Snapshot{Connected: true, Interface: "wlan0"})
	applied, removed = backend.snapshot()
	if len(applied) != 3 || applied[2].Interface != "wlan0" || len(removed) != 2 {
		t.Fatalf("reconnect operations are unexpected: applied %+v, removed %+v", applied, removed)
	}
	download, err := store.Download(context.Background(), identifier)
	if err != nil {
		t.Fatal(err)
	}
	if download.Status != model.StatusDownloading {
		t.Fatalf("interface transitions changed download status to %s", download.Status)
	}
	engine.release("transition")
}

type unavailableTrafficBackend struct {
	mutex    sync.Mutex
	attempts int
}

func (backend *unavailableTrafficBackend) Apply(context.Context, qos.DesiredState) error {
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	backend.attempts++

	return errors.New("helper unavailable")
}

func (backend *unavailableTrafficBackend) Remove(context.Context, string) error {
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	backend.attempts++

	return errors.New("helper unavailable")
}

func (backend *unavailableTrafficBackend) count() int {
	backend.mutex.Lock()
	defer backend.mutex.Unlock()

	return backend.attempts
}

func TestUnavailableQoSHelperDoesNotStopNetworkObservationOrDownloads(t *testing.T) {
	store := openTestStore(t)
	engine := newControlledDownloadEngine(store, "unavailable")
	observer := newControlledNetworkObserver()
	backend := &unavailableTrafficBackend{}
	service, err := daemon.NewServiceWithOptions(context.Background(), store, engine, daemon.ServiceOptions{
		MaximumConcurrentDownloads: 1,
		NetworkObserver:            observer,
		TrafficPolicy:              qos.PolicyFocus,
		TrafficLinkRate:            100_000_000,
		TrafficCgroupID:            42,
		TrafficBackend:             backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeSchedulerService(t, service)
	identifier := addScheduledDownload(t, service, "unavailable")
	assertStartedDownload(t, engine, identifier)

	snapshot := network.Snapshot{Connected: true, Interface: "eth0"}
	observer.send(t, snapshot)
	observer.send(t, snapshot)
	if backend.count() != 1 {
		t.Fatalf("unchanged network state caused %d helper attempts", backend.count())
	}
	status := adaptiveServiceStatus(t, service)
	if !strings.Contains(status.Traffic.Error, "helper unavailable") {
		t.Fatalf("helper failure is missing from status: %+v", status.Traffic)
	}
	observer.send(t, network.Snapshot{})
	download, err := store.Download(context.Background(), identifier)
	if err != nil {
		t.Fatal(err)
	}
	if download.Status != model.StatusDownloading {
		t.Fatalf("helper failure changed download status to %s", download.Status)
	}
	engine.release("unavailable")
}

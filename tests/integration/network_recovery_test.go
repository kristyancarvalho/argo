package integration_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/daemon"
	"github.com/kristyancarvalho/argo/internal/model"
	"github.com/kristyancarvalho/argo/internal/network"
	"github.com/kristyancarvalho/argo/internal/qos"
)

type recoveringNetworkObserver struct {
	mutex          sync.Mutex
	attempts       int
	reconnections  int
	initialApplied chan struct{}
	fail           chan struct{}
	retryStarted   chan struct{}
	recover        chan struct{}
}

type failingNetworkObserver struct {
	mutex    sync.Mutex
	attempts int
}

func TestNetworkObserverInvalidatesAndRecoversWithoutDaemonRestart(t *testing.T) {
	store := openTestStore(t)
	engine := newControlledDownloadEngine(store, "network-recovery")
	observer := newRecoveringNetworkObserver()
	backend := &trafficPolicyBackend{}
	service, err := daemon.NewServiceWithOptions(context.Background(), store, engine, daemon.ServiceOptions{
		MaximumConcurrentDownloads: 1,
		NetworkObserver:            observer,
		TrafficPolicy:              qos.PolicyBalanced,
		TrafficLinkRate:            100_000_000,
		TrafficCgroup:              qos.CgroupSelector{Path: "argo.service", Level: 1},
		TrafficBackend:             backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeSchedulerService(t, service)
	awaitSignal(t, observer.initialApplied)
	identifier := addScheduledDownload(t, service, "network-recovery")
	assertStartedDownload(t, engine, identifier)
	waitForTrafficPolicy(t, backend, qos.PolicyBalanced, 50_000_000)

	close(observer.fail)
	waitForNetworkStatus(t, service, func(statusNetwork networkStatusView) bool {
		return !statusNetwork.available && strings.Contains(statusNetwork.errorMessage, "temporary D-Bus failure")
	})
	waitForTrafficPolicyRemoval(t, backend, "eth0")
	awaitSignal(t, observer.retryStarted)
	if observer.reconnectCount() != 1 {
		t.Fatalf("network observer reconnected %d times", observer.reconnectCount())
	}
	close(observer.recover)
	waitForNetworkStatus(t, service, func(statusNetwork networkStatusView) bool {
		return statusNetwork.available && statusNetwork.interfaceName == "wlan0" && statusNetwork.errorMessage == ""
	})
	waitForTrafficPolicyInterface(t, backend, "wlan0")
	engine.release("network-recovery")
	waitForDownload(t, store, identifier, func(download model.Download) bool {
		return download.Status == model.StatusCompleted
	})
}

func TestNetworkObserverRetriesWithBoundedBackoff(t *testing.T) {
	store := openTestStore(t)
	observer := &failingNetworkObserver{}
	service, err := daemon.NewServiceWithOptions(
		context.Background(),
		store,
		newControlledDownloadEngine(store),
		daemon.ServiceOptions{NetworkObserver: observer},
	)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(350 * time.Millisecond)
	status := readServiceStatus(t, service)
	if status.Network.Available || !strings.Contains(status.Network.Error, "NetworkManager unavailable") {
		t.Fatalf("unexpected failed observer status: %+v", status.Network)
	}
	attempts := observer.count()
	if attempts < 2 || attempts > 3 {
		t.Fatalf("observer made %d attempts in 350ms", attempts)
	}
	closeSchedulerService(t, service)
}

func (observer *recoveringNetworkObserver) Observe(
	ctx context.Context,
	emit func(network.Snapshot) error,
) error {
	observer.mutex.Lock()
	observer.attempts++
	attempt := observer.attempts
	observer.mutex.Unlock()
	if attempt == 1 {
		if err := emit(network.Snapshot{Connected: true, Interface: "eth0", Metered: network.MeteredNo}); err != nil {
			return err
		}
		close(observer.initialApplied)
		select {
		case <-ctx.Done():
			return nil
		case <-observer.fail:
			return errors.New("temporary D-Bus failure")
		}
	}
	if attempt == 2 {
		close(observer.retryStarted)
		select {
		case <-ctx.Done():
			return nil
		case <-observer.recover:
		}
		if err := emit(network.Snapshot{Connected: true, Interface: "wlan0", Metered: network.MeteredNo}); err != nil {
			return err
		}
		<-ctx.Done()
		return nil
	}
	<-ctx.Done()

	return nil
}

func (observer *failingNetworkObserver) Observe(context.Context, func(network.Snapshot) error) error {
	observer.mutex.Lock()
	defer observer.mutex.Unlock()
	observer.attempts++

	return errors.New("NetworkManager unavailable")
}

func (observer *failingNetworkObserver) count() int {
	observer.mutex.Lock()
	defer observer.mutex.Unlock()

	return observer.attempts
}

func (observer *recoveringNetworkObserver) Reconnect(context.Context) error {
	observer.mutex.Lock()
	defer observer.mutex.Unlock()
	observer.reconnections++

	return nil
}

func (observer *recoveringNetworkObserver) reconnectCount() int {
	observer.mutex.Lock()
	defer observer.mutex.Unlock()

	return observer.reconnections
}

func newRecoveringNetworkObserver() *recoveringNetworkObserver {
	return &recoveringNetworkObserver{
		initialApplied: make(chan struct{}),
		fail:           make(chan struct{}),
		retryStarted:   make(chan struct{}),
		recover:        make(chan struct{}),
	}
}

type networkStatusView struct {
	available     bool
	interfaceName string
	errorMessage  string
}

func waitForNetworkStatus(
	t *testing.T,
	service *daemon.Service,
	condition func(networkStatusView) bool,
) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := readServiceStatus(t, service)
		view := networkStatusView{
			available: status.Network.Available, interfaceName: status.Network.Interface,
			errorMessage: status.Network.Error,
		}
		if condition(view) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for network status")
}

func waitForTrafficPolicyInterface(t *testing.T, backend *trafficPolicyBackend, interfaceName string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		applied, _ := backend.snapshot()
		if len(applied) > 0 && applied[len(applied)-1].Interface == interfaceName {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("traffic policy was not applied to %s", interfaceName)
}

func waitForTrafficPolicyRemoval(t *testing.T, backend *trafficPolicyBackend, interfaceName string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, removed := backend.snapshot()
		if len(removed) > 0 && removed[len(removed)-1] == interfaceName {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("traffic policy was not removed from %s", interfaceName)
}

func awaitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for observer signal")
	}
}

package unit_test

import (
	"context"
	"sync"
	"testing"
	"time"

	argonetwork "github.com/kristyancarvalho/argo/internal/network"
)

const (
	testActivePath = "/org/freedesktop/NetworkManager/ActiveConnection/1"
	testDevicePath = "/org/freedesktop/NetworkManager/Devices/1"
)

type observableNetworkProperties struct {
	mutex         sync.Mutex
	connected     bool
	connectivity  uint32
	connection    string
	interfaceName string
}

type fakeNetworkSubscription struct {
	events chan argonetwork.Event
	closed chan struct{}
	once   sync.Once
}

type fakeNetworkSubscriber struct {
	subscription *fakeNetworkSubscription
}

func TestNetworkObserverDetectsEthernetToWiFiAndMultipleEvents(t *testing.T) {
	properties := &observableNetworkProperties{}
	properties.set(true, 4, "Wired", "eth0")
	subscription := newFakeNetworkSubscription()
	client := argonetwork.NewObservableClient(
		properties,
		&fakeNetworkSubscriber{subscription: subscription},
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates, finished := startNetworkObserver(client, ctx)
	assertNetworkUpdate(t, updates, true, "Wired", "eth0", argonetwork.ConnectivityFull)

	properties.set(true, 4, "Wi-Fi", "wlan0")
	subscription.events <- argonetwork.Event{}
	assertNetworkUpdate(t, updates, true, "Wi-Fi", "wlan0", argonetwork.ConnectivityFull)
	properties.set(true, 3, "Wi-Fi", "wlan0")
	subscription.events <- argonetwork.Event{}
	assertNetworkUpdate(t, updates, true, "Wi-Fi", "wlan0", argonetwork.ConnectivityLimited)
	subscription.events <- argonetwork.Event{}
	assertNoNetworkUpdate(t, updates)
	cancel()
	assertObserverStopped(t, finished)
}

func TestNetworkObserverDetectsDisconnectAndReconnect(t *testing.T) {
	properties := &observableNetworkProperties{}
	properties.set(true, 4, "Home", "wlan0")
	subscription := newFakeNetworkSubscription()
	client := argonetwork.NewObservableClient(
		properties,
		&fakeNetworkSubscriber{subscription: subscription},
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates, finished := startNetworkObserver(client, ctx)
	assertNetworkUpdate(t, updates, true, "Home", "wlan0", argonetwork.ConnectivityFull)

	properties.set(false, 1, "", "")
	subscription.events <- argonetwork.Event{}
	assertNetworkUpdate(t, updates, false, "", "", argonetwork.ConnectivityNone)
	properties.set(true, 4, "Office", "eth0")
	subscription.events <- argonetwork.Event{}
	assertNetworkUpdate(t, updates, true, "Office", "eth0", argonetwork.ConnectivityFull)
	cancel()
	assertObserverStopped(t, finished)
}

func TestNetworkObserverClosesSubscriptionOnDaemonShutdown(t *testing.T) {
	properties := &observableNetworkProperties{}
	properties.set(false, 1, "", "")
	subscription := newFakeNetworkSubscription()
	client := argonetwork.NewObservableClient(
		properties,
		&fakeNetworkSubscriber{subscription: subscription},
	)
	ctx, cancel := context.WithCancel(context.Background())
	updates, finished := startNetworkObserver(client, ctx)
	assertNetworkUpdate(t, updates, false, "", "", argonetwork.ConnectivityNone)
	cancel()
	assertObserverStopped(t, finished)
	select {
	case <-subscription.closed:
	case <-time.After(time.Second):
		t.Fatal("network subscription was not closed")
	}
}

func (properties *observableNetworkProperties) ReadProperty(
	_ context.Context,
	_ string,
	path string,
	_ string,
	property string,
) (any, error) {
	properties.mutex.Lock()
	defer properties.mutex.Unlock()
	switch {
	case path == argonetwork.NetworkManagerPath && property == "State":
		if properties.connected {
			return uint32(70), nil
		}
		return uint32(20), nil
	case path == argonetwork.NetworkManagerPath && property == "Connectivity":
		return properties.connectivity, nil
	case path == argonetwork.NetworkManagerPath && property == "PrimaryConnection":
		if properties.connected {
			return testActivePath, nil
		}
		return "/", nil
	case path == testActivePath && property == "Id":
		return properties.connection, nil
	case path == testActivePath && property == "Devices":
		return []string{testDevicePath}, nil
	case path == testDevicePath && property == "Interface":
		return properties.interfaceName, nil
	default:
		return nil, context.Canceled
	}
}

func (properties *observableNetworkProperties) set(
	connected bool,
	connectivity uint32,
	connection string,
	interfaceName string,
) {
	properties.mutex.Lock()
	defer properties.mutex.Unlock()
	properties.connected = connected
	properties.connectivity = connectivity
	properties.connection = connection
	properties.interfaceName = interfaceName
}

func newFakeNetworkSubscription() *fakeNetworkSubscription {
	return &fakeNetworkSubscription{
		events: make(chan argonetwork.Event, 8),
		closed: make(chan struct{}),
	}
}

func (subscription *fakeNetworkSubscription) Events() <-chan argonetwork.Event {
	return subscription.events
}

func (subscription *fakeNetworkSubscription) Close() error {
	subscription.once.Do(func() {
		close(subscription.closed)
	})

	return nil
}

func (subscriber *fakeNetworkSubscriber) Subscribe(context.Context) (argonetwork.Subscription, error) {
	return subscriber.subscription, nil
}

func startNetworkObserver(
	client *argonetwork.Client,
	ctx context.Context,
) (<-chan argonetwork.Snapshot, <-chan error) {
	updates := make(chan argonetwork.Snapshot, 8)
	finished := make(chan error, 1)
	go func() {
		finished <- client.Observe(ctx, func(snapshot argonetwork.Snapshot) error {
			updates <- snapshot
			return nil
		})
	}()

	return updates, finished
}

func assertNetworkUpdate(
	t *testing.T,
	updates <-chan argonetwork.Snapshot,
	connected bool,
	connection string,
	interfaceName string,
	connectivity argonetwork.Connectivity,
) {
	t.Helper()
	select {
	case snapshot := <-updates:
		if snapshot.Connected != connected ||
			snapshot.ActiveConnection != connection ||
			snapshot.Interface != interfaceName ||
			snapshot.Connectivity != connectivity {
			t.Fatalf("unexpected network update: %+v", snapshot)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for network update")
	}
}

func assertNoNetworkUpdate(t *testing.T, updates <-chan argonetwork.Snapshot) {
	t.Helper()
	select {
	case snapshot := <-updates:
		t.Fatalf("unexpected duplicate network update: %+v", snapshot)
	case <-time.After(50 * time.Millisecond):
	}
}

func assertObserverStopped(t *testing.T, finished <-chan error) {
	t.Helper()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("network observer did not stop")
	}
}

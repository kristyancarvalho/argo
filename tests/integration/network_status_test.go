package integration_test

import (
	"context"
	"testing"

	"github.com/kristyancarvalho/argo/internal/daemon"
	"github.com/kristyancarvalho/argo/internal/ipc"
	"github.com/kristyancarvalho/argo/internal/network"
)

func TestStatusExposesConnectedNetworkAndActiveProfile(t *testing.T) {
	store := openTestStore(t)
	engine := newControlledDownloadEngine(store)
	observer := newControlledNetworkObserver()
	service := newProfileService(t, store, engine, observer)
	defer closeSchedulerService(t, service)
	switchProfile(t, service, "gaming")
	observer.send(t, network.Snapshot{
		Connected:        true,
		State:            network.ConnectionGlobal,
		Connectivity:     network.ConnectivityFull,
		Metered:          network.MeteredNo,
		ActiveConnection: "Office Wi-Fi",
		ConnectionType:   "802-11-wireless",
		Interface:        "wlan0",
	})

	status := readServiceStatus(t, service)
	if !status.Network.Available ||
		!status.Network.Connected ||
		status.Network.State != string(network.ConnectionGlobal) ||
		status.Network.Connectivity != string(network.ConnectivityFull) ||
		status.Network.Metered != string(network.MeteredNo) ||
		status.Network.ActiveConnection != "Office Wi-Fi" ||
		status.Network.ConnectionType != "802-11-wireless" ||
		status.Network.Interface != "wlan0" ||
		status.ActiveProfile != "gaming" {
		t.Fatalf("unexpected connected status: %+v", status)
	}
}

func TestStatusExposesDisconnectedNetwork(t *testing.T) {
	store := openTestStore(t)
	engine := newControlledDownloadEngine(store)
	observer := newControlledNetworkObserver()
	service, err := daemon.NewServiceWithOptions(context.Background(), store, engine, daemon.ServiceOptions{
		NetworkObserver: observer,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeSchedulerService(t, service)
	observer.send(t, network.Snapshot{
		State:        network.ConnectionDisconnected,
		Connectivity: network.ConnectivityNone,
		Metered:      network.MeteredUnknown,
	})

	status := readServiceStatus(t, service)
	if !status.Network.Available ||
		status.Network.Connected ||
		status.Network.State != string(network.ConnectionDisconnected) ||
		status.Network.Connectivity != string(network.ConnectivityNone) ||
		status.Network.Metered != string(network.MeteredUnknown) ||
		status.Network.Interface != "" ||
		status.Network.ConnectionType != "" ||
		status.ActiveProfile != "" {
		t.Fatalf("unexpected disconnected status: %+v", status)
	}
}

func TestStatusPreservesMissingOptionalNetworkData(t *testing.T) {
	store := openTestStore(t)
	engine := newControlledDownloadEngine(store)
	observer := newControlledNetworkObserver()
	service, err := daemon.NewServiceWithOptions(context.Background(), store, engine, daemon.ServiceOptions{
		NetworkObserver: observer,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeSchedulerService(t, service)
	observer.send(t, network.Snapshot{
		Connected:    true,
		State:        network.ConnectionGlobal,
		Connectivity: network.ConnectivityFull,
		Metered:      network.MeteredUnknown,
	})

	status := readServiceStatus(t, service)
	if !status.Network.Available || !status.Network.Connected ||
		status.Network.Interface != "" ||
		status.Network.ConnectionType != "" ||
		status.Network.ActiveConnection != "" ||
		status.Network.Metered != string(network.MeteredUnknown) {
		t.Fatalf("unexpected status with optional data missing: %+v", status)
	}
}

func TestStatusReportsUnavailableNetworkObserver(t *testing.T) {
	store := openTestStore(t)
	engine := newControlledDownloadEngine(store)
	service, err := daemon.NewService(context.Background(), store, engine)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSchedulerService(t, service)
	status := readServiceStatus(t, service)
	if status.Network.Available {
		t.Fatalf("network unexpectedly available: %+v", status.Network)
	}
}

func readServiceStatus(t *testing.T, service *daemon.Service) ipc.Status {
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

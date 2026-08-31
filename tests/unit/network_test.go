package unit_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	argonetwork "github.com/kristyancarvalho/argo/internal/network"
)

type propertyKey struct {
	destination   string
	path          string
	interfaceName string
	property      string
}

type networkPropertyReader struct {
	values map[propertyKey]any
	errors map[propertyKey]error
	calls  []propertyKey
}

func (reader *networkPropertyReader) ReadProperty(
	_ context.Context,
	destination string,
	path string,
	interfaceName string,
	property string,
) (any, error) {
	key := propertyKey{
		destination:   destination,
		path:          path,
		interfaceName: interfaceName,
		property:      property,
	}
	reader.calls = append(reader.calls, key)
	if err := reader.errors[key]; err != nil {
		return nil, err
	}
	value, exists := reader.values[key]
	if !exists {
		return nil, fmt.Errorf("unexpected property request: %+v", key)
	}

	return value, nil
}

func TestNetworkManagerDisconnectedState(t *testing.T) {
	reader := managerPropertyReader(uint32(20), uint32(1), "/")
	snapshot, err := argonetwork.NewClient(reader).ReadState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Connected ||
		snapshot.State != argonetwork.ConnectionDisconnected ||
		snapshot.Connectivity != argonetwork.ConnectivityNone ||
		snapshot.ActiveConnection != "" ||
		snapshot.Interface != "" {
		t.Fatalf("unexpected disconnected state: %+v", snapshot)
	}
	if len(reader.calls) != 3 {
		t.Fatalf("disconnected state made %d property calls, expected 3", len(reader.calls))
	}
}

func TestNetworkManagerActiveConnectionAndInterfaceResolution(t *testing.T) {
	activePath := "/org/freedesktop/NetworkManager/ActiveConnection/7"
	firstDevice := "/org/freedesktop/NetworkManager/Devices/2"
	secondDevice := "/org/freedesktop/NetworkManager/Devices/3"
	reader := managerPropertyReader(uint32(70), uint32(4), activePath)
	reader.values[propertyKey{
		destination:   argonetwork.NetworkManagerDestination,
		path:          activePath,
		interfaceName: argonetwork.ActiveConnectionInterface,
		property:      "Id",
	}] = "Office Wi-Fi"
	reader.values[propertyKey{
		destination:   argonetwork.NetworkManagerDestination,
		path:          activePath,
		interfaceName: argonetwork.ActiveConnectionInterface,
		property:      "Devices",
	}] = []string{firstDevice, secondDevice}
	reader.values[propertyKey{
		destination:   argonetwork.NetworkManagerDestination,
		path:          firstDevice,
		interfaceName: argonetwork.DeviceInterface,
		property:      "Interface",
	}] = ""
	reader.values[propertyKey{
		destination:   argonetwork.NetworkManagerDestination,
		path:          secondDevice,
		interfaceName: argonetwork.DeviceInterface,
		property:      "Interface",
	}] = "wlan0"

	snapshot, err := argonetwork.NewClient(reader).ReadState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Connected ||
		snapshot.State != argonetwork.ConnectionGlobal ||
		snapshot.Connectivity != argonetwork.ConnectivityFull ||
		snapshot.ActiveConnection != "Office Wi-Fi" ||
		snapshot.Interface != "wlan0" {
		t.Fatalf("unexpected active network state: %+v", snapshot)
	}
}

func TestNetworkManagerPropertyErrorsAndTypes(t *testing.T) {
	reader := managerPropertyReader(uint32(40), uint32(2), "/")
	connectivity := propertyKey{
		destination:   argonetwork.NetworkManagerDestination,
		path:          argonetwork.NetworkManagerPath,
		interfaceName: argonetwork.NetworkManagerInterface,
		property:      "Connectivity",
	}
	readError := errors.New("D-Bus unavailable")
	reader.errors[connectivity] = readError
	if _, err := argonetwork.NewClient(reader).ReadState(context.Background()); !errors.Is(err, readError) {
		t.Fatalf("property error was not preserved: %v", err)
	}

	reader = managerPropertyReader("connected", uint32(2), "/")
	if _, err := argonetwork.NewClient(reader).ReadState(context.Background()); err == nil {
		t.Fatal("invalid NetworkManager property type succeeded")
	}
}

func managerPropertyReader(state any, connectivity any, primary string) *networkPropertyReader {
	values := map[propertyKey]any{
		{
			destination:   argonetwork.NetworkManagerDestination,
			path:          argonetwork.NetworkManagerPath,
			interfaceName: argonetwork.NetworkManagerInterface,
			property:      "State",
		}: state,
		{
			destination:   argonetwork.NetworkManagerDestination,
			path:          argonetwork.NetworkManagerPath,
			interfaceName: argonetwork.NetworkManagerInterface,
			property:      "Connectivity",
		}: connectivity,
		{
			destination:   argonetwork.NetworkManagerDestination,
			path:          argonetwork.NetworkManagerPath,
			interfaceName: argonetwork.NetworkManagerInterface,
			property:      "PrimaryConnection",
		}: primary,
	}

	return &networkPropertyReader{
		values: values,
		errors: make(map[propertyKey]error),
	}
}

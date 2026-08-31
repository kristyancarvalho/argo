package network

import (
	"context"
	"fmt"
	"io"

	"github.com/godbus/dbus/v5"
)

const (
	NetworkManagerDestination = "org.freedesktop.NetworkManager"
	NetworkManagerPath        = "/org/freedesktop/NetworkManager"
	NetworkManagerInterface   = "org.freedesktop.NetworkManager"
	ActiveConnectionInterface = "org.freedesktop.NetworkManager.Connection.Active"
	DeviceInterface           = "org.freedesktop.NetworkManager.Device"
	disconnectedObjectPath    = "/"
	dbusPropertiesInterface   = "org.freedesktop.DBus.Properties"
	dbusPropertiesGetMethod   = dbusPropertiesInterface + ".Get"
)

type Connectivity string

const (
	ConnectivityUnknown Connectivity = "unknown"
	ConnectivityNone    Connectivity = "none"
	ConnectivityPortal  Connectivity = "portal"
	ConnectivityLimited Connectivity = "limited"
	ConnectivityFull    Connectivity = "full"
)

type ConnectionState string

const (
	ConnectionUnknown      ConnectionState = "unknown"
	ConnectionDisconnected ConnectionState = "disconnected"
	ConnectionConnecting   ConnectionState = "connecting"
	ConnectionLocal        ConnectionState = "connected-local"
	ConnectionSite         ConnectionState = "connected-site"
	ConnectionGlobal       ConnectionState = "connected-global"
)

type Snapshot struct {
	Connected        bool
	State            ConnectionState
	Connectivity     Connectivity
	ActiveConnection string
	Interface        string
}

type PropertyReader interface {
	ReadProperty(context.Context, string, string, string, string) (any, error)
}

type Client struct {
	properties PropertyReader
	closer     io.Closer
}

type systemPropertyReader struct {
	connection *dbus.Conn
}

func ConnectSystem() (*Client, error) {
	connection, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("connect to system D-Bus: %w", err)
	}

	return &Client{
		properties: &systemPropertyReader{connection: connection},
		closer:     connection,
	}, nil
}

func NewClient(properties PropertyReader) *Client {
	return &Client{properties: properties}
}

func (client *Client) Close() error {
	if client.closer == nil {
		return nil
	}

	return client.closer.Close()
}

func (client *Client) ReadState(ctx context.Context) (Snapshot, error) {
	stateValue, err := client.readManagerProperty(ctx, "State")
	if err != nil {
		return Snapshot{}, err
	}
	stateCode, err := uint32Value("State", stateValue)
	if err != nil {
		return Snapshot{}, err
	}
	connectivityValue, err := client.readManagerProperty(ctx, "Connectivity")
	if err != nil {
		return Snapshot{}, err
	}
	connectivityCode, err := uint32Value("Connectivity", connectivityValue)
	if err != nil {
		return Snapshot{}, err
	}
	primaryValue, err := client.readManagerProperty(ctx, "PrimaryConnection")
	if err != nil {
		return Snapshot{}, err
	}
	primaryPath, err := stringValue("PrimaryConnection", primaryValue)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{
		Connected:    stateCode >= 50 && stateCode <= 70,
		State:        decodeConnectionState(stateCode),
		Connectivity: decodeConnectivity(connectivityCode),
	}
	if primaryPath == "" || primaryPath == disconnectedObjectPath {
		return snapshot, nil
	}

	activeValue, err := client.properties.ReadProperty(
		ctx,
		NetworkManagerDestination,
		primaryPath,
		ActiveConnectionInterface,
		"Id",
	)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read active connection: %w", err)
	}
	snapshot.ActiveConnection, err = stringValue("Id", activeValue)
	if err != nil {
		return Snapshot{}, err
	}
	devicesValue, err := client.properties.ReadProperty(
		ctx,
		NetworkManagerDestination,
		primaryPath,
		ActiveConnectionInterface,
		"Devices",
	)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read active connection devices: %w", err)
	}
	devicePaths, err := stringsValue("Devices", devicesValue)
	if err != nil {
		return Snapshot{}, err
	}
	for _, devicePath := range devicePaths {
		interfaceValue, err := client.properties.ReadProperty(
			ctx,
			NetworkManagerDestination,
			devicePath,
			DeviceInterface,
			"Interface",
		)
		if err != nil {
			return Snapshot{}, fmt.Errorf("read active interface: %w", err)
		}
		name, err := stringValue("Interface", interfaceValue)
		if err != nil {
			return Snapshot{}, err
		}
		if name != "" {
			snapshot.Interface = name
			break
		}
	}

	return snapshot, nil
}

func (client *Client) readManagerProperty(ctx context.Context, property string) (any, error) {
	value, err := client.properties.ReadProperty(
		ctx,
		NetworkManagerDestination,
		NetworkManagerPath,
		NetworkManagerInterface,
		property,
	)
	if err != nil {
		return nil, fmt.Errorf("read NetworkManager %s: %w", property, err)
	}

	return value, nil
}

func (reader *systemPropertyReader) ReadProperty(
	ctx context.Context,
	destination string,
	objectPath string,
	interfaceName string,
	property string,
) (any, error) {
	path := dbus.ObjectPath(objectPath)
	if !path.IsValid() {
		return nil, fmt.Errorf("invalid D-Bus object path %q", objectPath)
	}
	var value dbus.Variant
	call := reader.connection.Object(destination, path).CallWithContext(
		ctx,
		dbusPropertiesGetMethod,
		0,
		interfaceName,
		property,
	)
	if err := call.Store(&value); err != nil {
		return nil, err
	}

	return normalizeDBusValue(value.Value()), nil
}

func normalizeDBusValue(value any) any {
	switch typed := value.(type) {
	case dbus.ObjectPath:
		return string(typed)
	case []dbus.ObjectPath:
		paths := make([]string, len(typed))
		for index, path := range typed {
			paths[index] = string(path)
		}
		return paths
	default:
		return value
	}
}

func uint32Value(property string, value any) (uint32, error) {
	typed, valid := value.(uint32)
	if !valid {
		return 0, fmt.Errorf("NetworkManager property %s has type %T, expected uint32", property, value)
	}

	return typed, nil
}

func stringValue(property string, value any) (string, error) {
	typed, valid := value.(string)
	if !valid {
		return "", fmt.Errorf("NetworkManager property %s has type %T, expected string", property, value)
	}

	return typed, nil
}

func stringsValue(property string, value any) ([]string, error) {
	typed, valid := value.([]string)
	if !valid {
		return nil, fmt.Errorf("NetworkManager property %s has type %T, expected string list", property, value)
	}

	return typed, nil
}

func decodeConnectivity(value uint32) Connectivity {
	switch value {
	case 1:
		return ConnectivityNone
	case 2:
		return ConnectivityPortal
	case 3:
		return ConnectivityLimited
	case 4:
		return ConnectivityFull
	default:
		return ConnectivityUnknown
	}
}

func decodeConnectionState(value uint32) ConnectionState {
	switch value {
	case 20, 30:
		return ConnectionDisconnected
	case 40:
		return ConnectionConnecting
	case 50:
		return ConnectionLocal
	case 60:
		return ConnectionSite
	case 70:
		return ConnectionGlobal
	default:
		return ConnectionUnknown
	}
}

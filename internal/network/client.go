package network

import (
	"context"
	"fmt"
	"io"
	"sync"

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

type Metered string

const (
	MeteredUnknown  Metered = "unknown"
	MeteredYes      Metered = "yes"
	MeteredNo       Metered = "no"
	MeteredGuessYes Metered = "guess-yes"
	MeteredGuessNo  Metered = "guess-no"
)

type Snapshot struct {
	Connected        bool
	State            ConnectionState
	Connectivity     Connectivity
	Metered          Metered
	ActiveConnection string
	ConnectionType   string
	Interface        string
}

type PropertyReader interface {
	ReadProperty(context.Context, string, string, string, string) (any, error)
}

type Client struct {
	mutex      sync.RWMutex
	properties PropertyReader
	events     EventSubscriber
	closer     io.Closer
	connector  func() (PropertyReader, EventSubscriber, io.Closer, error)
	closed     bool
}

type systemPropertyReader struct {
	connection *dbus.Conn
}

func ConnectSystem() (*Client, error) {
	client := &Client{connector: connectSystemResources}
	if err := client.Reconnect(context.Background()); err != nil {
		return nil, err
	}

	return client, nil
}

func connectSystemResources() (PropertyReader, EventSubscriber, io.Closer, error) {
	connection, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("connect to system D-Bus: %w", err)
	}

	return &systemPropertyReader{connection: connection},
		&systemEventSubscriber{connection: connection}, connection, nil
}

func NewClient(properties PropertyReader) *Client {
	return &Client{properties: properties}
}

func NewObservableClient(properties PropertyReader, events EventSubscriber) *Client {
	return &Client{properties: properties, events: events}
}

func (client *Client) Close() error {
	client.mutex.Lock()
	defer client.mutex.Unlock()
	if client.closed {
		return nil
	}
	client.closed = true
	if client.closer == nil {
		return nil
	}

	return client.closer.Close()
}

func (client *Client) Reconnect(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	client.mutex.RLock()
	connector := client.connector
	closed := client.closed
	client.mutex.RUnlock()
	if closed {
		return fmt.Errorf("network client is closed")
	}
	if connector == nil {
		return fmt.Errorf("network client cannot reconnect")
	}
	properties, events, closer, err := connector()
	if err != nil {
		return err
	}
	client.mutex.Lock()
	if client.closed {
		client.mutex.Unlock()
		_ = closer.Close()
		return fmt.Errorf("network client is closed")
	}
	previous := client.closer
	client.properties = properties
	client.events = events
	client.closer = closer
	client.mutex.Unlock()
	if previous != nil {
		_ = previous.Close()
	}

	return nil
}

func (client *Client) ReadState(ctx context.Context) (Snapshot, error) {
	client.mutex.RLock()
	defer client.mutex.RUnlock()
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
	metered := MeteredUnknown
	if meteredValue, meteredError := client.readManagerProperty(ctx, "Metered"); meteredError == nil {
		meteredCode, err := uint32Value("Metered", meteredValue)
		if err != nil {
			return Snapshot{}, err
		}
		metered = decodeMetered(meteredCode)
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
		Metered:      metered,
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
	typeValue, typeError := client.properties.ReadProperty(
		ctx,
		NetworkManagerDestination,
		primaryPath,
		ActiveConnectionInterface,
		"Type",
	)
	if typeError == nil {
		snapshot.ConnectionType, err = stringValue("Type", typeValue)
		if err != nil {
			return Snapshot{}, err
		}
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

func decodeMetered(value uint32) Metered {
	switch value {
	case 1:
		return MeteredYes
	case 2:
		return MeteredNo
	case 3:
		return MeteredGuessYes
	case 4:
		return MeteredGuessNo
	default:
		return MeteredUnknown
	}
}

func (metered Metered) IsMetered() bool {
	return metered == MeteredYes || metered == MeteredGuessYes
}

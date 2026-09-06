package network

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/godbus/dbus/v5"
)

const signalBufferSize = 16

const (
	dbusDestination = "org.freedesktop.DBus"
	dbusInterface   = "org.freedesktop.DBus"
	dbusPath        = "/org/freedesktop/DBus"
)

type Event struct{}

type Subscription interface {
	Events() <-chan Event
	Close() error
}

type EventSubscriber interface {
	Subscribe(context.Context) (Subscription, error)
}

type systemEventSubscriber struct {
	connection *dbus.Conn
}

type systemSubscription struct {
	connection *dbus.Conn
	matches    [][]dbus.MatchOption
	signals    chan *dbus.Signal
	events     chan Event
	cancel     context.CancelFunc
	finished   chan struct{}
	closeOnce  sync.Once
	closeError error
}

func (client *Client) Observe(ctx context.Context, emit func(Snapshot) error) error {
	client.mutex.RLock()
	events := client.events
	client.mutex.RUnlock()
	if events == nil {
		return fmt.Errorf("network event subscription is unavailable")
	}
	subscription, err := events.Subscribe(ctx)
	if err != nil {
		return fmt.Errorf("subscribe to NetworkManager events: %w", err)
	}
	defer func() {
		_ = subscription.Close()
	}()
	current, err := client.ReadState(ctx)
	if err != nil {
		return err
	}
	if err := emit(current); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, open := <-subscription.Events():
			if !open {
				return nil
			}
			next, err := client.ReadState(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			if next == current {
				continue
			}
			if err := emit(next); err != nil {
				return err
			}
			current = next
		}
	}
}

func (subscriber *systemEventSubscriber) Subscribe(ctx context.Context) (Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	matches := [][]dbus.MatchOption{
		{
			dbus.WithMatchSender(NetworkManagerDestination),
			dbus.WithMatchInterface(dbusPropertiesInterface),
			dbus.WithMatchMember("PropertiesChanged"),
			dbus.WithMatchPathNamespace(dbus.ObjectPath(NetworkManagerPath)),
		},
		{
			dbus.WithMatchSender(dbusDestination),
			dbus.WithMatchInterface(dbusInterface),
			dbus.WithMatchMember("NameOwnerChanged"),
			dbus.WithMatchObjectPath(dbus.ObjectPath(dbusPath)),
			dbus.WithMatchArg(0, NetworkManagerDestination),
		},
	}
	added := make([][]dbus.MatchOption, 0, len(matches))
	for _, options := range matches {
		if err := subscriber.connection.AddMatchSignalContext(ctx, options...); err != nil {
			for _, existing := range added {
				_ = subscriber.connection.RemoveMatchSignal(existing...)
			}
			return nil, err
		}
		added = append(added, options)
	}
	signals := make(chan *dbus.Signal, signalBufferSize)
	subscriber.connection.Signal(signals)
	subscriptionContext, cancel := context.WithCancel(ctx)
	subscription := &systemSubscription{
		connection: subscriber.connection,
		matches:    matches,
		signals:    signals,
		events:     make(chan Event, signalBufferSize),
		cancel:     cancel,
		finished:   make(chan struct{}),
	}
	go subscription.forward(subscriptionContext)

	return subscription, nil
}

func (subscription *systemSubscription) Events() <-chan Event {
	return subscription.events
}

func (subscription *systemSubscription) Close() error {
	subscription.closeOnce.Do(func() {
		subscription.cancel()
		subscription.connection.RemoveSignal(subscription.signals)
		var removeError error
		for _, options := range subscription.matches {
			removeError = errors.Join(removeError, subscription.connection.RemoveMatchSignal(options...))
		}
		<-subscription.finished
		subscription.closeError = removeError
	})

	return subscription.closeError
}

func (subscription *systemSubscription) forward(ctx context.Context) {
	defer close(subscription.finished)
	defer close(subscription.events)
	for {
		select {
		case <-ctx.Done():
			return
		case _, open := <-subscription.signals:
			if !open {
				return
			}
			select {
			case subscription.events <- Event{}:
			default:
			}
		}
	}
}

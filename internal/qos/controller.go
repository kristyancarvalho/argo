package qos

import (
	"context"
	"fmt"
	"sync"
)

type Backend interface {
	Apply(context.Context, DesiredState) error
	Remove(context.Context, string) error
}

type RecoveringBackend interface {
	Backend
	Recover(context.Context) (DesiredState, bool, error)
}

type Controller struct {
	backend Backend
	mutex   sync.Mutex
	current DesiredState
	applied bool
}

func NewController(backend Backend) (*Controller, error) {
	if backend == nil {
		return nil, fmt.Errorf("QoS backend is required")
	}

	return &Controller{backend: backend}, nil
}

func (controller *Controller) Recover(ctx context.Context) error {
	backend, ok := controller.backend.(RecoveringBackend)
	if !ok {
		return nil
	}
	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	state, applied, err := backend.Recover(ctx)
	if err != nil {
		return fmt.Errorf("recover QoS state: %w", err)
	}
	controller.current = state
	controller.applied = applied

	return nil
}

func (controller *Controller) Remove(ctx context.Context, interfaceName string) error {
	if err := ValidateInterface(interfaceName); err != nil {
		return err
	}
	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	if controller.applied && controller.current.Interface != interfaceName {
		return fmt.Errorf("interface %q does not own current QoS state", interfaceName)
	}
	if err := controller.backend.Remove(ctx, interfaceName); err != nil {
		return fmt.Errorf("remove QoS state from %s: %w", interfaceName, err)
	}
	if controller.current.Interface == interfaceName {
		controller.current = DesiredState{}
		controller.applied = false
	}

	return nil
}

func (controller *Controller) Reconcile(ctx context.Context, desired DesiredState) error {
	if err := desired.Validate(); err != nil {
		return err
	}
	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	if controller.applied && controller.current == desired {
		return nil
	}
	if !desired.Enabled {
		if !controller.applied {
			return nil
		}
		if err := controller.backend.Remove(ctx, controller.current.Interface); err != nil {
			return fmt.Errorf("remove QoS state from %s: %w", controller.current.Interface, err)
		}
		controller.current = DesiredState{}
		controller.applied = false
		return nil
	}
	if controller.applied && controller.current.Interface != desired.Interface {
		if err := controller.backend.Remove(ctx, controller.current.Interface); err != nil {
			return fmt.Errorf("remove QoS state from %s: %w", controller.current.Interface, err)
		}
		controller.current = DesiredState{}
		controller.applied = false
	}
	if err := controller.backend.Apply(ctx, desired); err != nil {
		controller.current = DesiredState{}
		controller.applied = false
		return fmt.Errorf("apply QoS state to %s: %w", desired.Interface, err)
	}
	controller.current = desired
	controller.applied = true

	return nil
}

func (controller *Controller) Current() (DesiredState, bool) {
	controller.mutex.Lock()
	defer controller.mutex.Unlock()

	return controller.current, controller.applied
}

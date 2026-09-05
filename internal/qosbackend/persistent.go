package qosbackend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/kristyancarvalho/argo/internal/qos"
)

const maximumStateSize = 64 * 1024

type Persistent struct {
	backend qos.Backend
	path    string
	mutex   sync.Mutex
}

func NewPersistent(backend qos.Backend, path string) (*Persistent, error) {
	if backend == nil {
		return nil, fmt.Errorf("QoS backend is required")
	}
	if path == "" || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("QoS recovery state path must be absolute")
	}

	return &Persistent{backend: backend, path: path}, nil
}

func (backend *Persistent) Apply(ctx context.Context, state qos.DesiredState) error {
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	if err := backend.backend.Apply(ctx, state); err != nil {
		return err
	}
	if err := backend.save(state); err != nil {
		return errors.Join(err, backend.backend.Remove(context.WithoutCancel(ctx), state.Interface))
	}

	return nil
}

func (backend *Persistent) Remove(ctx context.Context, interfaceName string) error {
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	if err := backend.backend.Remove(ctx, interfaceName); err != nil {
		return err
	}
	if err := os.Remove(backend.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove QoS recovery state: %w", err)
	}

	return nil
}

func (backend *Persistent) Recover(ctx context.Context) (qos.DesiredState, bool, error) {
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	info, err := os.Lstat(backend.path)
	if errors.Is(err, os.ErrNotExist) {
		return qos.DesiredState{}, false, nil
	}
	if err != nil {
		return qos.DesiredState{}, false, fmt.Errorf("inspect QoS recovery state: %w", err)
	}
	if !info.Mode().IsRegular() {
		return qos.DesiredState{}, false, fmt.Errorf("QoS recovery state is not a regular file")
	}
	file, err := os.Open(backend.path)
	if err != nil {
		return qos.DesiredState{}, false, fmt.Errorf("open QoS recovery state: %w", err)
	}
	defer func() {
		_ = file.Close()
	}()
	decoder := json.NewDecoder(io.LimitReader(file, maximumStateSize+1))
	decoder.DisallowUnknownFields()
	var state qos.DesiredState
	if err := decoder.Decode(&state); err != nil {
		return qos.DesiredState{}, false, fmt.Errorf("decode QoS recovery state: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return qos.DesiredState{}, false, fmt.Errorf("QoS recovery state contains trailing data")
	}
	if err := state.Validate(); err != nil || !state.Enabled {
		return qos.DesiredState{}, false, fmt.Errorf("invalid QoS recovery state")
	}
	if err := backend.backend.Apply(ctx, state); err != nil {
		return qos.DesiredState{}, false, fmt.Errorf("reapply QoS recovery state: %w", err)
	}

	return state, true, nil
}

func (backend *Persistent) save(state qos.DesiredState) (result error) {
	if err := os.MkdirAll(filepath.Dir(backend.path), 0o700); err != nil {
		return fmt.Errorf("create QoS recovery directory: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(backend.path), ".argo-qosd-state-")
	if err != nil {
		return fmt.Errorf("create QoS recovery state: %w", err)
	}
	temporaryPath := file.Name()
	defer func() {
		if result != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("secure QoS recovery state: %w", err)
	}
	if err := json.NewEncoder(file).Encode(state); err != nil {
		_ = file.Close()
		return fmt.Errorf("encode QoS recovery state: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync QoS recovery state: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close QoS recovery state: %w", err)
	}
	if err := os.Rename(temporaryPath, backend.path); err != nil {
		return fmt.Errorf("publish QoS recovery state: %w", err)
	}

	return nil
}

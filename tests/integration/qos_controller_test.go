package integration_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kristyancarvalho/argo/internal/qos"
)

type recordingQoSBackend struct {
	applied    []qos.DesiredState
	removed    []string
	applyError error
}

func (backend *recordingQoSBackend) Apply(_ context.Context, state qos.DesiredState) error {
	backend.applied = append(backend.applied, state)
	return backend.applyError
}

func TestQoSControllerDoesNotReportStaleStateAfterApplyFailure(t *testing.T) {
	backend := &recordingQoSBackend{}
	controller, err := qos.NewController(backend)
	if err != nil {
		t.Fatal(err)
	}
	balanced, err := qos.CalculateDesiredState(qos.Intent{Policy: qos.PolicyBalanced, Interface: "eth0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Reconcile(context.Background(), balanced); err != nil {
		t.Fatal(err)
	}
	backend.applyError = errors.New("kernel rejected update")
	throughput := balanced
	throughput.Policy = qos.PolicyThroughput
	if err := controller.Reconcile(context.Background(), throughput); err == nil {
		t.Fatal("failed update succeeded")
	}
	if current, applied := controller.Current(); applied || current != (qos.DesiredState{}) {
		t.Fatalf("controller retained stale state after failure: %+v, applied %t", current, applied)
	}
}

func (backend *recordingQoSBackend) Remove(_ context.Context, interfaceName string) error {
	backend.removed = append(backend.removed, interfaceName)
	return nil
}

func TestQoSControllerReconcilesIdempotently(t *testing.T) {
	backend := &recordingQoSBackend{}
	controller, err := qos.NewController(backend)
	if err != nil {
		t.Fatal(err)
	}
	balanced, err := qos.CalculateDesiredState(qos.Intent{
		Policy:    qos.PolicyBalanced,
		Interface: "eth0",
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := controller.Reconcile(context.Background(), balanced); err != nil {
			t.Fatal(err)
		}
	}
	if len(backend.applied) != 1 || len(backend.removed) != 0 {
		t.Fatalf("unexpected repeated operations: applied %+v, removed %+v", backend.applied, backend.removed)
	}
	throughput := balanced
	throughput.Policy = qos.PolicyThroughput
	if err := controller.Reconcile(context.Background(), throughput); err != nil {
		t.Fatal(err)
	}
	if len(backend.applied) != 2 || len(backend.removed) != 0 {
		t.Fatalf("unexpected policy-change operations: applied %+v, removed %+v", backend.applied, backend.removed)
	}
	wifi := throughput
	wifi.Interface = "wlan0"
	if err := controller.Reconcile(context.Background(), wifi); err != nil {
		t.Fatal(err)
	}
	if len(backend.applied) != 3 || len(backend.removed) != 1 || backend.removed[0] != "eth0" {
		t.Fatalf("unexpected interface-change operations: applied %+v, removed %+v", backend.applied, backend.removed)
	}
	off, err := qos.CalculateDesiredState(qos.Intent{Policy: qos.PolicyOff})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := controller.Reconcile(context.Background(), off); err != nil {
			t.Fatal(err)
		}
	}
	if len(backend.removed) != 2 || backend.removed[1] != "wlan0" {
		t.Fatalf("unexpected removal operations: %+v", backend.removed)
	}
	if current, applied := controller.Current(); applied || current != (qos.DesiredState{}) {
		t.Fatalf("unexpected controller state: %+v, applied %t", current, applied)
	}
}

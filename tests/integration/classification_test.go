package integration_test

import (
	"context"
	"testing"

	"github.com/kristyancarvalho/argo/internal/qos"
)

type recordingClassificationBackend struct {
	applied []qos.ClassificationPlan
	removed []string
}

func (backend *recordingClassificationBackend) ApplyClassification(
	_ context.Context,
	plan qos.ClassificationPlan,
) error {
	backend.applied = append(backend.applied, plan)
	return nil
}

func (backend *recordingClassificationBackend) RemoveClassification(
	_ context.Context,
	interfaceName string,
) error {
	backend.removed = append(backend.removed, interfaceName)
	return nil
}

func TestClassificationLifecycleAndInterfaceChange(t *testing.T) {
	backend := &recordingClassificationBackend{}
	controller, err := qos.NewClassificationController(backend)
	if err != nil {
		t.Fatal(err)
	}
	ethernet, err := qos.GenerateClassification("eth0", 42)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := controller.Reconcile(context.Background(), ethernet); err != nil {
			t.Fatal(err)
		}
	}
	if len(backend.applied) != 1 || len(backend.removed) != 0 {
		t.Fatalf("unexpected idempotent operations: %+v %+v", backend.applied, backend.removed)
	}
	wifi, err := qos.GenerateClassification("wlan0", 42)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Reconcile(context.Background(), wifi); err != nil {
		t.Fatal(err)
	}
	if len(backend.applied) != 2 || len(backend.removed) != 1 || backend.removed[0] != "eth0" {
		t.Fatalf("unexpected interface transition: %+v %+v", backend.applied, backend.removed)
	}
	for range 3 {
		if err := controller.Reconcile(context.Background(), qos.ClassificationPlan{}); err != nil {
			t.Fatal(err)
		}
	}
	if len(backend.removed) != 2 || backend.removed[1] != "wlan0" {
		t.Fatalf("unexpected cleanup operations: %+v", backend.removed)
	}
	if current, applied := controller.Current(); applied || current != (qos.ClassificationPlan{}) {
		t.Fatalf("classification remains active: %+v %t", current, applied)
	}
}

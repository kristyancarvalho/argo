package integration_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kristyancarvalho/argo/internal/qos"
	"github.com/kristyancarvalho/argo/internal/qosbackend"
)

func TestQoSRecoveryStateSurvivesProcessRestartAndCanBeRemoved(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "argo-qosd.state")
	firstKernel := &recordingQoSBackend{}
	first, err := qosbackend.NewPersistent(firstKernel, statePath)
	if err != nil {
		t.Fatal(err)
	}
	desired, err := qos.MapPolicy(qos.PolicyBalanced, qos.PolicyEnvironment{
		Interface:             "eth0",
		LinkRateBitsPerSecond: 100_000_000,
		Cgroup:                qos.CgroupSelector{Path: "user.slice/argod.service", Level: 2},
		ActiveDownloads:       1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Apply(context.Background(), desired); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("recovery state mode is %o", info.Mode().Perm())
	}

	secondKernel := &recordingQoSBackend{}
	second, err := qosbackend.NewPersistent(secondKernel, statePath)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := qos.NewController(second)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	current, applied := controller.Current()
	if !applied || current != desired || len(secondKernel.applied) != 1 || secondKernel.applied[0] != desired {
		t.Fatalf("recovered state is %+v applied=%t kernel=%+v", current, applied, secondKernel.applied)
	}
	if err := controller.Remove(context.Background(), "eth0"); err != nil {
		t.Fatal(err)
	}
	if len(secondKernel.removed) != 1 || secondKernel.removed[0] != "eth0" {
		t.Fatalf("unexpected recovery cleanup: %+v", secondKernel.removed)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("recovery state remains after cleanup: %v", err)
	}
}

func TestQoSRemovalReconcilesUnknownKernelState(t *testing.T) {
	backend := &recordingQoSBackend{}
	controller, err := qos.NewController(backend)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Remove(context.Background(), "eth0"); err != nil {
		t.Fatal(err)
	}
	if len(backend.removed) != 1 || backend.removed[0] != "eth0" {
		t.Fatalf("unknown kernel state was not reconciled: %+v", backend.removed)
	}
}

func TestQoSRecoveryRejectsSymlinkAndInvalidState(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte(`{"enabled":false,"policy":"off"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(directory, "state")
	if err := os.Symlink(target, statePath); err != nil {
		t.Fatal(err)
	}
	backend, err := qosbackend.NewPersistent(&recordingQoSBackend{}, statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := backend.Recover(context.Background()); err == nil {
		t.Fatal("symlink recovery state was accepted")
	}
	if _, err := qosbackend.NewPersistent(&recordingQoSBackend{}, "relative"); err == nil {
		t.Fatal("relative recovery state path was accepted")
	}
}

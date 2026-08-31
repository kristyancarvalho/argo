package e2e_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/qosipc"
)

func TestQoSHelperExecutableLifecycle(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(workingDirectory, "..", ".."))
	temporaryDirectory := t.TempDir()
	binary := filepath.Join(temporaryDirectory, "argo-qosd")
	build := exec.Command("go", "build", "-o", binary, "./cmd/argo-qosd")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build argo-qosd: %v: %s", err, output)
	}
	socketPath := filepath.Join(temporaryDirectory, "argo-qosd.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(
		ctx,
		binary,
		"-socket", socketPath,
		"-allowed-uid", strconv.Itoa(os.Getuid()),
	)
	var standardError bytes.Buffer
	command.Stderr = &standardError
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if command.ProcessState == nil {
			cancel()
			_ = command.Wait()
		}
	}()
	client := qosipc.NewClient(socketPath)
	deadline := time.Now().Add(3 * time.Second)
	for {
		status, err := client.Status(ctx)
		if err == nil {
			if status.Applied {
				t.Fatalf("fresh helper has applied state: %+v", status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("QoS helper did not become ready: %v: %s", err, standardError.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("QoS helper shutdown: %v: %s", err, standardError.String())
	}
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("QoS helper socket remains after shutdown: %v", err)
	}
}

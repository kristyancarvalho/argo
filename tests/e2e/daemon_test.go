package e2e_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/ipc"
)

func TestDaemonExecutableLifecycle(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(workingDirectory, "..", ".."))
	temporaryDirectory := t.TempDir()
	binary := filepath.Join(temporaryDirectory, "argod")
	build := exec.Command("go", "build", "-o", binary, "./cmd/argod")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build argod: %v: %s", err, output)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary)
	command.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+temporaryDirectory)
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

	socketPath := filepath.Join(temporaryDirectory, "argo", "argod.sock")
	client := ipc.NewClient(socketPath)
	deadline := time.Now().Add(3 * time.Second)
	for {
		status, err := client.Status(ctx)
		if err == nil {
			if status.State != "running" {
				t.Fatalf("unexpected daemon state %q", status.State)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon did not become ready: %v: %s", err, standardError.String())
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("daemon shutdown: %v: %s", err, standardError.String())
	}
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("daemon socket remains after executable shutdown: %v", err)
	}
}

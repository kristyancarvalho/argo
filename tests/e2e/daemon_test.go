package e2e_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	clientBinary := filepath.Join(temporaryDirectory, "argo-client")
	build = exec.Command("go", "build", "-o", clientBinary, "./cmd/argo")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build argo: %v: %s", err, output)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary)
	environment := append(
		os.Environ(),
		"XDG_CONFIG_HOME="+filepath.Join(temporaryDirectory, "config"),
		"XDG_RUNTIME_DIR="+temporaryDirectory,
		"XDG_DATA_HOME="+filepath.Join(temporaryDirectory, "data"),
	)
	command.Env = environment
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

	tuiContext, stopTUI := context.WithTimeout(ctx, 3*time.Second)
	tuiCommand := exec.CommandContext(tuiContext, clientBinary, "tui")
	tuiCommand.Env = environment
	tuiCommand.Stdin = strings.NewReader("a\x03")
	var tuiOutput bytes.Buffer
	tuiCommand.Stdout = &tuiOutput
	tuiCommand.Stderr = &tuiOutput
	if err := tuiCommand.Run(); err != nil {
		stopTUI()
		t.Fatalf("run TUI: %v: %s", err, tuiOutput.String())
	}
	stopTUI()
	if !strings.Contains(tuiOutput.String(), "Argo") {
		t.Fatalf("TUI output does not contain application title: %q", tuiOutput.String())
	}
	if !strings.Contains(tuiOutput.String(), "\x1b[?1049l") {
		t.Fatalf("TUI did not restore the terminal after modal Ctrl+C: %q", tuiOutput.String())
	}
	status, err := client.Status(ctx)
	if err != nil || status.State != "running" {
		t.Fatalf("daemon stopped after TUI exit: status=%+v error=%v", status, err)
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

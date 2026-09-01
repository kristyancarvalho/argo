package e2e_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCLIBasicDownloadFlow(t *testing.T) {
	payload := []byte("complete command-line download flow")
	releaseDownload := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseDownload) })
	})
	httpServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		<-releaseDownload
		_, _ = response.Write(payload)
	}))
	defer httpServer.Close()

	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(workingDirectory, "..", ".."))
	temporaryDirectory := t.TempDir()
	argoBinary := filepath.Join(temporaryDirectory, "argo")
	argodBinary := filepath.Join(temporaryDirectory, "argod")
	build := exec.Command("go", "build", "-o", argoBinary, "./cmd/argo")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build argo: %v: %s", err, output)
	}
	build = exec.Command("go", "build", "-o", argodBinary, "./cmd/argod")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build argod: %v: %s", err, output)
	}

	runtimeDirectory := filepath.Join(temporaryDirectory, "runtime")
	dataDirectory := filepath.Join(temporaryDirectory, "data")
	configDirectory := filepath.Join(temporaryDirectory, "config")
	destination := filepath.Join(temporaryDirectory, "downloads")
	if err := os.MkdirAll(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(configDirectory, "argo"), 0o700); err != nil {
		t.Fatal(err)
	}
	profileConfig, err := os.ReadFile(filepath.Join(root, "tests", "fixtures", "profiles", "qos.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDirectory, "argo", "config.toml"), profileConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	environment := append(
		os.Environ(),
		"XDG_RUNTIME_DIR="+runtimeDirectory,
		"XDG_DATA_HOME="+dataDirectory,
		"XDG_CONFIG_HOME="+configDirectory,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	daemonCommand := exec.CommandContext(ctx, argodBinary)
	daemonCommand.Env = environment
	var daemonError bytes.Buffer
	daemonCommand.Stderr = &daemonError
	if err := daemonCommand.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if daemonCommand.ProcessState == nil {
			cancel()
			_ = daemonCommand.Wait()
		}
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := executeCLI(ctx, argoBinary, destination, environment, "status"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon did not become ready: %s", daemonError.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	profileOutput, err := executeCLI(ctx, argoBinary, destination, environment, "profile", "focused")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(profileOutput, "Active profile: focused") || !strings.Contains(profileOutput, "Traffic policy: throughput") {
		t.Fatalf("unexpected profile output %q", profileOutput)
	}
	unknownOutput, err := executeCLI(ctx, argoBinary, destination, environment, "profile", "missing")
	if err == nil || !strings.Contains(unknownOutput, "unknown profile") {
		t.Fatalf("unknown profile returned %v: %q", err, unknownOutput)
	}

	addOutput, err := executeCLI(
		ctx,
		argoBinary,
		destination,
		environment,
		"add",
		httpServer.URL+"/cli.bin",
	)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(addOutput)
	if len(fields) < 2 || fields[0] != "Added" {
		t.Fatalf("unexpected add output %q", addOutput)
	}
	identifier := fields[1]
	priorityOutput, err := executeCLI(
		ctx,
		argoBinary,
		destination,
		environment,
		"priority",
		identifier,
		"high",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(priorityOutput, identifier+": high") {
		t.Fatalf("unexpected priority output %q", priorityOutput)
	}
	releaseOnce.Do(func() { close(releaseDownload) })

	deadline = time.Now().Add(3 * time.Second)
	for {
		showOutput, err := executeCLI(ctx, argoBinary, destination, environment, "show", identifier)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(showOutput, "Status: completed") && strings.Contains(showOutput, "Priority: high") {
			break
		}
		if strings.Contains(showOutput, "Status: failed") {
			t.Fatalf("CLI download failed: %s", showOutput)
		}
		if time.Now().After(deadline) {
			t.Fatalf("CLI download did not complete: %s", showOutput)
		}
		time.Sleep(10 * time.Millisecond)
	}
	listOutput, err := executeCLI(ctx, argoBinary, destination, environment, "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listOutput, identifier) || !strings.Contains(listOutput, "completed") {
		t.Fatalf("unexpected list output %q", listOutput)
	}
	watchOutput, err := executeCLI(ctx, argoBinary, destination, environment, "watch")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(watchOutput, identifier) || !strings.Contains(watchOutput, "completed") {
		t.Fatalf("unexpected watch output %q", watchOutput)
	}
	content, err := os.ReadFile(filepath.Join(destination, "cli.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(payload) {
		t.Fatalf("downloaded content %q does not match %q", content, payload)
	}

	if err := daemonCommand.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := daemonCommand.Wait(); err != nil {
		t.Fatalf("daemon shutdown: %v: %s", err, daemonError.String())
	}
}

func executeCLI(
	ctx context.Context,
	binary string,
	directory string,
	environment []string,
	arguments ...string,
) (string, error) {
	command := exec.CommandContext(ctx, binary, arguments...)
	command.Dir = directory
	command.Env = environment
	output, err := command.CombinedOutput()

	return string(output), err
}

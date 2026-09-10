package e2e_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	stateDirectory := filepath.Join(temporaryDirectory, "state")
	homeDirectory := filepath.Join(temporaryDirectory, "home")
	destination := filepath.Join(homeDirectory, "Downloads")
	invocationDirectory := filepath.Join(temporaryDirectory, "invocation")
	if err := os.MkdirAll(invocationDirectory, 0o700); err != nil {
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
		"XDG_STATE_HOME="+stateDirectory,
		"HOME="+homeDirectory,
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
		if _, err := executeCLI(ctx, argoBinary, invocationDirectory, environment, "status"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon did not become ready: %s", daemonError.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	profileOutput, err := executeCLI(ctx, argoBinary, invocationDirectory, environment, "profile", "focused")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(profileOutput, "Active profile: focused") || !strings.Contains(profileOutput, "Traffic policy: throughput") {
		t.Fatalf("unexpected profile output %q", profileOutput)
	}
	unknownOutput, err := executeCLI(ctx, argoBinary, invocationDirectory, environment, "profile", "missing")
	if err == nil || !strings.Contains(unknownOutput, "unknown profile") {
		t.Fatalf("unknown profile returned %v: %q", err, unknownOutput)
	}

	digest := sha256.Sum256(payload)
	checksum := "sha256:" + hex.EncodeToString(digest[:])
	addOutput, err := executeCLI(
		ctx,
		argoBinary,
		invocationDirectory,
		environment,
		"add",
		"--checksum",
		checksum,
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
		invocationDirectory,
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
		showOutput, err := executeCLI(ctx, argoBinary, invocationDirectory, environment, "show", identifier)
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
	listOutput, err := executeCLI(ctx, argoBinary, invocationDirectory, environment, "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listOutput, identifier) || !strings.Contains(listOutput, "completed") {
		t.Fatalf("unexpected list output %q", listOutput)
	}
	verifyOutput, err := executeCLI(ctx, argoBinary, invocationDirectory, environment, "verify", identifier)
	if err != nil || !strings.Contains(verifyOutput, "Verified "+identifier) || !strings.Contains(verifyOutput, checksum) {
		t.Fatalf("unexpected verify result %v: %q", err, verifyOutput)
	}
	watchOutput, err := executeCLI(ctx, argoBinary, invocationDirectory, environment, "watch")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(watchOutput, identifier) || !strings.Contains(watchOutput, "completed") {
		t.Fatalf("unexpected watch output %q", watchOutput)
	}
	content, err := os.ReadFile(filepath.Join(destination, "cli.bin"))
	if err != nil {
		showOutput, showErr := executeCLI(ctx, argoBinary, invocationDirectory, environment, "show", identifier)
		t.Fatalf("read default destination: %v; show: %v: %s", err, showErr, showOutput)
	}
	if string(content) != string(payload) {
		t.Fatalf("downloaded content %q does not match %q", content, payload)
	}
	retryOutput, err := executeCLI(ctx, argoBinary, invocationDirectory, environment, "retry", identifier)
	if err != nil {
		t.Fatal(err)
	}
	retryFields := strings.Fields(retryOutput)
	if len(retryFields) < 2 || retryFields[0] != "Added" || retryFields[1] == identifier {
		t.Fatalf("unexpected retry output %q", retryOutput)
	}
	retryID := retryFields[1]
	waitForCLICompletion(t, ctx, argoBinary, invocationDirectory, environment, retryID)
	repeatedOutput, err := executeCLI(ctx, argoBinary, invocationDirectory, environment, "add", httpServer.URL+"/cli.bin")
	if err != nil {
		t.Fatal(err)
	}
	repeatedFields := strings.Fields(repeatedOutput)
	if len(repeatedFields) < 2 || repeatedFields[1] == identifier || repeatedFields[1] == retryID {
		t.Fatalf("repeated URL did not create a new identity: %q", repeatedOutput)
	}
	repeatedID := repeatedFields[1]
	waitForCLICompletion(t, ctx, argoBinary, invocationDirectory, environment, repeatedID)
	for _, filename := range []string{"cli.bin", "cli (1).bin", "cli (2).bin"} {
		content, err := os.ReadFile(filepath.Join(destination, filename))
		if err != nil || string(content) != string(payload) {
			t.Fatalf("repeated download %q is invalid: %v", filename, err)
		}
	}
	removeOutput, err := executeCLI(ctx, argoBinary, invocationDirectory, environment, "remove", identifier)
	if err != nil || !strings.Contains(removeOutput, identifier+": removed") {
		t.Fatalf("unexpected remove result %v: %q", err, removeOutput)
	}
	if content, err := os.ReadFile(filepath.Join(destination, "cli.bin")); err != nil || string(content) != string(payload) {
		t.Fatalf("remove changed completed file: %v", err)
	}
	clearOutput, err := executeCLI(ctx, argoBinary, invocationDirectory, environment, "clear")
	if err != nil || !strings.Contains(clearOutput, "Removed 2 historical downloads") {
		t.Fatalf("unexpected clear result %v: %q", err, clearOutput)
	}
	clearedList, err := executeCLI(ctx, argoBinary, invocationDirectory, environment, "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, removedID := range []string{identifier, retryID, repeatedID} {
		if strings.Contains(clearedList, removedID) {
			t.Fatalf("cleared list still contains %s: %q", removedID, clearedList)
		}
	}
	entries, err := os.ReadDir(invocationDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("CLI working directory contains runtime files: %v", entries)
	}

	if err := daemonCommand.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := daemonCommand.Wait(); err != nil {
		t.Fatalf("daemon shutdown: %v: %s", err, daemonError.String())
	}
}

func waitForCLICompletion(
	t *testing.T,
	ctx context.Context,
	binary string,
	directory string,
	environment []string,
	identifier string,
) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		output, err := executeCLI(ctx, binary, directory, environment, "show", identifier)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output, "Status: completed") {
			return
		}
		if strings.Contains(output, "Status: failed") || time.Now().After(deadline) {
			t.Fatalf("download %s did not complete: %s", identifier, output)
		}
		time.Sleep(10 * time.Millisecond)
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

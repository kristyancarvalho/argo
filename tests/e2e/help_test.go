package e2e_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorJSONIsReadOnlyWithoutDaemon(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(workingDirectory, "..", ".."))
	binary := filepath.Join(t.TempDir(), "argo")
	build := exec.Command("go", "build", "-o", binary, "./cmd/argo")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build argo: %v: %s", err, output)
	}
	environmentRoot := t.TempDir()
	runtimeDirectory := filepath.Join(environmentRoot, "runtime")
	dataDirectory := filepath.Join(environmentRoot, "data")
	stateDirectory := filepath.Join(environmentRoot, "state")
	configDirectory := filepath.Join(environmentRoot, "config")
	homeDirectory := filepath.Join(environmentRoot, "home")
	if err := os.Mkdir(runtimeDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "doctor", "--json")
	command.Env = append(environmentWithout(os.Environ(), "HOME"),
		"HOME="+homeDirectory,
		"XDG_RUNTIME_DIR="+runtimeDirectory,
		"XDG_DATA_HOME="+dataDirectory,
		"XDG_STATE_HOME="+stateDirectory,
		"XDG_CONFIG_HOME="+configDirectory,
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err = command.Run()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 3 {
		t.Fatalf("doctor returned %v, expected daemon-unavailable exit: stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	var document struct {
		SchemaVersion int    `json:"schema_version"`
		Kind          string `json:"kind"`
		Data          struct {
			CoreReady bool `json:"core_ready"`
			Checks    []struct {
				Name string `json:"name"`
			} `json:"checks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatalf("decode doctor JSON: %v: %s", err, stdout.String())
	}
	if document.SchemaVersion != 1 || document.Kind != "doctor" || document.Data.CoreReady || len(document.Data.Checks) == 0 {
		t.Fatalf("unexpected doctor document: %+v", document)
	}
	for _, path := range []string{dataDirectory, stateDirectory, configDirectory, homeDirectory} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("doctor created runtime state at %s: %v", path, err)
		}
	}
}

func TestHelpDoesNotRequireDaemon(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(workingDirectory, "..", ".."))
	binary := filepath.Join(t.TempDir(), "argo")
	build := exec.Command("go", "build", "-o", binary, "./cmd/argo")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build argo: %v: %s", err, output)
	}
	for _, argument := range []string{"help", "-h", "--help"} {
		command := exec.Command(binary, argument)
		command.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+filepath.Join(t.TempDir(), "missing"))
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("argo %s: %v: %s", argument, err, output)
		}
		if !strings.Contains(string(output), "Traffic policies:") || !strings.Contains(string(output), "priority") {
			t.Fatalf("argo %s returned incomplete help: %q", argument, output)
		}
	}
}

func TestCLIUsesStableUsageAndDaemonUnavailableExitCodes(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(workingDirectory, "..", ".."))
	binary := filepath.Join(t.TempDir(), "argo")
	build := exec.Command("go", "build", "-o", binary, "./cmd/argo")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build argo: %v: %s", err, output)
	}
	runtimeDirectory := t.TempDir()
	for _, test := range []struct {
		arguments []string
		code      int
	}{
		{[]string{"unknown"}, 2},
		{[]string{"status"}, 3},
	} {
		command := exec.Command(binary, test.arguments...)
		command.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+runtimeDirectory)
		output, err := command.CombinedOutput()
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) || exitError.ExitCode() != test.code {
			t.Fatalf("argo %v returned %v, expected exit %d: %s", test.arguments, err, test.code, output)
		}
	}
}

func TestHelpColorTerminalModes(t *testing.T) {
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("script is unavailable")
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(workingDirectory, "..", ".."))
	binary := filepath.Join(t.TempDir(), "argo")
	build := exec.Command("go", "build", "-o", binary, "./cmd/argo")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build argo: %v: %s", err, output)
	}
	colored := exec.Command("script", "-qec", binary+" --help", "/dev/null")
	colored.Env = environmentWithout(os.Environ(), "NO_COLOR")
	coloredOutput, err := colored.CombinedOutput()
	if err != nil {
		t.Fatalf("colored help: %v: %s", err, coloredOutput)
	}
	if !strings.Contains(string(coloredOutput), "\x1b[") {
		t.Fatalf("terminal help has no colors: %q", coloredOutput)
	}
	plain := exec.Command("script", "-qec", binary+" --help", "/dev/null")
	plain.Env = append(environmentWithout(os.Environ(), "NO_COLOR"), "NO_COLOR=1")
	plainOutput, err := plain.CombinedOutput()
	if err != nil {
		t.Fatalf("NO_COLOR help: %v: %s", err, plainOutput)
	}
	if strings.Contains(string(plainOutput), "\x1b[") {
		t.Fatalf("NO_COLOR help contains colors: %q", plainOutput)
	}
}

func environmentWithout(environment []string, name string) []string {
	prefix := name + "="
	filtered := make([]string, 0, len(environment))
	for _, value := range environment {
		if !strings.HasPrefix(value, prefix) {
			filtered = append(filtered, value)
		}
	}

	return filtered
}

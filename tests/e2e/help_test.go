package e2e_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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

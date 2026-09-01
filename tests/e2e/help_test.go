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

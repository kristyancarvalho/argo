package integration_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestExecutablesBuild(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	command := exec.Command("go", "build", "./cmd/argo", "./cmd/argod")
	command.Dir = filepath.Clean(filepath.Join(workingDirectory, "..", ".."))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build executables: %v: %s", err, output)
	}
}

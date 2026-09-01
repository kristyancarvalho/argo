package e2e_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestFreshExecutableInstall(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(workingDirectory, "..", ".."))
	installDirectory := filepath.Join(t.TempDir(), "bin")
	command := exec.Command("go", "install", "./cmd/...")
	command.Dir = root
	command.Env = append(os.Environ(), "GOBIN="+installDirectory)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("install executables: %v: %s", err, output)
	}
	for _, name := range []string{"argo", "argod", "argo-qosd"} {
		info, err := os.Stat(filepath.Join(installDirectory, name))
		if err != nil {
			t.Fatalf("installed executable %s: %v", name, err)
		}
		if info.Mode()&0o111 == 0 {
			t.Fatalf("installed file %s is not executable", name)
		}
	}
}

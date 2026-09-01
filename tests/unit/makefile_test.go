package unit_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMakefileExposesProjectChecks(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test location")
	}
	content, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	makefile := string(content)
	for _, target := range []string{
		"format:",
		"format-check:",
		"vet:",
		"lint:",
		"test-unit:",
		"test-integration:",
		"test-e2e:",
		"test-race:",
		"build:",
		"check: format-check vet lint test test-race build",
		"ci: check",
	} {
		if !strings.Contains(makefile, target) {
			t.Fatalf("Makefile does not contain %q", target)
		}
	}
}

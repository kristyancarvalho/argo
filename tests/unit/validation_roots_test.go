package unit_test

import (
	"bytes"
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormattingOnlyTouchesProjectRootsAndPropagatesErrors(t *testing.T) {
	root := validationFixture(t)
	if output, err := runFixtureMake(root, "format-check"); err == nil {
		t.Fatalf("unformatted project source passed: %s", output)
	}
	if output, err := runFixtureMake(root, "format"); err != nil {
		t.Fatalf("format inspected unrelated source: %v: %s", err, output)
	}
	if output, err := runFixtureMake(root, "format-check"); err != nil {
		t.Fatalf("formatted project failed: %v: %s", err, output)
	}
	for _, path := range []string{"specs/audit/foreign.go", "website/node_modules/example/foreign.go"} {
		content, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || string(content) != "not valid Go; do not modify\n" {
			t.Fatalf("unrelated file %s changed: %q, %v", path, content, err)
		}
	}
	for _, directory := range []string{"cmd", "internal", "tests"} {
		content, err := os.ReadFile(filepath.Join(root, directory, "new.go"))
		if err != nil {
			t.Fatal(err)
		}
		formatted, err := format.Source(content)
		if err != nil || !bytes.Equal(content, formatted) {
			t.Fatalf("new project file in %s was not formatted: %v", directory, err)
		}
	}
	writeValidationFixture(t, root, "cmd/new.go", "invalid Go source\n")
	if output, err := runFixtureMake(root, "format-check"); err == nil {
		t.Fatalf("gofmt parser failure was suppressed: %s", output)
	}
}

func TestValidationIncludesNewProjectPackagesAndPropagatesTestFailure(t *testing.T) {
	root := validationFixture(t)
	for _, target := range []string{"vet", "test-race"} {
		if output, err := runFixtureMake(root, target); err != nil {
			t.Fatalf("%s included unrelated invalid source: %v: %s", target, err, output)
		}
	}
	output, err := runFixtureMake(root, "-n", "lint")
	if err != nil || !strings.Contains(string(output), "run ./cmd/... ./internal/... ./tests/...") {
		t.Fatalf("lint package roots are inconsistent: %s, %v", output, err)
	}
	writeValidationFixture(t, root, "tests/failure_test.go", "package tests\nimport \"testing\"\nfunc TestRequired(t *testing.T) { t.Fatal(\"required project test failed\") }\n")
	output, err = runFixtureMake(root, "test-race")
	if err == nil || !strings.Contains(string(output), "required project test failed") {
		t.Fatalf("required project test failure did not propagate: %s, %v", output, err)
	}
}

func validationFixture(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("make"); err != nil {
		t.Skipf("make unavailable: %v", err)
	}
	root := t.TempDir()
	makefile, err := os.ReadFile(filepath.Join(repositoryRoot(t), "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	writeValidationFixture(t, root, "Makefile", string(makefile))
	writeValidationFixture(t, root, "go.mod", "module example.test/validation\n\ngo 1.27.0\n")
	for _, directory := range []string{"cmd", "internal", "tests"} {
		writeValidationFixture(t, root, directory+"/new.go", "package "+directory+"\nfunc Value()int{return 1}\n")
	}
	for _, path := range []string{"specs/audit/foreign.go", "website/node_modules/example/foreign.go"} {
		writeValidationFixture(t, root, path, "not valid Go; do not modify\n")
	}
	return root
}

func writeValidationFixture(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runFixtureMake(root string, arguments ...string) ([]byte, error) {
	command := exec.Command("make", append([]string{"-s"}, arguments...)...)
	command.Dir = root
	return command.CombinedOutput()
}

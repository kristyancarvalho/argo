package unit_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repositoryRoot(t *testing.T) string {
	t.Helper()

	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	return filepath.Clean(filepath.Join(workingDirectory, "..", ".."))
}

func TestRepositoryFoundation(t *testing.T) {
	root := repositoryRoot(t)
	required := []string{
		".github/ISSUE_TEMPLATE/bug_report.yml",
		".github/ISSUE_TEMPLATE/config.yml",
		".github/ISSUE_TEMPLATE/feature_request.yml",
		".github/ISSUE_TEMPLATE/implementation.yml",
		".github/workflows/ci.yml",
		".github/workflows/tests.yml",
		".golangci.yml",
		"LICENSE",
		"README.md",
		"go.mod",
	}

	for _, path := range required {
		if _, err := os.Stat(filepath.Join(root, path)); err != nil {
			t.Errorf("required repository file %s: %v", path, err)
		}
	}
}

func TestDedicatedTestsWorkflowRunsRequiredSuites(t *testing.T) {
	content, err := os.ReadFile(filepath.Join(repositoryRoot(t), ".github", "workflows", "tests.yml"))
	if err != nil {
		t.Fatal(err)
	}

	workflow := string(content)
	for _, expected := range []string{
		"name: Tests",
		"pull_request:",
		"push:",
		"- dev",
		"- main",
		"concurrency:",
		"cancel-in-progress: true",
		"go test ./tests/unit/...",
		"go test ./tests/integration/...",
		"go test ./tests/e2e/...",
		"go test -race ./...",
	} {
		if !strings.Contains(workflow, expected) {
			t.Errorf("tests workflow does not contain %q", expected)
		}
	}
	for _, forbidden := range []string{"continue-on-error", "|| true"} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("tests workflow suppresses failures with %q", forbidden)
		}
	}
}

func TestCIAndTestsWorkflowsHaveDistinctResponsibilities(t *testing.T) {
	ciContent, err := os.ReadFile(filepath.Join(repositoryRoot(t), ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	testsContent, err := os.ReadFile(filepath.Join(repositoryRoot(t), ".github", "workflows", "tests.yml"))
	if err != nil {
		t.Fatal(err)
	}

	ci := string(ciContent)
	tests := string(testsContent)
	for _, expected := range []string{"gofmt -l", "go vet ./...", "golangci/golangci-lint-action", "go build ./cmd/argo ./cmd/argod ./cmd/argo-qosd"} {
		if !strings.Contains(ci, expected) {
			t.Errorf("CI workflow does not contain %q", expected)
		}
	}
	if strings.Contains(ci, "go test") {
		t.Fatal("general CI duplicates the dedicated test workflow")
	}
	for _, unrelated := range []string{"gofmt -l", "golangci/golangci-lint-action", "go build ./cmd/argo"} {
		if strings.Contains(tests, unrelated) {
			t.Errorf("tests workflow contains general CI responsibility %q", unrelated)
		}
	}
}

func TestGitignoreExcludesSpecifications(t *testing.T) {
	content, err := os.ReadFile(filepath.Join(repositoryRoot(t), ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}

	entries := strings.Fields(string(content))
	for _, expected := range []string{"/spec/", "/specs/"} {
		found := false
		for _, entry := range entries {
			if entry == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf(".gitignore does not contain %s", expected)
		}
	}
}

func TestCITriggersPersistentBranches(t *testing.T) {
	content, err := os.ReadFile(filepath.Join(repositoryRoot(t), ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}

	workflow := string(content)
	for _, expected := range []string{"pull_request:", "- dev", "- main"} {
		if !strings.Contains(workflow, expected) {
			t.Errorf("CI workflow does not contain %q", expected)
		}
	}
}

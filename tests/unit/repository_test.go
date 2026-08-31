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

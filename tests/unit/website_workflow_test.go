package unit_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebsiteWorkflowRunsRealValidationOnRelevantChanges(t *testing.T) {
	content, err := os.ReadFile(filepath.Join(repositoryRoot(t), ".github", "workflows", "website.yml"))
	if err != nil {
		t.Fatal(err)
	}
	value := string(content)
	for _, expected := range []string{
		"pull_request:",
		"push:",
		"- dev",
		"- main",
		"- website/**",
		"- assets/branding/**",
		"working-directory: website",
		"run: npm ci",
		"run: npm run check",
		"run: npm run lint",
		"run: npm run format:check",
		"run: npm run build",
	} {
		if !strings.Contains(value, expected) {
			t.Errorf("website workflow does not contain %q", expected)
		}
	}
}

func TestWebsiteWorkflowHasBoundedReadOnlyFailureSemantics(t *testing.T) {
	content, err := os.ReadFile(filepath.Join(repositoryRoot(t), ".github", "workflows", "website.yml"))
	if err != nil {
		t.Fatal(err)
	}
	value := string(content)
	for _, expected := range []string{
		"cancel-in-progress: true",
		"contents: read",
		"timeout-minutes: 15",
		"persist-credentials: false",
		"cache-dependency-path: website/package-lock.json",
	} {
		if !strings.Contains(value, expected) {
			t.Errorf("website workflow security contract does not contain %q", expected)
		}
	}
	for _, forbidden := range []string{
		"continue-on-error",
		"vercel deploy",
		"pnpm",
		"yarn",
		"pull_request_target",
	} {
		if strings.Contains(value, forbidden) {
			t.Errorf("website workflow contains forbidden behavior %q", forbidden)
		}
	}
}

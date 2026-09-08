package unit_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type websitePackage struct {
	Private      bool              `json:"private"`
	Type         string            `json:"type"`
	Scripts      map[string]string `json:"scripts"`
	Dependencies map[string]string `json:"dependencies"`
}

func TestWebsiteFoundation(t *testing.T) {
	root := repositoryRoot(t)
	required := []string{
		"website/package.json",
		"website/package-lock.json",
		"website/astro.config.mjs",
		"website/tsconfig.json",
		"website/scripts/sync-branding.mjs",
		"website/src/data/project.ts",
		"website/src/layouts/SiteLayout.astro",
		"website/src/pages/index.astro",
		"website/src/styles/tokens.css",
	}
	for _, name := range required {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Errorf("required website file %s: %v", name, err)
		}
	}
}

func TestWebsiteUsesStaticAstroAndRealValidation(t *testing.T) {
	root := repositoryRoot(t)
	content, err := os.ReadFile(filepath.Join(root, "website", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest websitePackage
	if err := json.Unmarshal(content, &manifest); err != nil {
		t.Fatal(err)
	}
	if !manifest.Private || manifest.Type != "module" || manifest.Dependencies["astro"] == "" {
		t.Fatal("website must be a private Astro ES module")
	}
	for _, script := range []string{"sync:branding", "dev", "check", "lint", "format:check", "build"} {
		if strings.TrimSpace(manifest.Scripts[script]) == "" {
			t.Errorf("website package does not define %s", script)
		}
	}
	for name, command := range manifest.Scripts {
		if strings.Contains(command, "exit 0") || strings.Contains(command, "|| true") {
			t.Errorf("website script %s suppresses failures", name)
		}
	}
	config, err := os.ReadFile(filepath.Join(root, "website", "astro.config.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), `output: "static"`) || strings.Contains(string(config), "adapter") {
		t.Fatal("Astro configuration must use static output without a runtime adapter")
	}
}

func TestWebsiteUsesOnlyNPM(t *testing.T) {
	root := repositoryRoot(t)
	for _, forbidden := range []string{
		"website/pnpm-lock.yaml",
		"website/yarn.lock",
		"website/bun.lock",
		"website/bun.lockb",
	} {
		if _, err := os.Stat(filepath.Join(root, forbidden)); !os.IsNotExist(err) {
			t.Errorf("forbidden package-manager file exists: %s", forbidden)
		}
	}
}

func TestWebsiteBrandingIsGeneratedFromCanonicalAssets(t *testing.T) {
	root := repositoryRoot(t)
	script, err := os.ReadFile(filepath.Join(root, "website", "scripts", "sync-branding.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	value := string(script)
	for _, expected := range []string{
		`"..", "assets", "branding"`,
		`"logo/argo-logo-mark.svg"`,
		`"logo/argo-logo-horizontal.svg"`,
		`"banner/argo-social-preview.png"`,
	} {
		if !strings.Contains(value, expected) {
			t.Errorf("branding sync does not contain %s", expected)
		}
	}
	ignore, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ignore), "/website/public/branding/") {
		t.Fatal("generated website branding is not ignored")
	}
}

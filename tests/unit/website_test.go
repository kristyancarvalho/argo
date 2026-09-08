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

func TestWebsiteHomepageCommunicatesRealArgoBehavior(t *testing.T) {
	root := repositoryRoot(t)
	required := []string{
		"website/src/components/Header.astro",
		"website/src/components/Footer.astro",
		"website/src/components/home/Hero.astro",
		"website/src/components/home/NetworkFlow.astro",
		"website/src/components/home/TerminalPreview.astro",
		"website/src/components/home/Features.astro",
		"website/src/components/home/Architecture.astro",
		"website/src/components/home/ProjectStatus.astro",
	}
	for _, name := range required {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Errorf("required homepage component %s: %v", name, err)
		}
	}
	page, err := os.ReadFile(filepath.Join(root, "website", "src", "pages", "index.astro"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`class="skip-link"`, `<Header />`, `<Hero />`, `<TerminalPreview />`, `<Features />`, `<Architecture />`, `<ProjectStatus />`, `<Footer />`} {
		if !strings.Contains(string(page), expected) {
			t.Errorf("homepage does not contain %s", expected)
		}
	}
}

func TestWebsiteTerminalPreviewUsesCurrentCLI(t *testing.T) {
	content, err := os.ReadFile(filepath.Join(repositoryRoot(t), "website", "src", "components", "home", "TerminalPreview.astro"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"argo add https://example.org/linux.iso", "Added a92f4c8e linux.iso (queued)", "argo list", "ID        STATUS        PROGRESS   FILENAME", "argo status", "Traffic policy: balanced"} {
		if !strings.Contains(string(content), expected) {
			t.Errorf("terminal preview does not contain current CLI behavior %q", expected)
		}
	}
}

func TestWebsiteHomepageProvidesResponsiveAndAccessibleNavigation(t *testing.T) {
	header, err := os.ReadFile(filepath.Join(repositoryRoot(t), "website", "src", "components", "Header.astro"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`aria-label="Primary navigation"`, `aria-label="Mobile navigation"`, `<details class="mobile-nav">`, `@media (max-width: 53rem)`} {
		if !strings.Contains(string(header), expected) {
			t.Errorf("homepage navigation does not contain %s", expected)
		}
	}
	flow, err := os.ReadFile(filepath.Join(repositoryRoot(t), "website", "src", "components", "home", "NetworkFlow.astro"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(flow), `aria-hidden="true"`) {
		t.Fatal("decorative network visualization is exposed to assistive technology")
	}
}

func TestStarlightDocumentationPortalHasSubstantivePublicGuides(t *testing.T) {
	root := repositoryRoot(t)
	required := []string{
		"index.mdx",
		"getting-started.mdx",
		"downloads.mdx",
		"configuration.mdx",
		"networking.mdx",
		"architecture.mdx",
		"development.mdx",
		"troubleshooting.mdx",
	}
	for _, name := range required {
		content, err := os.ReadFile(filepath.Join(root, "website", "src", "content", "docs", "docs", name))
		if err != nil {
			t.Errorf("required documentation page %s: %v", name, err)
			continue
		}
		if len(content) < 500 || !strings.HasPrefix(string(content), "---\n") {
			t.Errorf("documentation page %s is not substantive or has no frontmatter", name)
		}
		for _, forbidden := range []string{"AUDIT-REPORT", "audit-evidence"} {
			if strings.Contains(string(content), forbidden) {
				t.Errorf("documentation page %s publishes internal material %s", name, forbidden)
			}
		}
	}
}

func TestStarlightUsesArgoBrandingAndExplicitRoutes(t *testing.T) {
	root := repositoryRoot(t)
	config, err := os.ReadFile(filepath.Join(root, "website", "astro.config.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	value := string(config)
	for _, expected := range []string{
		`from "@astrojs/starlight"`,
		`src: "./public/branding/logo/argo-logo-horizontal.svg"`,
		`customCss: ["./src/styles/starlight.css"]`,
		`link: "/docs/"`,
		`link: "/docs/architecture/"`,
		`link: "/docs/troubleshooting/"`,
	} {
		if !strings.Contains(value, expected) {
			t.Errorf("Starlight config does not contain %s", expected)
		}
	}
	contentConfig, err := os.ReadFile(filepath.Join(root, "website", "src", "content.config.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contentConfig), "docsLoader()") || !strings.Contains(string(contentConfig), "docsSchema()") {
		t.Fatal("Starlight content collection does not use its loader and schema")
	}
}

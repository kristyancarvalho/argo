package unit_test

import (
	"encoding/json"
	"os"
	"os/exec"
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

func TestWebsiteHomepageRefinementContracts(t *testing.T) {
	root := repositoryRoot(t)
	page, err := os.ReadFile(filepath.Join(root, "website", "src", "pages", "index.astro"))
	if err != nil {
		t.Fatal(err)
	}
	pageValue := string(page)
	for _, forbidden := range []string{"page-frame", "Linux-native download control"} {
		if strings.Contains(pageValue, forbidden) {
			t.Errorf("homepage retains removed presentation %q", forbidden)
		}
	}
	for _, expected := range []string{"data-reveal", "IntersectionObserver", "prefers-reduced-motion"} {
		if !strings.Contains(pageValue, expected) {
			t.Errorf("homepage refinement does not contain %q", expected)
		}
	}

	hero, err := os.ReadFile(filepath.Join(root, "website", "src", "components", "home", "Hero.astro"))
	if err != nil {
		t.Fatal(err)
	}
	heroValue := string(hero)
	if strings.Contains(heroValue, "hero-cards") || strings.Contains(heroValue, "Linux-native download control") {
		t.Fatal("hero retains the removed eyebrow or three-card strip")
	}
	for _, expected := range []string{`class="control-rail"`, "Control plane", `name="network"`, `name="terminal"`} {
		if !strings.Contains(heroValue, expected) {
			t.Errorf("hero control rail does not contain %q", expected)
		}
	}

	architecture, err := os.ReadFile(filepath.Join(root, "website", "src", "components", "home", "Architecture.astro"))
	if err != nil {
		t.Fatal(err)
	}
	architectureValue := string(architecture)
	if !strings.Contains(architectureValue, ".connector {") || !strings.Contains(architectureValue, "grid-column: 1 / -1") {
		t.Fatal("architecture IPC connector is not centered across the complete map grid")
	}
	if _, err := os.Stat(filepath.Join(root, "website", "src", "components", "Icon.astro")); err != nil {
		t.Fatalf("local SVG icon system: %v", err)
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

func TestWebsiteChangelogCoversPublishedGitTags(t *testing.T) {
	root := repositoryRoot(t)
	files, err := filepath.Glob(filepath.Join(root, "website", "src", "content", "changelog", "v*.md"))
	if err != nil {
		t.Fatal(err)
	}
	entries := make(map[string]bool, len(files))
	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		version := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		value := string(content)
		for _, expected := range []string{"title:", "version: " + version, "date:", "description:"} {
			if !strings.Contains(value, expected) {
				t.Errorf("changelog %s does not contain %q", version, expected)
			}
		}
		entries[version] = true
	}

	command := exec.Command("git", "tag", "--list", "v*")
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		t.Skipf("git tags unavailable: %v", err)
	}
	for _, version := range strings.Fields(string(output)) {
		if !entries[version] {
			t.Errorf("published tag %s has no changelog entry", version)
		}
	}
}

func TestWebsiteChangelogAndMetadataRoutesExist(t *testing.T) {
	root := repositoryRoot(t)
	for _, name := range []string{
		"website/src/pages/changelog/index.astro",
		"website/src/pages/changelog/[version].astro",
		"website/src/layouts/ReleaseLayout.astro",
		"website/src/content/docs/404.mdx",
		"website/public/robots.txt",
	} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Errorf("required changelog or metadata file %s: %v", name, err)
		}
	}
	layout, err := os.ReadFile(filepath.Join(root, "website", "src", "layouts", "SiteLayout.astro"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"og:title", "og:description", "og:image", "twitter:card", `rel="canonical"`} {
		if !strings.Contains(string(layout), expected) {
			t.Errorf("site metadata layout does not contain %s", expected)
		}
	}
	config, err := os.ReadFile(filepath.Join(root, "website", "astro.config.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), "process.env.PUBLIC_SITE_URL") || strings.Contains(string(config), "vercel.app") {
		t.Fatal("canonical site URL must be configurable without an invented deployment domain")
	}
}

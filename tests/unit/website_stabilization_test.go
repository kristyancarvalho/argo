package unit_test

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestWebsiteBuildValidatorCoversRoutesAssetsAndInternalMaterial(t *testing.T) {
	root := repositoryRoot(t)
	script, err := os.ReadFile(filepath.Join(root, "website", "scripts", "validate-build.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	value := string(script)
	for _, expected := range []string{
		"docs/getting-started/index.html",
		"changelog/v0.8.0/index.html",
		"branding/banner/argo-social-preview.png",
		"unresolved local target",
		"unresolved fragment",
		"/specs/",
		"AUDIT-REPORT",
		"audit-evidence/",
	} {
		if !strings.Contains(value, expected) {
			t.Errorf("website build validator does not contain %q", expected)
		}
	}
	manifestContent, err := os.ReadFile(filepath.Join(root, "website", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest websitePackage
	if err := json.Unmarshal(manifestContent, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Scripts["validate:build"] == "" || !strings.Contains(manifest.Scripts["build"], "npm run validate:build") {
		t.Fatal("website build does not run the static-output validator")
	}
}

func TestWebsiteSourcesRemainSemanticAndFrameworkFree(t *testing.T) {
	manifestContent, err := os.ReadFile(filepath.Join(repositoryRoot(t), "website", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest websitePackage
	if err := json.Unmarshal(manifestContent, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, framework := range []string{"react", "vue", "svelte", "solid-js", "three", "gsap"} {
		if manifest.Dependencies[framework] != "" {
			t.Errorf("website has unnecessary runtime framework dependency %s", framework)
		}
	}

	root := filepath.Join(repositoryRoot(t), "website", "src")
	imagePattern := regexp.MustCompile(`(?s)<img\b[^>]*>`)
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		value := string(content)
		for _, directive := range []string{"client:load", "client:idle", "client:visible", "client:media", "client:only"} {
			if strings.Contains(value, directive) {
				t.Errorf("%s uses avoidable client hydration %s", path, directive)
			}
		}
		for _, image := range imagePattern.FindAllString(value, -1) {
			if !strings.Contains(image, "alt=") {
				t.Errorf("%s contains an image without alt text", path)
			}
		}
		for len(value) > 0 {
			r, size := utf8.DecodeRuneInString(value)
			if r >= 0xE000 && r <= 0xF8FF {
				t.Errorf("%s contains a private-use glyph U+%04X", path, r)
			}
			value = value[size:]
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	icon, err := os.ReadFile(filepath.Join(root, "components", "Icon.astro"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(icon), `aria-hidden="true"`) {
		t.Fatal("decorative local icons are exposed to assistive technology")
	}
}

func TestWebsiteCorePaletteMeetsTextContrast(t *testing.T) {
	content, err := os.ReadFile(filepath.Join(repositoryRoot(t), "website", "src", "styles", "tokens.css"))
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`--([a-z0-9-]+):\s*(#[0-9a-fA-F]{6})`)
	colors := make(map[string]string)
	for _, match := range pattern.FindAllStringSubmatch(string(content), -1) {
		colors[match[1]] = match[2]
	}
	pairs := [][2]string{
		{"argo-text", "argo-background"},
		{"argo-text-muted", "argo-background"},
		{"argo-blue-400", "argo-blue-950"},
		{"argo-blue-600", "argo-blue-50"},
		{"argo-blue-700", "argo-blue-50"},
	}
	for _, pair := range pairs {
		ratio := contrastRatio(t, colors[pair[0]], colors[pair[1]])
		if ratio < 4.5 {
			t.Errorf("%s on %s contrast %.2f is below 4.5", pair[0], pair[1], ratio)
		}
	}
}

func contrastRatio(t *testing.T, foreground, background string) float64 {
	t.Helper()
	first := relativeLuminance(t, foreground)
	second := relativeLuminance(t, background)
	return (math.Max(first, second) + 0.05) / (math.Min(first, second) + 0.05)
}

func relativeLuminance(t *testing.T, color string) float64 {
	t.Helper()
	if len(color) != 7 || color[0] != '#' {
		t.Fatalf("invalid color %q", color)
	}
	channels := make([]float64, 3)
	for index := range channels {
		value, err := strconv.ParseUint(color[1+index*2:3+index*2], 16, 8)
		if err != nil {
			t.Fatal(err)
		}
		channel := float64(value) / 255
		if channel <= 0.04045 {
			channels[index] = channel / 12.92
		} else {
			channels[index] = math.Pow((channel+0.055)/1.055, 2.4)
		}
	}
	return channels[0]*0.2126 + channels[1]*0.7152 + channels[2]*0.0722
}

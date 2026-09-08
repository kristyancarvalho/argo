package unit_test

import (
	"encoding/binary"
	"encoding/xml"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func brandingRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test location")
	}
	return filepath.Join(filepath.Dir(filename), "..", "..", "assets", "branding")
}

func TestBrandingSVGAssetsAreSelfContainedAndAccessible(t *testing.T) {
	root := brandingRoot(t)
	required := []string{
		"logo/argo-logo.svg",
		"logo/argo-logo-mark.svg",
		"logo/argo-logo-horizontal.svg",
		"banner/argo-banner.svg",
		"banner/argo-social-preview.svg",
		"previews/argo-logo-dark.svg",
		"widgets/status-early-development.svg",
		"widgets/stage-pre-1-0.svg",
		"widgets/language-go.svg",
		"widgets/platform-linux.svg",
		"widgets/license-gpl3.svg",
	}
	for _, name := range required {
		name := name
		t.Run(name, func(t *testing.T) {
			content, err := os.ReadFile(filepath.Join(root, name))
			if err != nil {
				t.Fatal(err)
			}
			var document struct {
				XMLName xml.Name
				ViewBox string `xml:"viewBox,attr"`
				Title   string `xml:"title"`
				Desc    string `xml:"desc"`
			}
			if err := xml.Unmarshal(content, &document); err != nil {
				t.Fatalf("invalid XML: %v", err)
			}
			if document.XMLName.Local != "svg" || document.ViewBox == "" {
				t.Fatal("asset must be an SVG with a viewBox")
			}
			if strings.TrimSpace(document.Title) == "" || strings.TrimSpace(document.Desc) == "" {
				t.Fatal("asset must provide title and description text")
			}
			decoder := xml.NewDecoder(strings.NewReader(string(content)))
			for {
				token, err := decoder.Token()
				if err != nil {
					break
				}
				start, ok := token.(xml.StartElement)
				if !ok {
					continue
				}
				for _, attribute := range start.Attr {
					if (attribute.Name.Local == "href" || attribute.Name.Local == "src") && strings.Contains(attribute.Value, "://") {
						t.Fatalf("external asset reference %q", attribute.Value)
					}
				}
			}
		})
	}
}

func TestBrandingPNGExportsHaveExpectedDimensions(t *testing.T) {
	root := brandingRoot(t)
	expected := map[string][2]uint32{
		"logo/argo-logo-32.png":          {32, 32},
		"logo/argo-logo-64.png":          {64, 64},
		"logo/argo-logo-128.png":         {128, 128},
		"logo/argo-logo-256.png":         {256, 256},
		"logo/argo-logo-512.png":         {512, 512},
		"logo/argo-logo-1024.png":        {1024, 1024},
		"logo/argo-avatar.png":           {1024, 1024},
		"logo/argo-logo.png":             {720, 600},
		"logo/argo-logo-horizontal.png":  {1040, 320},
		"banner/argo-banner.png":         {1440, 480},
		"banner/argo-social-preview.png": {1280, 640},
		"previews/argo-logo-dark.png":    {1200, 600},
	}
	for name, dimensions := range expected {
		content, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(content) < 24 || string(content[:8]) != "\x89PNG\r\n\x1a\n" {
			t.Fatalf("%s is not a valid PNG header", name)
		}
		width := binary.BigEndian.Uint32(content[16:20])
		height := binary.BigEndian.Uint32(content[20:24])
		if width != dimensions[0] || height != dimensions[1] {
			t.Fatalf("%s is %dx%d, want %dx%d", name, width, height, dimensions[0], dimensions[1])
		}
	}
}

func TestBrandGuideDocumentsCanonicalPaletteAndExports(t *testing.T) {
	content, err := os.ReadFile(filepath.Join(brandingRoot(t), "BRAND.md"))
	if err != nil {
		t.Fatal(err)
	}
	guide := string(content)
	for _, value := range []string{"#071A33", "#0E5AA7", "#1687E8", "#65B8FF", "#D8EEFF", "export.sh"} {
		if !strings.Contains(guide, value) {
			t.Fatalf("brand guide does not contain %q", value)
		}
	}
}

package unit_test

import (
	"encoding/binary"
	"encoding/xml"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
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

func TestBannerDisplayTypographyIsPortableVectorGeometry(t *testing.T) {
	for _, name := range []string{"argo-banner.svg", "argo-social-preview.svg"} {
		content, err := os.ReadFile(filepath.Join(brandingRoot(t), "banner", name))
		if err != nil {
			t.Fatal(err)
		}
		value := string(content)
		if strings.Contains(value, "<text") || strings.Contains(value, "font-family") {
			t.Fatalf("%s depends on locally installed fonts", name)
		}
		if strings.Contains(value, "Control the flow") {
			t.Fatalf("%s repeats the homepage slogan instead of identifying the project", name)
		}
		for _, expected := range []string{"Argo. A traffic-aware download manager for Linux.", "DejaVu Serif", "-glyph-", "<use href="} {
			if !strings.Contains(value, expected) {
				t.Errorf("%s does not contain portable display typography evidence %q", name, expected)
			}
		}
	}
}

func TestBrandingWidgetsProvideTextPadding(t *testing.T) {
	type geometry struct {
		width int
		split int
	}
	widgets := map[string]geometry{
		"status-early-development.svg": {width: 260, split: 104},
		"stage-pre-1-0.svg":            {width: 178, split: 96},
		"language-go.svg":              {width: 174, split: 120},
		"platform-linux.svg":           {width: 190, split: 120},
		"license-gpl3.svg":             {width: 204, split: 108},
	}
	rootPattern := regexp.MustCompile(`<svg[^>]+width="(\d+)"[^>]+height="34"[^>]+viewBox="0 0 (\d+) 34"`)
	textPattern := regexp.MustCompile(`<text x="(\d+)"[^>]*>([^<]+)</text>`)
	for name, expected := range widgets {
		content, err := os.ReadFile(filepath.Join(brandingRoot(t), "widgets", name))
		if err != nil {
			t.Fatal(err)
		}
		svg := string(content)
		root := rootPattern.FindStringSubmatch(svg)
		if len(root) != 3 || root[1] != strconv.Itoa(expected.width) || root[2] != strconv.Itoa(expected.width) {
			t.Fatalf("%s width and viewBox do not match %d", name, expected.width)
		}
		if !strings.Contains(svg, "H"+strconv.Itoa(expected.split)+"V34") {
			t.Fatalf("%s does not use label split %d", name, expected.split)
		}
		texts := textPattern.FindAllStringSubmatch(svg, -1)
		if len(texts) != 2 {
			t.Fatalf("%s has %d text fields", name, len(texts))
		}
		labelX, _ := strconv.Atoi(texts[0][1])
		valueX, _ := strconv.Atoi(texts[1][1])
		labelEnd := labelX + len(texts[0][2])*8
		valueEnd := valueX + len(texts[1][2])*7
		if expected.split-labelEnd < 16 {
			t.Fatalf("%s label padding is less than 16 pixels", name)
		}
		if valueX-expected.split < 12 {
			t.Fatalf("%s value starts without 12 pixels of padding", name)
		}
		if expected.width-valueEnd < 16 {
			t.Fatalf("%s value padding is less than 16 pixels", name)
		}
	}
}

func TestPublicBrandGuideIsAbsent(t *testing.T) {
	_, err := os.Stat(filepath.Join(brandingRoot(t), "BRAND"+".md"))
	if !os.IsNotExist(err) {
		t.Fatalf("public brand guide remains: %v", err)
	}
}

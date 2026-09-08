package unit_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/kristyancarvalho/argo/internal/diagnostic"
)

func TestDiagnosticURLRedactsCredentialsQueriesAndFragments(t *testing.T) {
	if value := diagnostic.URL("https://example.test/file.iso"); value != "https://example.test/file.iso" {
		t.Fatalf("non-sensitive URL changed to %q", value)
	}
	secret := "synthetic-secret"
	rawURL := "https://user:" + secret + "@example.test/file.iso?token=" + secret + "&signature=" + secret + "#" + secret
	redacted := diagnostic.URL(rawURL)
	if strings.Contains(redacted, secret) || redacted != "https://redacted@example.test/file.iso?redacted#redacted" {
		t.Fatalf("URL redaction returned %q", redacted)
	}
	message := diagnostic.Text("request failed for " + rawURL + "): connection reset")
	if strings.Contains(message, secret) || !strings.HasSuffix(message, "): connection reset") {
		t.Fatalf("diagnostic text redaction returned %q", message)
	}
}

func TestDiagnosticErrorPreservesErrorIdentityWithoutExposingURL(t *testing.T) {
	sentinel := errors.New("transport failure")
	wrapped := fmt.Errorf("GET https://user:password@example.test/file?token=secret: %w", sentinel)
	redacted := diagnostic.Error(wrapped)
	if !errors.Is(redacted, sentinel) {
		t.Fatal("redacted error lost wrapped error identity")
	}
	if strings.Contains(redacted.Error(), "password") || strings.Contains(redacted.Error(), "secret") {
		t.Fatalf("redacted error exposed credentials: %q", redacted.Error())
	}
}

func TestDiagnosticDisplayEscapesTerminalControlsAndPreservesUnicode(t *testing.T) {
	unsafe := "ESC\x1b[31m OSC\x1b]0;title\a line\nrow\r tab\t bidi\u202eend café-日本語"
	display := diagnostic.Display(unsafe)
	for _, character := range []rune{'\x1b', '\a', '\r', '\t', '\u202e'} {
		if strings.ContainsRune(display, character) {
			t.Fatalf("display contains control U+%04X: %q", character, display)
		}
	}
	if strings.Contains(display, "line\nrow") || !strings.Contains(display, `line\nrow`) ||
		!strings.Contains(display, "café-日本語") {
		t.Fatalf("display escaping returned %q", display)
	}
	filename := diagnostic.Filename("café\x1b\n\t\u202e-日本語.iso")
	if filename != "café____-日本語.iso" {
		t.Fatalf("safe filename is %q", filename)
	}
}

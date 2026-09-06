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

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/daemon"
	"github.com/kristyancarvalho/argo/internal/downloader"
	"github.com/kristyancarvalho/argo/internal/ipc"
	"github.com/kristyancarvalho/argo/internal/model"
)

type failingURLTransport struct {
	sentinel error
}

func TestDownloaderRedactsNestedTransportErrorsButPreservesStoredSource(t *testing.T) {
	parts := t.TempDir()
	store := openTestStore(t)
	sentinel := errors.New("synthetic transport failure")
	engine, err := downloader.NewWithOptions(store, downloader.Options{
		HTTPClient: &http.Client{Transport: failingURLTransport{sentinel: sentinel}}, PartsDirectory: parts,
	})
	if err != nil {
		t.Fatal(err)
	}
	secretURL := "https://user:audit-password@example.test/file?token=audit-token&signature=audit-signature"
	download := persistedDownload(t, store, secretURL, t.TempDir(), "file")
	err = engine.Download(context.Background(), download)
	if !errors.Is(err, sentinel) {
		t.Fatalf("download error lost transport identity: %v", err)
	}
	for _, secret := range []string{"audit-password", "audit-token", "audit-signature"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("download error exposed %q: %v", secret, err)
		}
	}
	persisted, err := store.Download(context.Background(), download.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.URL != secretURL {
		t.Fatalf("internal source was mutated to %q", persisted.URL)
	}
	if strings.Contains(persisted.Error, "audit-password") || strings.Contains(persisted.Error, "audit-token") {
		t.Fatalf("persisted error exposed source credentials: %q", persisted.Error)
	}
}

func TestDaemonRedactsExistingHistoryAtIPCBoundary(t *testing.T) {
	store := openTestStore(t)
	identifier, err := model.NewDownloadID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	secretURL := "https://user:history-password@example.test/file?token=history-token"
	download := model.Download{
		ID: identifier, URL: secretURL, Destination: t.TempDir(), Filename: "file", TotalSize: -1,
		Status: model.StatusFailed, Priority: model.PriorityNormal, CreatedAt: now, UpdatedAt: now,
		Error: "GET " + secretURL + ": failed",
	}
	if err := store.CreateDownload(context.Background(), download); err != nil {
		t.Fatal(err)
	}
	service, err := daemon.NewService(context.Background(), store, newControlledDownloadEngine(store))
	if err != nil {
		t.Fatal(err)
	}
	defer closeSchedulerService(t, service)
	result, err := service.Handle(context.Background(), ipc.Request{Operation: ipc.OperationList})
	if err != nil {
		t.Fatal(err)
	}
	response := result.(ipc.ListResponse)
	if len(response.Downloads) != 1 {
		t.Fatalf("history response has %d entries", len(response.Downloads))
	}
	encoded := response.Downloads[0].URL + " " + response.Downloads[0].Error
	if strings.Contains(encoded, "history-password") || strings.Contains(encoded, "history-token") ||
		!strings.Contains(encoded, "https://redacted@example.test/file?redacted") {
		t.Fatalf("IPC history exposed secrets: %q", encoded)
	}
}

func (transport failingURLTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("nested request failure for %s: %w", request.URL.String(), transport.sentinel)
}

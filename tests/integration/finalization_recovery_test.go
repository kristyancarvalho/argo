package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/downloader"
	"github.com/kristyancarvalho/argo/internal/model"
)

type finalizationFixture struct {
	Candidate string `json:"candidate"`
	Staging   string `json:"staging"`
	Size      int64  `json:"size"`
	Ready     bool   `json:"ready"`
}

func TestFinalizationFaultInjectionRecoversEveryDurabilityWindow(t *testing.T) {
	stages := []string{"created", "copying", "synced", "published"}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			parts, err := os.MkdirTemp("/dev/shm", "argo-finalization-")
			if err != nil {
				t.Skipf("cross-filesystem state directory unavailable: %v", err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(parts) })
			destination := t.TempDir()
			partsInfo, err := os.Stat(parts)
			if err != nil {
				t.Fatal(err)
			}
			destinationInfo, err := os.Stat(destination)
			if err != nil {
				t.Fatal(err)
			}
			if partsInfo.Sys().(*syscall.Stat_t).Dev == destinationInfo.Sys().(*syscall.Stat_t).Dev {
				t.Skip("cross-filesystem test directories share a filesystem")
			}
			payload := []byte(strings.Repeat("argo-finalization-payload", 4096))
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("Content-Length", strconv.Itoa(len(payload)))
				_, _ = response.Write(payload)
			}))
			defer server.Close()
			store := openTestStore(t)
			download := persistedDownload(t, store, server.URL+"/artifact.bin", destination, "artifact.bin")
			injected := errors.New("injected abrupt termination")
			engine, err := downloader.NewWithOptions(store, downloader.Options{
				PartsDirectory: parts,
				FinalizationCheckpoint: func(current string) error {
					if current == stage {
						return injected
					}
					return nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := engine.Download(context.Background(), download); !errors.Is(err, injected) {
				t.Fatalf("checkpoint %s returned %v", stage, err)
			}
			finalPath := filepath.Join(destination, download.Filename)
			if stage == "published" {
				content, err := os.ReadFile(finalPath)
				if err != nil || string(content) != string(payload) {
					t.Fatalf("published checkpoint content is invalid: %d bytes, %v", len(content), err)
				}
			} else if _, err := os.Stat(finalPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("checkpoint %s exposed final destination: %v", stage, err)
			}
			persisted, err := store.Download(context.Background(), download.ID)
			if err != nil {
				t.Fatal(err)
			}
			if persisted.Status != model.StatusDownloading {
				t.Fatalf("checkpoint %s persisted status %s", stage, persisted.Status)
			}
			recovered := finalizationRecoveryEngine(t, store, parts)
			if err := recovered.RecoverFinalizations(context.Background(), []model.Download{persisted}); err != nil {
				t.Fatal(err)
			}
			assertRecoveredFinalization(t, store, persisted, payload)
			entries, err := os.ReadDir(destination)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".finalizing") {
					t.Errorf("staging artifact remains after %s recovery: %s", stage, entry.Name())
				}
			}
		})
	}
}

func TestFinalizationRecoveryRestartsInterruptedStaging(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "after create"},
		{name: "mid copy", content: "complete-payload"[:8]},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parts, destination, store, download, record := finalizationRecoveryFixture(t, "complete-payload", false)
			staging := filepath.Join(destination, record.Staging)
			if err := os.WriteFile(staging, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			engine := finalizationRecoveryEngine(t, store, parts)
			if err := engine.RecoverFinalizations(context.Background(), []model.Download{download}); err != nil {
				t.Fatal(err)
			}
			assertRecoveredFinalization(t, store, download, []byte("complete-payload"))
			assertFinalizationArtifactsRemoved(t, parts, destination, download.ID, record.Staging)
		})
	}
}

func TestFinalizationRecoveryPublishesSyncedStaging(t *testing.T) {
	parts, destination, store, download, record := finalizationRecoveryFixture(t, "synced-payload", true)
	if err := os.WriteFile(filepath.Join(destination, record.Staging), []byte("synced-payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	engine := finalizationRecoveryEngine(t, store, parts)
	if err := engine.RecoverFinalizations(context.Background(), []model.Download{download}); err != nil {
		t.Fatal(err)
	}
	assertRecoveredFinalization(t, store, download, []byte("synced-payload"))
	assertFinalizationArtifactsRemoved(t, parts, destination, download.ID, record.Staging)
}

func TestFinalizationRecoveryRecognizesPublishedFileBeforeDatabaseCompletion(t *testing.T) {
	parts, destination, store, download, record := finalizationRecoveryFixture(t, "published-payload", true)
	staging := filepath.Join(destination, record.Staging)
	finalPath := filepath.Join(destination, record.Candidate)
	if err := os.WriteFile(staging, []byte("published-payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(staging, finalPath); err != nil {
		t.Fatal(err)
	}
	engine := finalizationRecoveryEngine(t, store, parts)
	if err := engine.RecoverFinalizations(context.Background(), []model.Download{download}); err != nil {
		t.Fatal(err)
	}
	assertRecoveredFinalization(t, store, download, []byte("published-payload"))
	assertFinalizationArtifactsRemoved(t, parts, destination, download.ID, record.Staging)
}

func TestFinalizationRecoveryPreservesExistingDestination(t *testing.T) {
	parts, destination, store, download, record := finalizationRecoveryFixture(t, "new-payload", true)
	if err := os.WriteFile(filepath.Join(destination, record.Staging), []byte("new-payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(destination, record.Candidate)
	if err := os.WriteFile(original, []byte("existing-payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	engine := finalizationRecoveryEngine(t, store, parts)
	if err := engine.RecoverFinalizations(context.Background(), []model.Download{download}); err != nil {
		t.Fatal(err)
	}
	existing, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	if string(existing) != "existing-payload" {
		t.Fatalf("existing destination changed to %q", existing)
	}
	persisted, err := store.Download(context.Background(), download.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Filename != "artifact (1).bin" {
		t.Fatalf("recovered filename is %q", persisted.Filename)
	}
	created, err := os.ReadFile(filepath.Join(destination, persisted.Filename))
	if err != nil {
		t.Fatal(err)
	}
	if string(created) != "new-payload" {
		t.Fatalf("recovered content is %q", created)
	}
}

func TestFinalizationRecoveryRejectsInvalidSyncedStaging(t *testing.T) {
	parts, destination, store, download, record := finalizationRecoveryFixture(t, "expected-payload", true)
	if err := os.WriteFile(filepath.Join(destination, record.Staging), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	engine := finalizationRecoveryEngine(t, store, parts)
	err := engine.RecoverFinalizations(context.Background(), []model.Download{download})
	if err == nil || !strings.Contains(err.Error(), "expected") {
		t.Fatalf("invalid staging returned %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, record.Candidate)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid staging was published: %v", err)
	}
}

func TestCompletedFinalizationRecoveryCleansOwnedStateOnly(t *testing.T) {
	parts, destination, store, download, record := finalizationRecoveryFixture(t, "complete", true)
	staging := filepath.Join(destination, record.Staging)
	finalPath := filepath.Join(destination, record.Candidate)
	if err := os.WriteFile(staging, []byte("complete"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(staging, finalPath); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateDownloadStatus(context.Background(), download.ID, model.StatusCompleted, time.Now().UTC(), ""); err != nil {
		t.Fatal(err)
	}
	download.Status = model.StatusCompleted
	engine := finalizationRecoveryEngine(t, store, parts)
	if err := engine.RecoverFinalizations(context.Background(), []model.Download{download}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(finalPath)
	if err != nil || string(content) != "complete" {
		t.Fatalf("completed file changed: %q, %v", content, err)
	}
	assertFinalizationArtifactsRemoved(t, parts, destination, download.ID, record.Staging)
}

func finalizationRecoveryFixture(t *testing.T, payload string, ready bool) (string, string, interfaceStore, model.Download, finalizationFixture) {
	t.Helper()
	parts := filepath.Join(t.TempDir(), "parts")
	destination := t.TempDir()
	store := openTestStore(t)
	download := persistedDownload(t, store, "https://example.test/artifact.bin", destination, "artifact.bin")
	now := time.Now().UTC()
	if err := store.UpdateDownloadStatus(context.Background(), download.ID, model.StatusDownloading, now, ""); err != nil {
		t.Fatal(err)
	}
	download.Status = model.StatusDownloading
	download.TotalSize = int64(len(payload))
	download.DownloadedBytes = int64(len(payload))
	if err := store.UpdateRemoteMetadata(context.Background(), download.ID, int64(len(payload)), true, "etag", "", now); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateDownloadProgress(context.Background(), download.ID, int64(len(payload)), now); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(parts, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parts, download.ID.String()+".part"), []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	record := finalizationFixture{
		Candidate: download.Filename,
		Staging:   ".argo-" + download.ID.String() + "-0123456789abcdef0123456789abcdef.finalizing",
		Size:      int64(len(payload)),
		Ready:     ready,
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parts, download.ID.String()+".finalize"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	return parts, destination, store, download, record
}

type interfaceStore = interface {
	downloader.Store
	Download(context.Context, model.DownloadID) (model.Download, error)
}

func finalizationRecoveryEngine(t *testing.T, store downloader.Store, parts string) *downloader.Engine {
	t.Helper()
	engine, err := downloader.NewWithOptions(store, downloader.Options{PartsDirectory: parts})
	if err != nil {
		t.Fatal(err)
	}

	return engine
}

func assertRecoveredFinalization(t *testing.T, store interfaceStore, download model.Download, payload []byte) {
	t.Helper()
	persisted, err := store.Download(context.Background(), download.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.StatusCompleted {
		t.Fatalf("recovered status is %s", persisted.Status)
	}
	content, err := os.ReadFile(filepath.Join(download.Destination, persisted.Filename))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(payload) {
		t.Fatalf("recovered content is %q", content)
	}
}

func assertFinalizationArtifactsRemoved(t *testing.T, parts string, destination string, identifier model.DownloadID, staging string) {
	t.Helper()
	paths := []string{
		filepath.Join(parts, identifier.String()+".part"),
		filepath.Join(parts, identifier.String()+".finalize"),
		filepath.Join(destination, staging),
	}
	for _, path := range paths {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("finalization artifact %s remains: %v", path, err)
		}
	}
}

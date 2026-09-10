package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/downloader"
	"github.com/kristyancarvalho/argo/internal/model"
	"github.com/kristyancarvalho/argo/internal/storage"
)

func TestChecksumSerialDownloadAndHistoricalVerification(t *testing.T) {
	parts := isolateDownloadState(t)
	payload := []byte(strings.Repeat("verified serial payload", 4096))
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write(payload)
	}))
	defer server.Close()
	store := openTestStore(t)
	download := persistedChecksumDownload(t, store, server.URL+"/serial.bin", t.TempDir(), "serial.bin", checksumFor(payload))
	engine := downloader.New(store)
	if err := engine.Download(context.Background(), download); err != nil {
		t.Fatal(err)
	}
	persisted, err := store.Download(context.Background(), download.ID)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := engine.Verify(context.Background(), persisted)
	if err != nil {
		t.Fatal(err)
	}
	if actual != download.Checksum {
		t.Fatalf("verified checksum is %q, expected %q", actual, download.Checksum)
	}
	if _, err := os.Stat(filepath.Join(parts, download.ID.String()+".part")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("verified partial remains: %v", err)
	}
	if err := os.WriteFile(filepath.Join(download.Destination, download.Filename), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = engine.Verify(context.Background(), persisted)
	var mismatch downloader.ChecksumMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("tampered verification returned %T, expected ChecksumMismatchError", err)
	}
}

func TestChecksumMismatchNeverPublishesFinalFile(t *testing.T) {
	parts := isolateDownloadState(t)
	payload := []byte(strings.Repeat("mismatched payload", 4096))
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write(payload)
	}))
	defer server.Close()
	destination := t.TempDir()
	store := openTestStore(t)
	download := persistedChecksumDownload(t, store, server.URL+"/mismatch.bin", destination, "mismatch.bin", checksumFor([]byte("different")))
	err := downloader.New(store).Download(context.Background(), download)
	var mismatch downloader.ChecksumMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("mismatch returned %T, expected ChecksumMismatchError", err)
	}
	persisted, err := store.Download(context.Background(), download.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.StatusFailed || !strings.Contains(persisted.Error, "checksum mismatch") {
		t.Fatalf("mismatch state is %+v", persisted)
	}
	if _, err := os.Stat(filepath.Join(destination, download.Filename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mismatched final file was published: %v", err)
	}
	if _, err := os.Stat(filepath.Join(parts, download.ID.String()+".part")); err != nil {
		t.Fatalf("mismatched partial was not retained: %v", err)
	}
}

func TestChecksumParallelDownload(t *testing.T) {
	payload := makePayload(4096)
	server := parallelServer(t, payload, func(response http.ResponseWriter, _ *http.Request, start, end int64) {
		writeRange(response, payload, start, end)
	})
	store := openTestStore(t)
	download := persistedChecksumDownload(t, store, server.URL+"/parallel.bin", t.TempDir(), "parallel.bin", checksumFor(payload))
	if err := parallelEngine(t, store, nil).Download(context.Background(), download); err != nil {
		t.Fatal(err)
	}
	persisted, err := store.Download(context.Background(), download.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.StatusCompleted || persisted.Checksum != download.Checksum {
		t.Fatalf("parallel checksum state is %+v", persisted)
	}
}

func TestChecksumResumeValidatesCompletePartial(t *testing.T) {
	parts := isolateDownloadState(t)
	payload := makePayload(64 * 1024)
	server, _ := rangeFixtureServer(t, payload)
	store := openTestStore(t)
	download := persistedChecksumDownload(t, store, server.URL+"/resume.bin", t.TempDir(), "resume.bin", checksumFor(payload))
	now := time.Now().UTC()
	if err := store.UpdateDownloadStatus(context.Background(), download.ID, model.StatusDownloading, now, ""); err != nil {
		t.Fatal(err)
	}
	partial := payload[:16*1024]
	if err := os.MkdirAll(parts, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parts, download.ID.String()+".part"), partial, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateRemoteMetadata(context.Background(), download.ID, int64(len(payload)), true, `"fixture-v1"`, "", now); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateDownloadProgress(context.Background(), download.ID, int64(len(partial)), now); err != nil {
		t.Fatal(err)
	}
	download, err := store.Download(context.Background(), download.ID)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := downloader.NewWithOptions(store, downloader.Options{MaximumChunks: 1, PartsDirectory: parts})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Download(context.Background(), download); err != nil {
		t.Fatal(err)
	}
	assertCompletedDownload(t, store, download, payload)
}

func TestChecksumFinalizationRecoveryCompletesVerifiedTransfer(t *testing.T) {
	parts := isolateDownloadState(t)
	payload := []byte(strings.Repeat("verified recovery", 4096))
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write(payload)
	}))
	defer server.Close()
	store := openTestStore(t)
	download := persistedChecksumDownload(t, store, server.URL+"/recover.bin", t.TempDir(), "recover.bin", checksumFor(payload))
	injected := errors.New("stop after publish")
	engine, err := downloader.NewWithOptions(store, downloader.Options{
		PartsDirectory: parts,
		FinalizationCheckpoint: func(stage string) error {
			if stage == "published" {
				return injected
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Download(context.Background(), download); !errors.Is(err, injected) {
		t.Fatalf("interrupted finalization returned %v", err)
	}
	persisted, err := store.Download(context.Background(), download.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.StatusVerifying {
		t.Fatalf("interrupted verified transfer has status %s", persisted.Status)
	}
	recovered, err := downloader.NewWithOptions(store, downloader.Options{PartsDirectory: parts})
	if err != nil {
		t.Fatal(err)
	}
	if err := recovered.RecoverFinalizations(context.Background(), []model.Download{persisted}); err != nil {
		t.Fatal(err)
	}
	persisted, err = store.Download(context.Background(), download.ID)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(download.Destination, download.Filename))
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.StatusCompleted || string(content) != string(payload) {
		t.Fatalf("recovered checksum transfer is %+v with %d bytes", persisted, len(content))
	}
	if _, err := os.Stat(filepath.Join(parts, download.ID.String()+".part")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovered partial remains: %v", err)
	}
}

func TestChecksumRetryPreservesIntegrityMetadata(t *testing.T) {
	isolateDownloadState(t)
	payload := makePayload(64 * 1024)
	server, _ := rangeFixtureServer(t, payload)
	store := openTestStore(t)
	running := startDownloadDaemon(t, store)
	defer stopDownloadDaemon(t, running)
	added, err := running.client.AddWithChecksum(context.Background(), server.URL+"/retry-checksum.bin", t.TempDir(), checksumFor(payload))
	if err != nil {
		t.Fatal(err)
	}
	originalID, err := model.ParseDownloadID(added.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitForDownload(t, store, originalID, func(download model.Download) bool { return download.Status == model.StatusCompleted })
	retried, err := running.client.Retry(context.Background(), added.ID)
	if err != nil {
		t.Fatal(err)
	}
	retriedID, err := model.ParseDownloadID(retried.ID)
	if err != nil {
		t.Fatal(err)
	}
	retriedDownload := waitForDownload(t, store, retriedID, func(download model.Download) bool {
		return download.Status == model.StatusCompleted
	})
	if retriedDownload.Checksum != checksumFor(payload) || retriedID == originalID {
		t.Fatalf("retried checksum transfer is %+v", retriedDownload)
	}
	verified, err := running.client.Verify(context.Background(), retried.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !verified.Matched || verified.Checksum != checksumFor(payload) {
		t.Fatalf("verify response is %+v", verified)
	}
}

func persistedChecksumDownload(
	t *testing.T,
	store *storage.Store,
	rawURL string,
	destination string,
	filename string,
	checksum string,
) model.Download {
	t.Helper()
	identifier, err := model.NewDownloadID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	download := model.Download{
		ID: identifier, URL: rawURL, Destination: destination, Filename: filename,
		TotalSize: -1, Status: model.StatusQueued, Priority: model.PriorityNormal,
		CreatedAt: now, UpdatedAt: now, Checksum: checksum,
	}
	if err := store.CreateDownload(context.Background(), download); err != nil {
		t.Fatal(err)
	}

	return download
}

func checksumFor(payload []byte) string {
	digest := sha256.Sum256(payload)

	return "sha256:" + hex.EncodeToString(digest[:])
}

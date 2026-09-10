package integration_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/downloader"
	"github.com/kristyancarvalho/argo/internal/model"
	"github.com/kristyancarvalho/argo/internal/storage"
)

type finalizationFailureStore struct {
	*storage.Store
	progress      func() error
	failCompleted bool
}

func (store *finalizationFailureStore) UpdateDownloadProgress(ctx context.Context, id model.DownloadID, count int64, at time.Time) error {
	if err := store.Store.UpdateDownloadProgress(ctx, id, count, at); err != nil {
		return err
	}
	if store.progress != nil && count > 0 {
		return store.progress()
	}
	return nil
}

func (store *finalizationFailureStore) UpdateChunkProgress(ctx context.Context, id model.DownloadID, index int, count int64, at time.Time) error {
	if err := store.Store.UpdateChunkProgress(ctx, id, index, count, at); err != nil {
		return err
	}
	if store.progress != nil && count > 0 {
		return store.progress()
	}
	return nil
}

func (store *finalizationFailureStore) UpdateDownloadStatus(ctx context.Context, id model.DownloadID, status model.Status, at time.Time, message string) error {
	if status == model.StatusCompleted && store.failCompleted {
		return errors.New("completion transaction unavailable")
	}
	return store.Store.UpdateDownloadStatus(ctx, id, status, at, message)
}

func TestFinalizationFailureIsRecoverableWithoutDuplicatePublication(t *testing.T) {
	for _, chunks := range []int{1, 4} {
		for _, fault := range []string{"destination", "completion transaction"} {
			t.Run(fmt.Sprintf("%d/%s", chunks, fault), func(t *testing.T) {
				if fault == "destination" && os.Geteuid() == 0 {
					t.Skip("permission failure requires an unprivileged process")
				}
				ctx := context.Background()
				payload := []byte("complete transfer payload")
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					writer.Header().Set("ETag", `"stable"`)
					http.ServeContent(writer, request, "file.bin", time.Time{}, bytes.NewReader(payload))
				}))
				defer server.Close()
				store := openTestStore(t)
				destination, parts := t.TempDir(), t.TempDir()
				t.Cleanup(func() { _ = os.Chmod(destination, 0o700) })
				download := persistedDownload(t, store, server.URL, destination, "file.bin")
				faultStore := &finalizationFailureStore{Store: store, failCompleted: fault == "completion transaction"}
				var once sync.Once
				var permissionError error
				if fault == "destination" {
					faultStore.progress = func() error {
						once.Do(func() { permissionError = os.Chmod(destination, 0o500) })
						return permissionError
					}
				}
				engine, err := downloader.NewWithOptions(faultStore, downloader.Options{
					PartsDirectory: parts, MaximumChunks: chunks, MinimumChunkSize: 1,
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := engine.Download(ctx, download); err == nil {
					t.Fatal("finalization fault was ignored")
				}
				failed, err := store.Download(ctx, download.ID)
				if err != nil || failed.Status != model.StatusFailed || failed.Error == "" {
					t.Fatalf("failed finalization remains active: %+v, %v", failed, err)
				}
				if _, err := os.Stat(filepath.Join(parts, download.ID.String()+".finalize")); err != nil {
					t.Fatalf("recovery journal lost: %v", err)
				}
				if fault == "destination" {
					if _, err := os.Stat(filepath.Join(destination, download.Filename)); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("failed placement exposed destination: %v", err)
					}
				}
				if err := os.Chmod(destination, 0o700); err != nil {
					t.Fatal(err)
				}
				server.Close()
				recovered := finalizationRecoveryEngine(t, store, parts)
				if fault == "destination" {
					if err := store.UpdateDownloadStatus(ctx, download.ID, model.StatusQueued, time.Now(), ""); err != nil {
						t.Fatal(err)
					}
					failed.Status = model.StatusQueued
					err = recovered.Download(ctx, failed)
				} else {
					err = recovered.RecoverFinalizations(ctx, []model.Download{failed})
				}
				if err != nil {
					t.Fatal(err)
				}
				assertCompletedDownload(t, store, download, payload)
				entries, err := os.ReadDir(destination)
				if err != nil || len(entries) != 1 || entries[0].Name() != "file.bin" {
					t.Fatalf("duplicate publication or stale staging: %v, %v", entries, err)
				}
			})
		}
	}
}

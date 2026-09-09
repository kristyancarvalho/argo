package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/daemon"
	"github.com/kristyancarvalho/argo/internal/ipc"
	"github.com/kristyancarvalho/argo/internal/model"
	"github.com/kristyancarvalho/argo/internal/storage"
)

type historyEngine struct {
	store   *storage.Store
	parts   string
	started chan model.DownloadID
}

func (engine *historyEngine) Download(ctx context.Context, download model.Download) error {
	if err := engine.store.UpdateDownloadStatus(ctx, download.ID, model.StatusDownloading, time.Now().UTC(), ""); err != nil {
		return err
	}
	engine.started <- download.ID
	<-ctx.Done()

	return ctx.Err()
}

func (engine *historyEngine) RemovePartial(identifier model.DownloadID) error {
	err := os.Remove(filepath.Join(engine.parts, identifier.String()+".part"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	return nil
}

func TestRemoveCompletedHistoryPreservesFinalFile(t *testing.T) {
	parts := isolateDownloadState(t)
	store := openTestStore(t)
	engine := &historyEngine{store: store, parts: parts, started: make(chan model.DownloadID, 1)}
	service := newHistoryService(t, store, engine)
	destination := t.TempDir()
	download := createHistoryDownload(t, store, model.StatusCompleted, destination, "complete.bin")
	finalPath := filepath.Join(destination, download.Filename)
	if err := os.WriteFile(finalPath, []byte("completed"), 0o600); err != nil {
		t.Fatal(err)
	}
	response, err := removeHistory(t, service, download.ID)
	if err != nil {
		t.Fatal(err)
	}
	if response.ID != download.ID.String() || response.Status != "removed" {
		t.Fatalf("unexpected remove response: %+v", response)
	}
	if _, err := store.Download(context.Background(), download.ID); !errors.Is(err, storage.ErrDownloadNotFound) {
		t.Fatalf("removed history remains: %v", err)
	}
	if content, err := os.ReadFile(finalPath); err != nil || string(content) != "completed" {
		t.Fatalf("completed file changed: %q, %v", content, err)
	}
}

func TestRemoveCanceledHistoryCleansPartial(t *testing.T) {
	parts := isolateDownloadState(t)
	store := openTestStore(t)
	engine := &historyEngine{store: store, parts: parts, started: make(chan model.DownloadID, 1)}
	service := newHistoryService(t, store, engine)
	download := createHistoryDownload(t, store, model.StatusCanceled, t.TempDir(), "canceled.bin")
	partial := createHistoryPartial(t, parts, download.ID)
	if _, err := removeHistory(t, service, download.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(partial); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled partial remains: %v", err)
	}
}

func TestRemoveActiveDownloadIsRejected(t *testing.T) {
	parts := isolateDownloadState(t)
	store := openTestStore(t)
	download := createHistoryDownload(t, store, model.StatusQueued, t.TempDir(), "active.bin")
	engine := &historyEngine{store: store, parts: parts, started: make(chan model.DownloadID, 1)}
	service := newHistoryService(t, store, engine)
	select {
	case <-engine.started:
	case <-time.After(2 * time.Second):
		t.Fatal("active download did not start")
	}
	_, err := removeHistory(t, service, download.ID)
	var actionError daemon.InvalidDownloadActionError
	if !errors.As(err, &actionError) || actionError.Status != model.StatusDownloading {
		t.Fatalf("active remove returned %v", err)
	}
	if _, err := store.Download(context.Background(), download.ID); err != nil {
		t.Fatalf("active record was removed: %v", err)
	}
}

func TestClearHistoryPreservesActiveAndPausedDownloads(t *testing.T) {
	parts := isolateDownloadState(t)
	store := openTestStore(t)
	destination := t.TempDir()
	completed := createHistoryDownload(t, store, model.StatusCompleted, destination, "completed.bin")
	failed := createHistoryDownload(t, store, model.StatusFailed, destination, "failed.bin")
	canceled := createHistoryDownload(t, store, model.StatusCanceled, destination, "canceled.bin")
	paused := createHistoryDownload(t, store, model.StatusPaused, destination, "paused.bin")
	active := createHistoryDownload(t, store, model.StatusQueued, destination, "active.bin")
	finalPath := filepath.Join(destination, completed.Filename)
	if err := os.WriteFile(finalPath, []byte("final"), 0o600); err != nil {
		t.Fatal(err)
	}
	failedPartial := createHistoryPartial(t, parts, failed.ID)
	canceledPartial := createHistoryPartial(t, parts, canceled.ID)
	engine := &historyEngine{store: store, parts: parts, started: make(chan model.DownloadID, 1)}
	service := newHistoryService(t, store, engine)
	select {
	case <-engine.started:
	case <-time.After(2 * time.Second):
		t.Fatal("queued download did not start")
	}
	result, err := service.Handle(context.Background(), ipc.Request{Operation: ipc.OperationClear})
	if err != nil {
		t.Fatal(err)
	}
	response, ok := result.(ipc.ClearResponse)
	if !ok || response.Removed != 3 {
		t.Fatalf("unexpected clear response: %#v", result)
	}
	for _, identifier := range []model.DownloadID{paused.ID, active.ID} {
		if _, err := store.Download(context.Background(), identifier); err != nil {
			t.Fatalf("preserved download %s is missing: %v", identifier, err)
		}
	}
	for _, path := range []string{failedPartial, canceledPartial} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("historical partial remains at %s: %v", path, err)
		}
	}
	if content, err := os.ReadFile(finalPath); err != nil || string(content) != "final" {
		t.Fatalf("completed file changed: %q, %v", content, err)
	}
}

func newHistoryService(t *testing.T, store *storage.Store, engine daemon.DownloadEngine) *daemon.Service {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	service, err := daemon.NewService(ctx, store, engine)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		if err := service.Close(); err != nil {
			t.Error(err)
		}
	})

	return service
}

func createHistoryDownload(
	t *testing.T,
	store *storage.Store,
	status model.Status,
	destination string,
	filename string,
) model.Download {
	t.Helper()
	identifier, err := model.NewDownloadID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	download := model.Download{
		ID: identifier, URL: "https://example.test/" + filename, Destination: destination,
		Filename: filename, TotalSize: 0, Status: status, Priority: model.PriorityNormal,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateDownload(context.Background(), download); err != nil {
		t.Fatal(err)
	}

	return download
}

func createHistoryPartial(t *testing.T, parts string, identifier model.DownloadID) string {
	t.Helper()
	if err := os.MkdirAll(parts, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parts, identifier.String()+".part")
	if err := os.WriteFile(path, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func removeHistory(
	t *testing.T,
	service *daemon.Service,
	identifier model.DownloadID,
) (ipc.DownloadActionResponse, error) {
	t.Helper()
	payload, err := json.Marshal(ipc.DownloadActionRequest{ID: identifier.String()})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Handle(context.Background(), ipc.Request{Operation: ipc.OperationRemove, Payload: payload})
	if err != nil {
		return ipc.DownloadActionResponse{}, err
	}
	response, ok := result.(ipc.DownloadActionResponse)
	if !ok {
		t.Fatalf("unexpected remove response type %T", result)
	}

	return response, nil
}

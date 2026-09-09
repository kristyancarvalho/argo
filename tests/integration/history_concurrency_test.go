package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/ipc"
	"github.com/kristyancarvalho/argo/internal/model"
	"github.com/kristyancarvalho/argo/internal/storage"
)

type coordinatedHistoryEngine struct {
	*historyEngine
	clean   func(model.DownloadID) error
	stopped chan struct{}
}

func (engine *coordinatedHistoryEngine) RemovePartial(id model.DownloadID) error {
	if engine.clean != nil {
		return engine.clean(id)
	}
	return engine.historyEngine.RemovePartial(id)
}

func (engine *coordinatedHistoryEngine) ValidateCanceledResume(context.Context, model.Download) error {
	return nil
}

func (engine *coordinatedHistoryEngine) Download(ctx context.Context, download model.Download) error {
	err := engine.historyEngine.Download(ctx, download)
	if engine.stopped != nil {
		<-engine.stopped
	}
	return err
}

func TestHistoryCleanupExcludesConcurrentResume(t *testing.T) {
	for _, operation := range []ipc.Operation{ipc.OperationRemove, ipc.OperationClear} {
		t.Run(string(operation), func(t *testing.T) {
			parts := isolateDownloadState(t)
			store := openTestStore(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			engine := &coordinatedHistoryEngine{historyEngine: &historyEngine{
				store: store, parts: parts, started: make(chan model.DownloadID, 1),
			}}
			engine.clean = func(id model.DownloadID) error {
				close(entered)
				<-release
				return engine.historyEngine.RemovePartial(id)
			}
			service := newHistoryService(t, store, engine)
			t.Cleanup(unblock)
			download := createHistoryDownload(t, store, model.StatusCanceled, t.TempDir(), "canceled.bin")
			createHistoryPartial(t, parts, download.ID)
			payload, err := json.Marshal(ipc.DownloadActionRequest{ID: download.ID.String()})
			if err != nil {
				t.Fatal(err)
			}
			removed := make(chan error, 1)
			go func() {
				_, err := service.Handle(context.Background(), ipc.Request{Operation: operation, Payload: payload})
				removed <- err
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("cleanup did not begin")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			if _, err := service.Handle(ctx, ipc.Request{Operation: ipc.OperationResume, Payload: payload}); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("resume crossed cleanup boundary: %v", err)
			}
			if _, err := service.Handle(context.Background(), ipc.Request{Operation: ipc.OperationStatus}); err != nil {
				t.Fatalf("status unavailable during cleanup: %v", err)
			}
			unblock()
			if err := <-removed; err != nil {
				t.Fatal(err)
			}
			if _, err := service.Handle(context.Background(), ipc.Request{Operation: ipc.OperationResume, Payload: payload}); !errors.Is(err, storage.ErrDownloadNotFound) {
				t.Fatalf("removed download resumed: %v", err)
			}
			select {
			case <-engine.started:
				t.Fatal("worker started during removal")
			default:
			}
		})
	}
}

func TestHistoryRemovalWaitsForCanceledWorker(t *testing.T) {
	parts := isolateDownloadState(t)
	store := openTestStore(t)
	download := createHistoryDownload(t, store, model.StatusQueued, t.TempDir(), "active.bin")
	engine := &coordinatedHistoryEngine{historyEngine: &historyEngine{
		store: store, parts: parts, started: make(chan model.DownloadID, 1),
	}, stopped: make(chan struct{})}
	service := newHistoryService(t, store, engine)
	var once sync.Once
	unblock := func() { once.Do(func() { close(engine.stopped) }) }
	t.Cleanup(unblock)
	select {
	case <-engine.started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not start")
	}
	payload, err := json.Marshal(ipc.DownloadActionRequest{ID: download.ID.String()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Handle(context.Background(), ipc.Request{Operation: ipc.OperationCancel, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []ipc.Operation{ipc.OperationRemove, ipc.OperationResume} {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		_, err := service.Handle(ctx, ipc.Request{Operation: operation, Payload: payload})
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%s did not wait for the previous worker: %v", operation, err)
		}
	}
	if _, err := store.Download(context.Background(), download.ID); err != nil {
		t.Fatal(err)
	}
	unblock()
	if _, err := removeHistory(t, service, download.ID); err != nil {
		t.Fatal(err)
	}
}

func TestClearOnlyDeletesCleanedSnapshot(t *testing.T) {
	parts := isolateDownloadState(t)
	store := openTestStore(t)
	engine := &coordinatedHistoryEngine{historyEngine: &historyEngine{
		store: store, parts: parts, started: make(chan model.DownloadID, 1),
	}}
	service := newHistoryService(t, store, engine)
	createHistoryDownload(t, store, model.StatusCanceled, t.TempDir(), "old.bin")
	newlyFailed := createHistoryDownload(t, store, model.StatusDownloading, t.TempDir(), "new.bin")
	engine.clean = func(model.DownloadID) error {
		return store.UpdateDownloadStatus(context.Background(), newlyFailed.ID, model.StatusFailed, time.Now(), "network failure")
	}
	result, err := service.Handle(context.Background(), ipc.Request{Operation: ipc.OperationClear})
	if err != nil {
		t.Fatal(err)
	}
	if result.(ipc.ClearResponse).Removed != 1 {
		t.Fatalf("clear removed a record outside its cleaned snapshot: %+v", result)
	}
	if _, err := store.Download(context.Background(), newlyFailed.ID); err != nil {
		t.Fatal(err)
	}
}

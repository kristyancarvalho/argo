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
	"sync/atomic"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/downloader"
	"github.com/kristyancarvalho/argo/internal/model"
	"github.com/kristyancarvalho/argo/internal/storage"
)

type checkpointStore struct {
	*storage.Store
	calls     atomic.Int64
	failure   error
	failureAt int64
}

func (store *checkpointStore) UpdateDownloadProgress(ctx context.Context, id model.DownloadID, count int64, at time.Time) error {
	store.calls.Add(1)
	if store.failure != nil && (store.failureAt == 0 || count == store.failureAt) {
		return store.failure
	}
	return store.Store.UpdateDownloadProgress(ctx, id, count, at)
}

func (store *checkpointStore) UpdateChunkProgress(ctx context.Context, id model.DownloadID, index int, count int64, at time.Time) error {
	store.calls.Add(1)
	if store.failure != nil && (store.failureAt == 0 || count == store.failureAt) {
		return store.failure
	}
	return store.Store.UpdateChunkProgress(ctx, id, index, count, at)
}

func TestProgressCheckpointsBatchAndFlushCompletion(t *testing.T) {
	for _, chunks := range []int{1, 4} {
		t.Run(fmt.Sprint(chunks), func(t *testing.T) {
			payload := makePayload(4<<20 + 17)
			server := checkpointServer(t, payload)
			store := &checkpointStore{Store: openTestStore(t)}
			download := persistedDownload(t, store.Store, server.URL, t.TempDir(), "file.bin")
			engine := checkpointEngine(t, store, t.TempDir(), chunks, nil)
			if err := engine.Download(context.Background(), download); err != nil {
				t.Fatal(err)
			}
			assertCompletedDownload(t, store.Store, download, payload)
			if calls := store.calls.Load(); calls < int64(chunks) || calls > 32 {
				t.Fatalf("unexpected checkpoint count %d for %d chunks", calls, chunks)
			}
		})
	}
}

func TestCheckpointFailurePreventsCompletion(t *testing.T) {
	for _, chunks := range []int{1, 4} {
		t.Run(fmt.Sprint(chunks), func(t *testing.T) {
			server := checkpointServer(t, makePayload(256<<10))
			failure := errors.New("checkpoint unavailable")
			store := &checkpointStore{Store: openTestStore(t), failure: failure}
			download := persistedDownload(t, store.Store, server.URL, t.TempDir(), "file.bin")
			engine := checkpointEngine(t, store, t.TempDir(), chunks, nil)
			if err := engine.Download(context.Background(), download); !errors.Is(err, failure) {
				t.Fatalf("checkpoint error lost: %v", err)
			}
			persisted, err := store.Download(context.Background(), download.ID)
			if err != nil || persisted.Status != model.StatusFailed {
				t.Fatalf("checkpoint failure reported success: %+v, %v", persisted, err)
			}
		})
	}
}

func TestFinalCheckpointFailurePreventsCompletion(t *testing.T) {
	for _, chunks := range []int{1, 4} {
		t.Run(fmt.Sprint(chunks), func(t *testing.T) {
			server := checkpointServer(t, makePayload(chunks*96<<10))
			failure := errors.New("final checkpoint unavailable")
			store := &checkpointStore{Store: openTestStore(t), failure: failure, failureAt: 96 << 10}
			download := persistedDownload(t, store.Store, server.URL, t.TempDir(), "file.bin")
			engine := checkpointEngine(t, store, t.TempDir(), chunks, nil)
			if err := engine.Download(context.Background(), download); !errors.Is(err, failure) {
				t.Fatalf("final checkpoint error lost: %v", err)
			}
			persisted, err := store.Download(context.Background(), download.ID)
			if err != nil || persisted.Status != model.StatusFailed || persisted.DownloadedBytes == 0 {
				t.Fatalf("final checkpoint failure reported success or lost earlier progress: %+v, %v", persisted, err)
			}
		})
	}
}

func TestCancellationFlushesSerialCheckpointRemainder(t *testing.T) {
	payload := makePayload(96 << 10)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Length", "1048576")
		_, _ = writer.Write(payload)
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
	}))
	defer server.Close()
	store := &checkpointStore{Store: openTestStore(t)}
	download := persistedDownload(t, store.Store, server.URL, t.TempDir(), "file.bin")
	parts := t.TempDir()
	engine := checkpointEngine(t, store, parts, 1, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- engine.Download(ctx, download) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		info, err := os.Stat(filepath.Join(parts, download.ID.String()+".part"))
		if err == nil && info.Size() == int64(len(payload)) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("transfer did not reach uncheckpointed remainder")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	persisted, err := store.Download(context.Background(), download.ID)
	if err != nil || persisted.DownloadedBytes != int64(len(payload)) {
		t.Fatalf("canceled remainder not flushed: %+v, %v", persisted, err)
	}
}

func TestCancellationFlushesParallelCheckpointRemainder(t *testing.T) {
	payload := makePayload(1 << 20)
	server := parallelServer(t, payload, func(writer http.ResponseWriter, request *http.Request, start, end int64) {
		writer.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		writer.Header().Set("Content-Length", fmt.Sprint(end-start+1))
		writer.WriteHeader(http.StatusPartialContent)
		_, _ = writer.Write(payload[start : start+128<<10])
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
	})
	store := &checkpointStore{Store: openTestStore(t)}
	download := persistedDownload(t, store.Store, server.URL, t.TempDir(), "file.bin")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine := checkpointEngine(t, store, t.TempDir(), 4, func(_ model.DownloadID, progress downloader.ChunkProgress) {
		if progress.Index == 0 && progress.Downloaded >= 96<<10 {
			cancel()
		}
	})
	if err := engine.Download(ctx, download); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	chunks, err := store.DownloadChunks(context.Background(), download.ID)
	if err != nil || len(chunks) != 4 || chunks[0].DownloadedBytes < 96<<10 {
		t.Fatalf("canceled chunk remainder not flushed: %+v, %v", chunks, err)
	}
}

func TestProgressCheckpointRefreshesSlowTransfer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Length", "1048576")
		_, _ = writer.Write(make([]byte, 32<<10))
		writer.(http.Flusher).Flush()
		select {
		case <-request.Context().Done():
			return
		case <-time.After(1200 * time.Millisecond):
		}
		_, _ = writer.Write(make([]byte, 4096))
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
	}))
	defer server.Close()
	store := &checkpointStore{Store: openTestStore(t)}
	download := persistedDownload(t, store.Store, server.URL, t.TempDir(), "file.bin")
	engine := checkpointEngine(t, store, t.TempDir(), 1, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- engine.Download(ctx, download) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		persisted, err := store.Download(context.Background(), download.ID)
		if err != nil {
			t.Fatal(err)
		}
		if persisted.DownloadedBytes > 32<<10 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("slow transfer did not checkpoint before EOF")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func checkpointServer(t *testing.T, payload []byte) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("ETag", `"checkpoint"`)
		http.ServeContent(writer, request, "file.bin", time.Time{}, bytes.NewReader(payload))
	}))
	t.Cleanup(server.Close)
	return server
}

func checkpointEngine(t *testing.T, store *checkpointStore, parts string, chunks int, observer func(model.DownloadID, downloader.ChunkProgress)) *downloader.Engine {
	t.Helper()
	engine, err := downloader.NewWithOptions(store, downloader.Options{
		PartsDirectory: parts, MaximumChunks: chunks, MinimumChunkSize: 1, ChunkProgress: observer,
	})
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

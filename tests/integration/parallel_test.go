package integration_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/downloader"
	"github.com/kristyancarvalho/argo/internal/model"
	"github.com/kristyancarvalho/argo/internal/storage"
)

func TestParallelRangeDownloadIntegrityAndProgress(t *testing.T) {
	payload := makePayload(4096)
	var active atomic.Int32
	var maximumActive atomic.Int32
	server := parallelServer(t, payload, func(
		response http.ResponseWriter,
		request *http.Request,
		start int64,
		end int64,
	) {
		current := active.Add(1)
		for {
			maximum := maximumActive.Load()
			if current <= maximum || maximumActive.CompareAndSwap(maximum, current) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		writeRange(response, payload, start, end)
		active.Add(-1)
	})

	store := openTestStore(t)
	download := persistedDownload(t, store, server.URL+"/parallel.bin", t.TempDir(), "parallel.bin")
	progress := make(map[int]int64)
	var progressMutex sync.Mutex
	engine := parallelEngine(t, store, func(_ model.DownloadID, update downloader.ChunkProgress) {
		progressMutex.Lock()
		progress[update.Index] = update.Downloaded
		progressMutex.Unlock()
	})
	if err := engine.Download(context.Background(), download); err != nil {
		t.Fatal(err)
	}
	assertCompletedDownload(t, store, download, payload)
	if maximumActive.Load() < 2 || maximumActive.Load() > 4 {
		t.Fatalf("observed %d concurrent workers, expected between 2 and 4", maximumActive.Load())
	}
	progressMutex.Lock()
	defer progressMutex.Unlock()
	if len(progress) != 4 {
		t.Fatalf("tracked progress for %d chunks, expected 4", len(progress))
	}
	for index := 0; index < 4; index++ {
		if progress[index] != 1024 {
			t.Errorf("chunk %d progress is %d, expected 1024", index, progress[index])
		}
	}
}

func TestParallelWorkerFailureCancelsOtherWorkers(t *testing.T) {
	payload := makePayload(4096)
	var canceled atomic.Int32
	peersStarted := make(chan struct{}, 3)
	server := parallelServer(t, payload, func(
		response http.ResponseWriter,
		request *http.Request,
		start int64,
		end int64,
	) {
		if start == 1024 {
			for count := 0; count < 3; count++ {
				<-peersStarted
			}
			http.Error(response, "worker failed", http.StatusServiceUnavailable)
			return
		}
		peersStarted <- struct{}{}
		select {
		case <-request.Context().Done():
			canceled.Add(1)
		case <-time.After(500 * time.Millisecond):
			writeRange(response, payload, start, end)
		}
	})

	store := openTestStore(t)
	download := persistedDownload(t, store, server.URL+"/failure.bin", t.TempDir(), "failure.bin")
	startedAt := time.Now()
	err := parallelEngine(t, store, nil).Download(context.Background(), download)
	if err == nil {
		t.Fatal("parallel worker failure succeeded")
	}
	if time.Since(startedAt) >= 500*time.Millisecond {
		t.Fatal("parallel workers were not canceled after fatal failure")
	}
	if canceled.Load() == 0 {
		t.Fatal("no peer worker observed cancellation")
	}
	persisted, readErr := store.Download(context.Background(), download.ID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if persisted.Status != model.StatusFailed {
		t.Fatalf("worker failure status is %s, expected failed", persisted.Status)
	}
}

func TestParallelDownloadCancellation(t *testing.T) {
	payload := makePayload(4096)
	started := make(chan struct{}, 4)
	server := parallelServer(t, payload, func(
		_ http.ResponseWriter,
		request *http.Request,
		_ int64,
		_ int64,
	) {
		started <- struct{}{}
		<-request.Context().Done()
	})

	store := openTestStore(t)
	download := persistedDownload(t, store, server.URL+"/cancel.bin", t.TempDir(), "cancel.bin")
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	engine := parallelEngine(t, store, nil)
	go func() {
		finished <- engine.Download(ctx, download)
	}()
	<-started
	cancel()
	err := <-finished
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("parallel cancellation returned %v", err)
	}
	persisted, readErr := store.Download(context.Background(), download.ID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if persisted.Status != model.StatusDownloading {
		t.Fatalf("canceled transfer status is %s, expected downloading for recovery", persisted.Status)
	}
}

func TestParallelDownloadDoesNotWaitForSlowChunkToStartOthers(t *testing.T) {
	payload := makePayload(4096)
	fastCompleted := make(chan struct{}, 3)
	server := parallelServer(t, payload, func(
		response http.ResponseWriter,
		_ *http.Request,
		start int64,
		end int64,
	) {
		if start == 0 {
			time.Sleep(100 * time.Millisecond)
		} else {
			fastCompleted <- struct{}{}
		}
		writeRange(response, payload, start, end)
	})

	store := openTestStore(t)
	download := persistedDownload(t, store, server.URL+"/slow.bin", t.TempDir(), "slow.bin")
	finished := make(chan error, 1)
	engine := parallelEngine(t, store, nil)
	go func() {
		finished <- engine.Download(context.Background(), download)
	}()
	select {
	case <-fastCompleted:
	case <-time.After(80 * time.Millisecond):
		t.Fatal("fast chunks did not run while a chunk was slow")
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	assertCompletedDownload(t, store, download, payload)
}

func TestParallelDownloadRejectsRangeMismatch(t *testing.T) {
	payload := makePayload(4096)
	server := parallelServer(t, payload, func(
		response http.ResponseWriter,
		_ *http.Request,
		start int64,
		end int64,
	) {
		if start == 2048 {
			response.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start+1, end, len(payload)))
			response.WriteHeader(http.StatusPartialContent)
			_, _ = response.Write(payload[start : end+1])
			return
		}
		writeRange(response, payload, start, end)
	})

	store := openTestStore(t)
	download := persistedDownload(t, store, server.URL+"/mismatch.bin", t.TempDir(), "mismatch.bin")
	err := parallelEngine(t, store, nil).Download(context.Background(), download)
	var mismatch downloader.RangeMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("range mismatch returned %T, expected RangeMismatchError", err)
	}
	persisted, readErr := store.Download(context.Background(), download.ID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if persisted.Status != model.StatusFailed {
		t.Fatalf("range mismatch status is %s, expected failed", persisted.Status)
	}
}

func parallelEngine(
	t *testing.T,
	store *storage.Store,
	observer func(model.DownloadID, downloader.ChunkProgress),
) *downloader.Engine {
	t.Helper()
	engine, err := downloader.NewWithOptions(store, downloader.Options{
		HTTPClient:       http.DefaultClient,
		MaximumChunks:    4,
		MinimumChunkSize: 1024,
		ChunkProgress:    observer,
	})
	if err != nil {
		t.Fatal(err)
	}

	return engine
}

func parallelServer(
	t *testing.T,
	payload []byte,
	handle func(http.ResponseWriter, *http.Request, int64, int64),
) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		start, end, err := requestedRange(request.Header.Get("Range"), int64(len(payload)))
		if err != nil {
			http.Error(response, err.Error(), http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if start == 0 && end == 0 {
			writeRange(response, payload, start, end)
			return
		}
		handle(response, request, start, end)
	}))
	t.Cleanup(server.Close)

	return server
}

func requestedRange(value string, totalSize int64) (int64, int64, error) {
	if !strings.HasPrefix(value, "bytes=") {
		return 0, 0, fmt.Errorf("missing byte range")
	}
	bounds := strings.Split(strings.TrimPrefix(value, "bytes="), "-")
	if len(bounds) != 2 {
		return 0, 0, fmt.Errorf("invalid byte range")
	}
	start, err := strconv.ParseInt(bounds[0], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	end, err := strconv.ParseInt(bounds[1], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	if start < 0 || end < start || end >= totalSize {
		return 0, 0, fmt.Errorf("byte range is outside resource")
	}

	return start, end, nil
}

func writeRange(response http.ResponseWriter, payload []byte, start, end int64) {
	response.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
	response.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	response.WriteHeader(http.StatusPartialContent)
	_, _ = response.Write(payload[start : end+1])
}

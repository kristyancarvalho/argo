package integration_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/downloader"
	"github.com/kristyancarvalho/argo/internal/model"
	"github.com/kristyancarvalho/argo/internal/storage"
)

const recoveryChunkSize = 64 * 1024

type mutableRangeFixture struct {
	mutex        sync.Mutex
	payload      []byte
	etag         string
	lastModified string
	starts       []int64
	server       *httptest.Server
}

func TestParallelChunksResumeAfterRestartWithIntegrity(t *testing.T) {
	isolateDownloadState(t)
	payload := makePayload(4 * recoveryChunkSize)
	fixture := newMutableRangeFixture(t, payload)
	databasePath := filepath.Join(t.TempDir(), "argo.db")
	destination := t.TempDir()
	download := interruptParallelDownload(t, fixture, databasePath, destination)

	store := reopenRecoveredStore(t, databasePath)
	defer closeStore(t, store)
	fixture.resetStarts()
	resumeParallelDownload(t, store, download)

	starts := fixture.requestStarts()
	resumed := false
	for _, start := range starts {
		if start > 0 && start%recoveryChunkSize != 0 {
			resumed = true
		}
	}
	if !resumed {
		t.Fatalf("restart did not resume within a persisted chunk: %v", starts)
	}
	assertCompletedDownload(t, store, download, payload)
}

func TestParallelChunksInvalidateChangedETag(t *testing.T) {
	testParallelValidatorChange(t, func(fixture *mutableRangeFixture) {
		fixture.setResource(makePayloadVariant(4*recoveryChunkSize, 17), `"chunk-v2"`, fixture.lastModified)
	})
}

func TestParallelChunksInvalidateChangedLastModified(t *testing.T) {
	testParallelValidatorChange(t, func(fixture *mutableRangeFixture) {
		fixture.setResource(makePayloadVariant(4*recoveryChunkSize, 29), fixture.etag, "Tue, 01 Sep 2026 12:00:00 GMT")
	})
}

func TestParallelChunksRecoverFromTruncatedPartial(t *testing.T) {
	parts := isolateDownloadState(t)
	payload := makePayload(4 * recoveryChunkSize)
	fixture := newMutableRangeFixture(t, payload)
	databasePath := filepath.Join(t.TempDir(), "argo.db")
	destination := t.TempDir()
	download := interruptParallelDownload(t, fixture, databasePath, destination)
	partial := filepath.Join(parts, download.ID.String()+".part")
	if err := os.Truncate(partial, recoveryChunkSize/2); err != nil {
		t.Fatal(err)
	}

	store := reopenRecoveredStore(t, databasePath)
	defer closeStore(t, store)
	fixture.resetStarts()
	resumeParallelDownload(t, store, download)

	starts := fixture.requestStarts()
	for _, expected := range []int64{recoveryChunkSize, 2 * recoveryChunkSize, 3 * recoveryChunkSize} {
		if !slices.Contains(starts, expected) {
			t.Fatalf("truncated partial did not reset chunk at %d: %v", expected, starts)
		}
	}
	assertCompletedDownload(t, store, download, payload)
}

func testParallelValidatorChange(t *testing.T, change func(*mutableRangeFixture)) {
	t.Helper()
	isolateDownloadState(t)
	payload := makePayload(4 * recoveryChunkSize)
	fixture := newMutableRangeFixture(t, payload)
	databasePath := filepath.Join(t.TempDir(), "argo.db")
	destination := t.TempDir()
	download := interruptParallelDownload(t, fixture, databasePath, destination)
	change(fixture)
	fixture.resetStarts()

	store := reopenRecoveredStore(t, databasePath)
	defer closeStore(t, store)
	resumeParallelDownload(t, store, download)

	starts := fixture.requestStarts()
	for _, expected := range []int64{0, recoveryChunkSize, 2 * recoveryChunkSize, 3 * recoveryChunkSize} {
		if !slices.Contains(starts, expected) {
			t.Fatalf("validator change did not restart chunk at %d: %v", expected, starts)
		}
	}
	fixture.mutex.Lock()
	expected := append([]byte(nil), fixture.payload...)
	fixture.mutex.Unlock()
	assertCompletedDownload(t, store, download, expected)
}

func interruptParallelDownload(
	t *testing.T,
	fixture *mutableRangeFixture,
	databasePath string,
	destination string,
) model.Download {
	t.Helper()
	store := openStoreAt(t, databasePath)
	download := persistedDownload(t, store, fixture.server.URL+"/chunked.bin", destination, "chunked.bin")
	ctx, cancel := context.WithCancel(context.Background())
	engine := recoveryParallelEngine(t, store, func(_ model.DownloadID, update downloader.ChunkProgress) {
		if update.Downloaded >= 4096 {
			cancel()
		}
	})
	err := engine.Download(ctx, download)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted parallel download returned %v", err)
	}
	persisted, err := store.Download(context.Background(), download.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.DownloadedBytes == 0 || persisted.DownloadedBytes >= int64(len(fixture.payload)) {
		t.Fatalf("unexpected interrupted progress: %d", persisted.DownloadedBytes)
	}
	chunks, err := store.DownloadChunks(context.Background(), download.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 4 {
		t.Fatalf("persisted %d chunks, expected 4", len(chunks))
	}
	closeStore(t, store)

	return download
}

func reopenRecoveredStore(t *testing.T, databasePath string) *storage.Store {
	t.Helper()
	store := openStoreAt(t, databasePath)
	if err := store.RecoverActiveDownloads(context.Background(), time.Now().UTC()); err != nil {
		closeStore(t, store)
		t.Fatal(err)
	}

	return store
}

func resumeParallelDownload(t *testing.T, store *storage.Store, original model.Download) {
	t.Helper()
	download, err := store.Download(context.Background(), original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if download.Status != model.StatusQueued {
		t.Fatalf("recovered status is %s, expected queued", download.Status)
	}
	if err := recoveryParallelEngine(t, store, nil).Download(context.Background(), download); err != nil {
		t.Fatal(err)
	}
}

func recoveryParallelEngine(
	t *testing.T,
	store *storage.Store,
	observer func(model.DownloadID, downloader.ChunkProgress),
) *downloader.Engine {
	t.Helper()
	engine, err := downloader.NewWithOptions(store, downloader.Options{
		HTTPClient:       http.DefaultClient,
		MaximumChunks:    4,
		MinimumChunkSize: recoveryChunkSize,
		ChunkProgress:    observer,
	})
	if err != nil {
		t.Fatal(err)
	}

	return engine
}

func newMutableRangeFixture(t *testing.T, payload []byte) *mutableRangeFixture {
	t.Helper()
	fixture := &mutableRangeFixture{
		payload:      payload,
		etag:         `"chunk-v1"`,
		lastModified: "Mon, 31 Aug 2026 12:00:00 GMT",
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.handle))
	t.Cleanup(fixture.server.Close)

	return fixture
}

func (fixture *mutableRangeFixture) handle(response http.ResponseWriter, request *http.Request) {
	fixture.mutex.Lock()
	payload := fixture.payload
	etag := fixture.etag
	lastModified := fixture.lastModified
	fixture.mutex.Unlock()
	start, end, err := requestedRange(request.Header.Get("Range"), int64(len(payload)))
	if err != nil {
		http.Error(response, err.Error(), http.StatusRequestedRangeNotSatisfiable)
		return
	}
	fixture.mutex.Lock()
	fixture.starts = append(fixture.starts, start)
	fixture.mutex.Unlock()
	response.Header().Set("ETag", etag)
	response.Header().Set("Last-Modified", lastModified)
	response.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
	response.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	response.WriteHeader(http.StatusPartialContent)
	flusher, _ := response.(http.Flusher)
	for position := start; position <= end; position += 4096 {
		limit := min(position+4096, end+1)
		if _, err := response.Write(payload[position:limit]); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(time.Millisecond)
	}
}

func (fixture *mutableRangeFixture) setResource(payload []byte, etag string, lastModified string) {
	fixture.mutex.Lock()
	defer fixture.mutex.Unlock()
	fixture.payload = payload
	fixture.etag = etag
	fixture.lastModified = lastModified
}

func (fixture *mutableRangeFixture) resetStarts() {
	fixture.mutex.Lock()
	defer fixture.mutex.Unlock()
	fixture.starts = nil
}

func (fixture *mutableRangeFixture) requestStarts() []int64 {
	fixture.mutex.Lock()
	defer fixture.mutex.Unlock()

	return append([]int64(nil), fixture.starts...)
}

func makePayloadVariant(size int, offset byte) []byte {
	payload := make([]byte, size)
	for index := range payload {
		payload[index] = byte((index + int(offset)) % 251)
	}

	return payload
}

func closeStore(t *testing.T, store *storage.Store) {
	t.Helper()
	if err := store.Close(); err != nil {
		t.Error(err)
	}
}

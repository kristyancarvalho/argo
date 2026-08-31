package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/daemon"
	"github.com/kristyancarvalho/argo/internal/downloader"
	"github.com/kristyancarvalho/argo/internal/ipc"
	"github.com/kristyancarvalho/argo/internal/model"
	"github.com/kristyancarvalho/argo/internal/storage"
)

type runningDownloadDaemon struct {
	client   *ipc.Client
	cancel   context.CancelFunc
	service  *daemon.Service
	server   *ipc.Server
	finished <-chan error
}

func TestPauseResumeAndPausedRestartIntegrity(t *testing.T) {
	payload := makePayload(256 * 1024)
	httpServer, rangeRequests := rangeFixtureServer(t, payload)
	databasePath := filepath.Join(t.TempDir(), "argo.db")
	destination := t.TempDir()

	firstStore := openStoreAt(t, databasePath)
	firstDaemon := startDownloadDaemon(t, firstStore)
	added, err := firstDaemon.client.Add(context.Background(), httpServer.URL+"/payload.bin", destination)
	if err != nil {
		t.Fatal(err)
	}
	identifier, err := model.ParseDownloadID(added.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitForDownload(t, firstStore, identifier, func(download model.Download) bool {
		return download.Status == model.StatusDownloading && download.DownloadedBytes >= 32*1024
	})
	paused, err := firstDaemon.client.Pause(context.Background(), identifier.String())
	if err != nil {
		t.Fatal(err)
	}
	if paused.Status != string(model.StatusPaused) {
		t.Fatalf("pause returned status %q", paused.Status)
	}
	pausedDownload := waitForDownload(t, firstStore, identifier, func(download model.Download) bool {
		return download.Status == model.StatusPaused
	})
	partialPath := filepath.Join(destination, ".argo-"+identifier.String()+".part")
	partialInfo, err := os.Stat(partialPath)
	if err != nil {
		t.Fatal(err)
	}
	if partialInfo.Size() < pausedDownload.DownloadedBytes || pausedDownload.DownloadedBytes == 0 {
		t.Fatalf("partial size is %d, persisted progress is %d", partialInfo.Size(), pausedDownload.DownloadedBytes)
	}
	stopDownloadDaemon(t, firstDaemon)
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}

	secondStore := openStoreAt(t, databasePath)
	secondDaemon := startDownloadDaemon(t, secondStore)
	restored, err := secondStore.Download(context.Background(), identifier)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Status != model.StatusPaused || restored.DownloadedBytes != pausedDownload.DownloadedBytes {
		t.Fatalf("unexpected paused restoration: %+v", restored)
	}
	if _, err := secondDaemon.client.Resume(context.Background(), identifier.String()); err != nil {
		t.Fatal(err)
	}
	waitForDownload(t, secondStore, identifier, func(download model.Download) bool {
		return download.Status == model.StatusCompleted
	})
	stopDownloadDaemon(t, secondDaemon)
	if err := secondStore.Close(); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, filepath.Join(destination, "payload.bin"), payload)
	if rangeRequests.Load() == 0 {
		t.Fatal("resume did not issue a Range request")
	}
}

func TestActiveDownloadRecoversAfterDaemonRestart(t *testing.T) {
	payload := makePayload(256 * 1024)
	httpServer, rangeRequests := rangeFixtureServer(t, payload)
	databasePath := filepath.Join(t.TempDir(), "argo.db")
	destination := t.TempDir()

	firstStore := openStoreAt(t, databasePath)
	firstDaemon := startDownloadDaemon(t, firstStore)
	added, err := firstDaemon.client.Add(context.Background(), httpServer.URL+"/restart.bin", destination)
	if err != nil {
		t.Fatal(err)
	}
	identifier, err := model.ParseDownloadID(added.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeRestart := waitForDownload(t, firstStore, identifier, func(download model.Download) bool {
		return download.Status == model.StatusDownloading && download.DownloadedBytes >= 32*1024
	})
	stopDownloadDaemon(t, firstDaemon)
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}

	secondStore := openStoreAt(t, databasePath)
	secondDaemon := startDownloadDaemon(t, secondStore)
	completed := waitForDownload(t, secondStore, identifier, func(download model.Download) bool {
		return download.Status == model.StatusCompleted
	})
	stopDownloadDaemon(t, secondDaemon)
	if err := secondStore.Close(); err != nil {
		t.Fatal(err)
	}
	if completed.DownloadedBytes != int64(len(payload)) || beforeRestart.DownloadedBytes == 0 {
		t.Fatalf("unexpected recovery progress: before %d, after %d", beforeRestart.DownloadedBytes, completed.DownloadedBytes)
	}
	assertFileContent(t, filepath.Join(destination, "restart.bin"), payload)
	if rangeRequests.Load() == 0 {
		t.Fatal("restart recovery did not issue a Range request")
	}
}

func rangeFixtureServer(t *testing.T, payload []byte) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var rangeRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		offset := int64(0)
		lastByte := int64(len(payload) - 1)
		if rangeHeader := request.Header.Get("Range"); rangeHeader != "" {
			if !strings.HasPrefix(rangeHeader, "bytes=") {
				http.Error(response, "invalid range", http.StatusRequestedRangeNotSatisfiable)
				return
			}
			bounds := strings.Split(strings.TrimPrefix(rangeHeader, "bytes="), "-")
			if len(bounds) != 2 {
				http.Error(response, "invalid range", http.StatusRequestedRangeNotSatisfiable)
				return
			}
			parsedOffset, err := strconv.ParseInt(bounds[0], 10, 64)
			if err != nil || parsedOffset < 0 || parsedOffset >= int64(len(payload)) {
				http.Error(response, "invalid range", http.StatusRequestedRangeNotSatisfiable)
				return
			}
			offset = parsedOffset
			if bounds[1] != "" {
				lastByte, err = strconv.ParseInt(bounds[1], 10, 64)
				if err != nil || lastByte < offset || lastByte >= int64(len(payload)) {
					http.Error(response, "invalid range", http.StatusRequestedRangeNotSatisfiable)
					return
				}
			}
			if offset > 0 {
				rangeRequests.Add(1)
			}
			response.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, lastByte, len(payload)))
			response.Header().Set("Content-Length", strconv.FormatInt(lastByte-offset+1, 10))
			response.WriteHeader(http.StatusPartialContent)
		} else {
			response.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		}
		flusher, _ := response.(http.Flusher)
		for position := offset; position <= lastByte; position += 4096 {
			end := min(position+4096, lastByte+1)
			if _, err := response.Write(payload[position:end]); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(time.Millisecond)
		}
	}))
	t.Cleanup(server.Close)

	return server, &rangeRequests
}

func startDownloadDaemon(t *testing.T, store *storage.Store) runningDownloadDaemon {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	service, err := daemon.NewService(ctx, store, downloader.New(store))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	server, err := ipc.Listen(filepath.Join(t.TempDir(), "argod.sock"), service)
	if err != nil {
		cancel()
		_ = service.Close()
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		finished <- server.Serve(ctx)
	}()

	return runningDownloadDaemon{
		client:   ipc.NewClient(server.SocketPath()),
		cancel:   cancel,
		service:  service,
		server:   server,
		finished: finished,
	}
}

func stopDownloadDaemon(t *testing.T, running runningDownloadDaemon) {
	t.Helper()
	running.cancel()
	select {
	case err := <-running.finished:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(3 * time.Second):
		if err := running.server.Close(); err != nil {
			t.Error(err)
		}
		t.Error("IPC server did not stop")
	}
	if err := running.service.Close(); err != nil {
		t.Error(err)
	}
}

func openStoreAt(t *testing.T, path string) *storage.Store {
	t.Helper()
	store, err := storage.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}

	return store
}

func waitForDownload(
	t *testing.T,
	store *storage.Store,
	identifier model.DownloadID,
	condition func(model.Download) bool,
) model.Download {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		download, err := store.Download(context.Background(), identifier)
		if err != nil {
			t.Fatal(err)
		}
		if condition(download) {
			return download
		}
		if download.Status == model.StatusFailed {
			t.Fatalf("download failed while waiting: %s", download.Error)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for download: %+v", download)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func makePayload(size int) []byte {
	payload := make([]byte, size)
	for index := range payload {
		payload[index] = byte(index % 251)
	}

	return payload
}

func assertFileContent(t *testing.T, path string, expected []byte) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(expected) {
		t.Fatalf("file content differs: got %d bytes, expected %d", len(content), len(expected))
	}
}

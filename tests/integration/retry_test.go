package integration_test

import (
	"context"
	"errors"
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

	"github.com/kristyancarvalho/argo/internal/downloader"
	"github.com/kristyancarvalho/argo/internal/ipc"
	"github.com/kristyancarvalho/argo/internal/model"
)

func TestRepeatedURLCreatesNewDownloadsAndCollisionNames(t *testing.T) {
	isolateDownloadState(t)
	payload := makePayload(64 * 1024)
	httpServer, _ := rangeFixtureServer(t, payload)
	store := openTestStore(t)
	running := startDownloadDaemon(t, store)
	defer stopDownloadDaemon(t, running)
	destination := t.TempDir()
	first, err := running.client.Add(context.Background(), httpServer.URL+"/repeat.iso", destination)
	if err != nil {
		t.Fatal(err)
	}
	firstID, err := model.ParseDownloadID(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitForDownload(t, store, firstID, func(download model.Download) bool {
		return download.Status == model.StatusCompleted
	})
	second, err := running.client.Add(context.Background(), httpServer.URL+"/repeat.iso", destination)
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := model.ParseDownloadID(second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if secondID == firstID {
		t.Fatal("repeated URL reused the original download ID")
	}
	secondDownload := waitForDownload(t, store, secondID, func(download model.Download) bool {
		return download.Status == model.StatusCompleted
	})
	if secondDownload.Filename != "repeat (1).iso" {
		t.Fatalf("repeated filename is %q", secondDownload.Filename)
	}
	for _, name := range []string{"repeat.iso", "repeat (1).iso"} {
		if content, err := os.ReadFile(filepath.Join(destination, name)); err != nil || string(content) != string(payload) {
			t.Fatalf("download %s has invalid content: %v", name, err)
		}
	}
}

func TestRetryCompletedDownloadCreatesNewIdentity(t *testing.T) {
	isolateDownloadState(t)
	payload := makePayload(64 * 1024)
	httpServer, _ := rangeFixtureServer(t, payload)
	store := openTestStore(t)
	running := startDownloadDaemon(t, store)
	defer stopDownloadDaemon(t, running)
	destination := t.TempDir()
	original, err := running.client.Add(context.Background(), httpServer.URL+"/retry.bin", destination)
	if err != nil {
		t.Fatal(err)
	}
	originalID, err := model.ParseDownloadID(original.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitForDownload(t, store, originalID, func(download model.Download) bool {
		return download.Status == model.StatusCompleted
	})
	retried, err := running.client.Retry(context.Background(), original.ID)
	if err != nil {
		t.Fatal(err)
	}
	retriedID, err := model.ParseDownloadID(retried.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retriedID == originalID {
		t.Fatal("retry reused the completed download ID")
	}
	waitForDownload(t, store, retriedID, func(download model.Download) bool {
		return download.Status == model.StatusCompleted
	})
	originalDownload, err := store.Download(context.Background(), originalID)
	if err != nil {
		t.Fatal(err)
	}
	if originalDownload.Status != model.StatusCompleted || originalDownload.Filename != "retry.bin" {
		t.Fatalf("retry mutated original history: %+v", originalDownload)
	}
}

func TestCanceledDownloadResumesFromPartialWithSameIdentity(t *testing.T) {
	parts := isolateDownloadState(t)
	payload := makePayload(512 * 1024)
	httpServer, rangeRequests := rangeFixtureServer(t, payload)
	store := openTestStore(t)
	running := startDownloadDaemon(t, store)
	defer stopDownloadDaemon(t, running)
	added, err := running.client.Add(context.Background(), httpServer.URL+"/resume.bin", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	identifier, err := model.ParseDownloadID(added.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitForDownload(t, store, identifier, func(download model.Download) bool {
		return download.Status == model.StatusDownloading && download.DownloadedBytes >= 32*1024
	})
	if _, err := running.client.Cancel(context.Background(), added.ID); err != nil {
		t.Fatal(err)
	}
	canceled := waitForDownload(t, store, identifier, func(download model.Download) bool {
		return download.Status == model.StatusCanceled
	})
	if _, err := os.Stat(filepath.Join(parts, identifier.String()+".part")); err != nil {
		t.Fatal(err)
	}
	resumed, err := running.client.Resume(context.Background(), added.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ID != added.ID || resumed.Status != string(model.StatusQueued) {
		t.Fatalf("unexpected resume response: %+v", resumed)
	}
	completed := waitForDownload(t, store, identifier, func(download model.Download) bool {
		return download.Status == model.StatusCompleted
	})
	if completed.DownloadedBytes != int64(len(payload)) || canceled.DownloadedBytes == 0 || rangeRequests.Load() == 0 {
		t.Fatalf("canceled resume did not reuse partial state: canceled=%d completed=%d ranges=%d", canceled.DownloadedBytes, completed.DownloadedBytes, rangeRequests.Load())
	}
}

func TestCanceledResumeRejectsMissingPartialData(t *testing.T) {
	parts := isolateDownloadState(t)
	payload := makePayload(512 * 1024)
	httpServer, _ := rangeFixtureServer(t, payload)
	store := openTestStore(t)
	running := startDownloadDaemon(t, store)
	defer stopDownloadDaemon(t, running)
	added, err := running.client.Add(context.Background(), httpServer.URL+"/missing.bin", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	identifier, err := model.ParseDownloadID(added.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitForDownload(t, store, identifier, func(download model.Download) bool {
		return download.Status == model.StatusDownloading && download.DownloadedBytes >= 32*1024
	})
	if _, err := running.client.Cancel(context.Background(), added.ID); err != nil {
		t.Fatal(err)
	}
	waitForDownload(t, store, identifier, func(download model.Download) bool {
		return download.Status == model.StatusCanceled
	})
	if err := os.Remove(filepath.Join(parts, identifier.String()+".part")); err != nil {
		t.Fatal(err)
	}
	_, err = running.client.Resume(context.Background(), added.ID)
	var remoteError ipc.RemoteError
	if !errors.As(err, &remoteError) || remoteError.Code != "invalid_request" {
		t.Fatalf("missing partial resume returned %v", err)
	}
	persisted, err := store.Download(context.Background(), identifier)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.StatusCanceled {
		t.Fatalf("invalid resume changed status to %s", persisted.Status)
	}
}

func TestCanceledResumeMetadataRequestHonorsCancellation(t *testing.T) {
	requestStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		close(requestStarted)
		<-request.Context().Done()
	}))
	defer server.Close()
	parts := t.TempDir()
	store := openTestStore(t)
	identifier, err := model.NewDownloadID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	download := model.Download{
		ID: identifier, URL: server.URL + "/resume.bin", Destination: t.TempDir(), Filename: "resume.bin",
		TotalSize: 1024, DownloadedBytes: 1, Status: model.StatusCanceled, Priority: model.PriorityNormal,
		CreatedAt: now, UpdatedAt: now, ETag: `"resume"`, RangeSupported: true,
	}
	if err := store.CreateDownload(context.Background(), download); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parts, identifier.String()+".part"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	engine, err := downloader.NewWithOptions(store, downloader.Options{
		HTTPClient: server.Client(), PartsDirectory: parts,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = engine.ValidateCanceledResume(ctx, download)
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("canceled resume returned %v after %s", err, time.Since(started))
	}
	awaitSignal(t, requestStarted)
}

func TestCanceledResumeRejectsChangedRemoteValidators(t *testing.T) {
	isolateDownloadState(t)
	payload := makePayload(512 * 1024)
	httpServer, etag := mutableValidatorServer(t, payload)
	store := openTestStore(t)
	running := startDownloadDaemon(t, store)
	defer stopDownloadDaemon(t, running)
	added, err := running.client.Add(context.Background(), httpServer.URL+"/changed.bin", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	identifier, err := model.ParseDownloadID(added.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitForDownload(t, store, identifier, func(download model.Download) bool {
		return download.Status == model.StatusDownloading && download.DownloadedBytes >= 32*1024
	})
	if _, err := running.client.Cancel(context.Background(), added.ID); err != nil {
		t.Fatal(err)
	}
	waitForDownload(t, store, identifier, func(download model.Download) bool {
		return download.Status == model.StatusCanceled
	})
	etag.Store(`"validator-v2"`)
	_, err = running.client.Resume(context.Background(), added.ID)
	var remoteError ipc.RemoteError
	if !errors.As(err, &remoteError) || remoteError.Code != "invalid_request" || !strings.Contains(remoteError.Message, "validators changed") {
		t.Fatalf("changed validator resume returned %v", err)
	}
	persisted, err := store.Download(context.Background(), identifier)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.StatusCanceled {
		t.Fatalf("changed validator resume changed status to %s", persisted.Status)
	}
}

func TestCanceledURLCanBeAddedAgainWithNewIdentity(t *testing.T) {
	isolateDownloadState(t)
	payload := makePayload(512 * 1024)
	httpServer, _ := rangeFixtureServer(t, payload)
	store := openTestStore(t)
	running := startDownloadDaemon(t, store)
	defer stopDownloadDaemon(t, running)
	destination := t.TempDir()
	first, err := running.client.Add(context.Background(), httpServer.URL+"/again.bin", destination)
	if err != nil {
		t.Fatal(err)
	}
	firstID, err := model.ParseDownloadID(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitForDownload(t, store, firstID, func(download model.Download) bool {
		return download.Status == model.StatusDownloading && download.DownloadedBytes >= 32*1024
	})
	if _, err := running.client.Cancel(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	waitForDownload(t, store, firstID, func(download model.Download) bool {
		return download.Status == model.StatusCanceled
	})
	second, err := running.client.Add(context.Background(), httpServer.URL+"/again.bin", destination)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatal("adding a canceled URL reused its download ID")
	}
}

func mutableValidatorServer(t *testing.T, payload []byte) (*httptest.Server, *atomic.Value) {
	t.Helper()
	etag := &atomic.Value{}
	etag.Store(`"validator-v1"`)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("ETag", etag.Load().(string))
		response.Header().Set("Last-Modified", "Mon, 31 Aug 2026 12:00:00 GMT")
		start := int64(0)
		end := int64(len(payload) - 1)
		if value := request.Header.Get("Range"); value != "" {
			if !strings.HasPrefix(value, "bytes=") {
				http.Error(response, "invalid range", http.StatusRequestedRangeNotSatisfiable)
				return
			}
			bounds := strings.Split(strings.TrimPrefix(value, "bytes="), "-")
			parsed, err := strconv.ParseInt(bounds[0], 10, 64)
			if err != nil || parsed < 0 || parsed >= int64(len(payload)) {
				http.Error(response, "invalid range", http.StatusRequestedRangeNotSatisfiable)
				return
			}
			start = parsed
			if len(bounds) == 2 && bounds[1] != "" {
				end, err = strconv.ParseInt(bounds[1], 10, 64)
				if err != nil || end < start || end >= int64(len(payload)) {
					http.Error(response, "invalid range", http.StatusRequestedRangeNotSatisfiable)
					return
				}
			}
			response.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
			response.WriteHeader(http.StatusPartialContent)
		}
		for position := start; position <= end; position += 4096 {
			limit := min(position+4096, end+1)
			if _, err := response.Write(payload[position:limit]); err != nil {
				return
			}
			if flusher, ok := response.(http.Flusher); ok {
				flusher.Flush()
			}
			time.Sleep(time.Millisecond)
		}
	}))
	t.Cleanup(server.Close)

	return server, etag
}

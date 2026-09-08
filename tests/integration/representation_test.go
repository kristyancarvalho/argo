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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/downloader"
	"github.com/kristyancarvalho/argo/internal/model"
)

func TestParallelDownloadRejectsChangedRepresentationBetweenChunks(t *testing.T) {
	payload := makePayload(4096)
	var headersMutex sync.Mutex
	var conditions []string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		start, end, err := requestedRange(request.Header.Get("Range"), int64(len(payload)))
		if err != nil {
			http.Error(response, err.Error(), http.StatusRequestedRangeNotSatisfiable)
			return
		}
		etag := `"v1"`
		body := payload[start : end+1]
		if start != 0 || end != 0 {
			headersMutex.Lock()
			conditions = append(conditions, request.Header.Get("If-Match"))
			headersMutex.Unlock()
			if start == 0 {
				etag = `"v2"`
				body = make([]byte, end-start+1)
			} else {
				etag = `"v3"`
				body = make([]byte, end-start+1)
				for index := range body {
					body[index] = 1
				}
			}
		}
		response.Header().Set("ETag", etag)
		response.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		response.Header().Set("Content-Length", strconv.Itoa(len(body)))
		response.WriteHeader(http.StatusPartialContent)
		_, _ = response.Write(body)
	}))
	t.Cleanup(server.Close)

	store := openTestStore(t)
	destination := t.TempDir()
	download := persistedDownload(t, store, server.URL+"/changing.bin", destination, "changing.bin")
	err := parallelEngine(t, store, nil).Download(context.Background(), download)
	var changed downloader.RepresentationChangedError
	if !errors.As(err, &changed) {
		t.Fatalf("changed representation returned %T: %v", err, err)
	}
	headersMutex.Lock()
	defer headersMutex.Unlock()
	if len(conditions) == 0 {
		t.Fatal("no data request was made")
	}
	for _, condition := range conditions {
		if condition != `"v1"` {
			t.Fatalf("data request If-Match is %q", condition)
		}
	}
	if _, err := os.Stat(filepath.Join(destination, "changing.bin")); !os.IsNotExist(err) {
		t.Fatalf("changed representation produced a final file: %v", err)
	}
}

func TestDownloadRejectsLastModifiedChangeAfterInspection(t *testing.T) {
	payload := makePayload(2048)
	const first = "Mon, 31 Aug 2026 12:00:00 GMT"
	const second = "Tue, 01 Sep 2026 12:00:00 GMT"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		current := requests.Add(1)
		modified := first
		if current > 1 {
			modified = second
			if request.Header.Get("If-Unmodified-Since") != first {
				t.Errorf("If-Unmodified-Since is %q", request.Header.Get("If-Unmodified-Since"))
			}
		}
		response.Header().Set("Last-Modified", modified)
		if request.Header.Get("Range") == "bytes=0-0" {
			writeRange(response, payload, 0, 0)
			return
		}
		response.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = response.Write(payload)
	}))
	t.Cleanup(server.Close)
	store := openTestStore(t)
	download := persistedDownload(t, store, server.URL+"/modified.bin", t.TempDir(), "modified.bin")
	err := downloader.NewWithHTTPClient(store, http.DefaultClient).Download(context.Background(), download)
	var changed downloader.RepresentationChangedError
	if !errors.As(err, &changed) {
		t.Fatalf("changed Last-Modified returned %T: %v", err, err)
	}
}

func TestDownloadMapsPreconditionFailureToRepresentationChange(t *testing.T) {
	payload := makePayload(2048)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Range") == "bytes=0-0" {
			response.Header().Set("ETag", `"v1"`)
			writeRange(response, payload, 0, 0)
			return
		}
		if request.Header.Get("If-Match") != `"v1"` {
			t.Errorf("If-Match is %q", request.Header.Get("If-Match"))
		}
		response.WriteHeader(http.StatusPreconditionFailed)
	}))
	t.Cleanup(server.Close)
	store := openTestStore(t)
	download := persistedDownload(t, store, server.URL+"/precondition.bin", t.TempDir(), "precondition.bin")
	err := downloader.NewWithHTTPClient(store, http.DefaultClient).Download(context.Background(), download)
	var changed downloader.RepresentationChangedError
	if !errors.As(err, &changed) {
		t.Fatalf("precondition failure returned %T: %v", err, err)
	}
}

func TestRangeServerWithoutValidatorsUsesOneConsistentStream(t *testing.T) {
	payload := makePayload(4096)
	var dataRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Range") == "bytes=0-0" {
			writeRange(response, payload, 0, 0)
			return
		}
		dataRequests.Add(1)
		response.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = response.Write(payload)
	}))
	t.Cleanup(server.Close)
	store := openTestStore(t)
	download := persistedDownload(t, store, server.URL+"/unvalidated.bin", t.TempDir(), "unvalidated.bin")
	if err := parallelEngine(t, store, nil).Download(context.Background(), download); err != nil {
		t.Fatal(err)
	}
	if dataRequests.Load() != 1 {
		t.Fatalf("unvalidated resource used %d data requests", dataRequests.Load())
	}
	assertCompletedDownload(t, store, download, payload)
	persisted, err := store.Download(context.Background(), download.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.StatusCompleted {
		t.Fatalf("unvalidated single stream status is %s", persisted.Status)
	}
}

func TestResumeRejectsRepresentationChangeAfterPreflight(t *testing.T) {
	payload := makePayload(4096)
	parts := isolateDownloadState(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		current := requests.Add(1)
		start := int64(0)
		end := int64(0)
		if request.Header.Get("Range") == "bytes=512-" {
			start = 512
			end = int64(len(payload) - 1)
		} else if request.Header.Get("Range") != "bytes=0-0" {
			http.Error(response, "unexpected range", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		etag := `"v1"`
		if current > 1 {
			if request.Header.Get("If-Match") != `"v1"` {
				t.Errorf("resume If-Match is %q", request.Header.Get("If-Match"))
			}
			etag = `"v2"`
		}
		response.Header().Set("ETag", etag)
		response.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		response.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		response.WriteHeader(http.StatusPartialContent)
		_, _ = response.Write(payload[start : end+1])
	}))
	t.Cleanup(server.Close)

	store := openTestStore(t)
	download := persistedDownload(t, store, server.URL+"/resume.bin", t.TempDir(), "resume.bin")
	now := time.Now().UTC()
	if err := store.UpdateDownloadStatus(context.Background(), download.ID, model.StatusResolving, now, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateDownloadStatus(context.Background(), download.ID, model.StatusDownloading, now, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateRemoteMetadata(context.Background(), download.ID, int64(len(payload)), true, `"v1"`, "", now); err != nil {
		t.Fatal(err)
	}
	const downloaded = 512
	if err := store.UpdateDownloadProgress(context.Background(), download.ID, downloaded, now); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(parts, 0o700); err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(parts, download.ID.String()+".part")
	if err := os.WriteFile(partial, payload[:downloaded], 0o600); err != nil {
		t.Fatal(err)
	}
	download, err := store.Download(context.Background(), download.ID)
	if err != nil {
		t.Fatal(err)
	}
	err = downloader.NewWithHTTPClient(store, http.DefaultClient).Download(context.Background(), download)
	var changed downloader.RepresentationChangedError
	if !errors.As(err, &changed) {
		t.Fatalf("resume representation change returned %T: %v", err, err)
	}
	content, err := os.ReadFile(partial)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(payload[:downloaded]) {
		t.Fatal("resume modified partial data after validator mismatch")
	}
}

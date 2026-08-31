package integration_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/downloader"
	"github.com/kristyancarvalho/argo/internal/model"
	"github.com/kristyancarvalho/argo/internal/storage"
)

func TestSingleStreamHTTPDownloadIntegrity(t *testing.T) {
	payload := []byte("argo fixture payload\nwith multiple lines\n")
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write(payload)
	}))
	defer server.Close()

	store := openTestStore(t)
	download := persistedDownload(t, store, server.URL+"/artifact.bin", t.TempDir(), "artifact.bin")
	if err := downloader.New(store).Download(context.Background(), download); err != nil {
		t.Fatal(err)
	}
	assertCompletedDownload(t, store, download, payload)
}

func TestSingleStreamHTTPSDownload(t *testing.T) {
	payload := []byte("trusted TLS fixture")
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write(payload)
	}))
	defer server.Close()

	store := openTestStore(t)
	download := persistedDownload(t, store, server.URL+"/secure.bin", t.TempDir(), "secure.bin")
	engine := downloader.NewWithHTTPClient(store, server.Client())
	if err := engine.Download(context.Background(), download); err != nil {
		t.Fatal(err)
	}
	assertCompletedDownload(t, store, download, payload)
}

func TestSingleStreamDownloadFollowsRedirect(t *testing.T) {
	payload := []byte("redirect target")
	mux := http.NewServeMux()
	mux.HandleFunc("/redirect", func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, "/target.bin", http.StatusFound)
	})
	mux.HandleFunc("/target.bin", func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write(payload)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	store := openTestStore(t)
	download := persistedDownload(t, store, server.URL+"/redirect", t.TempDir(), "redirect")
	if err := downloader.New(store).Download(context.Background(), download); err != nil {
		t.Fatal(err)
	}
	assertCompletedDownload(t, store, download, payload)
}

func TestSingleStreamDownloadPreservesHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	store := openTestStore(t)
	download := persistedDownload(t, store, server.URL+"/failure.bin", t.TempDir(), "failure.bin")
	err := downloader.New(store).Download(context.Background(), download)
	var statusError downloader.HTTPStatusError
	if !errors.As(err, &statusError) {
		t.Fatalf("download returned %T, expected HTTPStatusError", err)
	}
	if statusError.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status code is %d, expected %d", statusError.StatusCode, http.StatusServiceUnavailable)
	}
	persisted, err := store.Download(context.Background(), download.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.StatusFailed || persisted.Error == "" {
		t.Fatalf("HTTP failure was not persisted: %+v", persisted)
	}
}

func TestSingleStreamDownloadKeepsPartialOnInterruptedConnection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Length", "1024")
		_, _ = response.Write(make([]byte, 128))
	}))
	defer server.Close()

	destination := t.TempDir()
	store := openTestStore(t)
	download := persistedDownload(t, store, server.URL+"/interrupted.bin", destination, "interrupted.bin")
	if err := downloader.New(store).Download(context.Background(), download); err == nil {
		t.Fatal("interrupted download succeeded")
	}
	persisted, err := store.Download(context.Background(), download.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.StatusFailed || persisted.DownloadedBytes != 128 {
		t.Fatalf("unexpected interrupted state: %+v", persisted)
	}
	partial := filepath.Join(destination, ".argo-"+download.ID.String()+".part")
	content, err := os.ReadFile(partial)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 128 {
		t.Fatalf("partial file has %d bytes, expected 128", len(content))
	}
}

func TestSingleStreamDownloadHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		flusher, ok := response.(http.Flusher)
		if !ok {
			t.Error("response writer does not support flushing")
			return
		}
		for index := 0; index < 100; index++ {
			if _, err := response.Write(make([]byte, 4096)); err != nil {
				return
			}
			flusher.Flush()
			select {
			case <-request.Context().Done():
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}))
	defer server.Close()

	store := openTestStore(t)
	download := persistedDownload(t, store, server.URL+"/slow.bin", t.TempDir(), "slow.bin")
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() {
		finished <- downloader.New(store).Download(ctx, download)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		persisted, err := store.Download(context.Background(), download.ID)
		if err != nil {
			t.Fatal(err)
		}
		if persisted.DownloadedBytes > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("download did not begin before cancellation")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	err := <-finished
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("download returned %v, expected context cancellation", err)
	}
	persisted, err := store.Download(context.Background(), download.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.StatusDownloading || persisted.DownloadedBytes == 0 {
		t.Fatalf("cancellation state was not persisted: %+v", persisted)
	}
}

func TestResumeRestartsWhenServerIgnoresRange(t *testing.T) {
	payload := []byte("complete payload from a server without range support")
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write(payload)
	}))
	defer server.Close()

	destination := t.TempDir()
	store := openTestStore(t)
	download := persistedDownload(t, store, server.URL+"/fallback.bin", destination, "fallback.bin")
	startedAt := time.Now().UTC()
	if err := store.UpdateDownloadStatus(
		context.Background(),
		download.ID,
		model.StatusDownloading,
		startedAt,
		"",
	); err != nil {
		t.Fatal(err)
	}
	partialContent := []byte("stale")
	if err := os.WriteFile(
		filepath.Join(destination, ".argo-"+download.ID.String()+".part"),
		partialContent,
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateDownloadProgress(
		context.Background(),
		download.ID,
		int64(len(partialContent)),
		startedAt,
	); err != nil {
		t.Fatal(err)
	}
	download, err := store.Download(context.Background(), download.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := downloader.New(store).Download(context.Background(), download); err != nil {
		t.Fatal(err)
	}
	assertCompletedDownload(t, store, download, payload)
}

func TestSingleStreamRateLimit(t *testing.T) {
	payload := make([]byte, 32*1024)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write(payload)
	}))
	defer server.Close()

	store := openTestStore(t)
	download := persistedDownload(t, store, server.URL+"/limited.bin", t.TempDir(), "limited.bin")
	startedAt := time.Now()
	engine := downloader.NewWithRateLimit(store, http.DefaultClient, 64*1024)
	if err := engine.Download(context.Background(), download); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(startedAt)
	if elapsed < 400*time.Millisecond {
		t.Fatalf("rate-limited transfer finished too quickly in %s", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("rate-limited transfer exceeded tolerance at %s", elapsed)
	}
}

func persistedDownload(
	t *testing.T,
	store *storage.Store,
	rawURL string,
	destination string,
	filename string,
) model.Download {
	t.Helper()
	id, err := model.NewDownloadID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	download := model.Download{
		ID:              id,
		URL:             rawURL,
		Destination:     destination,
		Filename:        filename,
		TotalSize:       -1,
		DownloadedBytes: 0,
		Status:          model.StatusQueued,
		Priority:        model.PriorityNormal,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := store.CreateDownload(context.Background(), download); err != nil {
		t.Fatal(err)
	}

	return download
}

func assertCompletedDownload(
	t *testing.T,
	store *storage.Store,
	download model.Download,
	payload []byte,
) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(download.Destination, download.Filename))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(payload) {
		t.Fatalf("downloaded content %q does not match payload %q", content, payload)
	}
	persisted, err := store.Download(context.Background(), download.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.StatusCompleted ||
		persisted.DownloadedBytes != int64(len(payload)) ||
		persisted.TotalSize != int64(len(payload)) {
		t.Fatalf("unexpected completed state: %s", fmt.Sprintf("%+v", persisted))
	}
	partial := filepath.Join(download.Destination, ".argo-"+download.ID.String()+".part")
	if _, err := os.Stat(partial); !os.IsNotExist(err) {
		t.Fatalf("partial file remains after completion: %v", err)
	}
}

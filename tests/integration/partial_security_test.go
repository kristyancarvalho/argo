package integration_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/downloader"
	"github.com/kristyancarvalho/argo/internal/model"
	"github.com/kristyancarvalho/argo/internal/storage"
)

func TestPartialSymlinkIsRejectedWithoutModifyingTarget(t *testing.T) {
	payload := makePayload(4096)
	for _, parallel := range []bool{false, true} {
		t.Run(map[bool]string{false: "single", true: "parallel"}[parallel], func(t *testing.T) {
			parts := t.TempDir()
			victim := filepath.Join(t.TempDir(), "victim")
			original := []byte("unrelated content")
			if err := os.WriteFile(victim, original, 0o600); err != nil {
				t.Fatal(err)
			}
			store := openTestStore(t)
			destination := t.TempDir()
			server := securityServer(t, payload, parallel)
			download := persistedDownload(t, store, server.URL+"/partial.bin", destination, "partial.bin")
			if err := os.Symlink(victim, filepath.Join(parts, download.ID.String()+".part")); err != nil {
				t.Fatal(err)
			}
			engine := securityEngine(t, store, parts, parallel, nil)
			if err := engine.Download(context.Background(), download); err == nil {
				t.Fatal("partial symlink was accepted")
			}
			assertFileContent(t, victim, original)
			if _, err := os.Lstat(filepath.Join(destination, "partial.bin")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unsafe transfer produced a final file: %v", err)
			}
		})
	}
}

func TestPartialDirectorySymlinkIsRejected(t *testing.T) {
	payload := makePayload(2048)
	parent := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(parent, "redirect")); err != nil {
		t.Fatal(err)
	}
	parts := filepath.Join(parent, "redirect", "parts")
	store := openTestStore(t)
	server := securityServer(t, payload, false)
	download := persistedDownload(t, store, server.URL+"/parent.bin", t.TempDir(), "parent.bin")
	engine := securityEngine(t, store, parts, false, nil)
	if err := engine.Download(context.Background(), download); err == nil {
		t.Fatal("symlinked partial directory was accepted")
	}
	if _, err := os.Stat(filepath.Join(target, "parts")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial state escaped through parent symlink: %v", err)
	}
}

func TestSingleTransferRejectsPartialPathSwap(t *testing.T) {
	payload := makePayload(512 * 1024)
	parts := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	original := []byte("victim remains unchanged")
	if err := os.WriteFile(victim, original, 0o600); err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t)
	destination := t.TempDir()
	server := slowSecurityServer(t, payload)
	download := persistedDownload(t, store, server.URL+"/swap.bin", destination, "swap.bin")
	engine := securityEngine(t, store, parts, false, nil)
	finished := make(chan error, 1)
	go func() {
		finished <- engine.Download(context.Background(), download)
	}()
	partial := filepath.Join(parts, download.ID.String()+".part")
	waitForPartialData(t, partial)
	retained := filepath.Join(parts, "retained")
	if err := os.Rename(partial, retained); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, partial); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err == nil || !strings.Contains(err.Error(), "partial path") {
		t.Fatalf("partial path swap returned %v", err)
	}
	assertFileContent(t, victim, original)
	if info, err := os.Lstat(filepath.Join(destination, "swap.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path swap produced final file %+v: %v", info, err)
	}
}

func TestParallelTransferRejectsPartialPathSwap(t *testing.T) {
	payload := makePayload(256 * 1024)
	parts := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	original := []byte("parallel victim remains unchanged")
	if err := os.WriteFile(victim, original, 0o600); err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t)
	destination := t.TempDir()
	server := parallelServer(t, payload, func(response http.ResponseWriter, _ *http.Request, start, end int64) {
		writeRange(response, payload, start, end)
	})
	download := persistedDownload(t, store, server.URL+"/parallel-swap.bin", destination, "parallel-swap.bin")
	partial := filepath.Join(parts, download.ID.String()+".part")
	var swap sync.Once
	var swapError error
	observer := func(_ model.DownloadID, _ downloader.ChunkProgress) {
		swap.Do(func() {
			swapError = os.Rename(partial, filepath.Join(parts, "retained"))
			if swapError == nil {
				swapError = os.Symlink(victim, partial)
			}
		})
	}
	engine := securityEngine(t, store, parts, true, observer)
	err := engine.Download(context.Background(), download)
	if swapError != nil {
		t.Fatal(swapError)
	}
	if err == nil || !strings.Contains(err.Error(), "partial path") {
		t.Fatalf("parallel partial path swap returned %v", err)
	}
	assertFileContent(t, victim, original)
	if _, err := os.Lstat(filepath.Join(destination, "parallel-swap.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("parallel path swap produced a final file: %v", err)
	}
}

func securityEngine(
	t *testing.T,
	store *storage.Store,
	parts string,
	parallel bool,
	observer func(model.DownloadID, downloader.ChunkProgress),
) *downloader.Engine {
	t.Helper()
	options := downloader.Options{
		HTTPClient:       http.DefaultClient,
		MaximumChunks:    1,
		MinimumChunkSize: 1024,
		ChunkProgress:    observer,
		PartsDirectory:   parts,
	}
	if parallel {
		options.MaximumChunks = 4
	}
	engine, err := downloader.NewWithOptions(store, options)
	if err != nil {
		t.Fatal(err)
	}

	return engine
}

func securityServer(t *testing.T, payload []byte, parallel bool) *httptest.Server {
	t.Helper()
	if parallel {
		return parallelServer(t, payload, func(response http.ResponseWriter, _ *http.Request, start, end int64) {
			writeRange(response, payload, start, end)
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Length", "4096")
		if request.Header.Get("Range") != "bytes=0-0" {
			_, _ = response.Write(payload)
		}
	}))
	t.Cleanup(server.Close)

	return server
}

func slowSecurityServer(t *testing.T, payload []byte) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Length", "524288")
		if request.Header.Get("Range") == "bytes=0-0" {
			return
		}
		flusher, _ := response.(http.Flusher)
		for offset := 0; offset < len(payload); offset += 4096 {
			end := min(offset+4096, len(payload))
			if _, err := response.Write(payload[offset:end]); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(time.Millisecond)
		}
	}))
	t.Cleanup(server.Close)

	return server
}

func waitForPartialData(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		info, err := os.Stat(path)
		if err == nil && info.Size() > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("partial file did not receive data")
}

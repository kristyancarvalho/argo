package integration_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/downloader"
	"github.com/kristyancarvalho/argo/internal/model"
)

func TestResumeRequiresByteCompatibleValidator(t *testing.T) {
	for _, test := range []struct {
		name, etag, modified string
		reuse                bool
	}{
		{name: "weak only", etag: `W/"same"`},
		{name: "absent"},
		{name: "strong", etag: `"same"`, reuse: true},
		{name: "weak with modification time", etag: `W/"same"`, modified: "Tue, 08 Sep 2026 12:00:00 GMT", reuse: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			payload := []byte("BBBBBBBB")
			var mutex sync.Mutex
			var ranges []string
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				mutex.Lock()
				ranges = append(ranges, request.Header.Get("Range"))
				mutex.Unlock()
				writer.Header().Set("ETag", test.etag)
				writer.Header().Set("Last-Modified", test.modified)
				http.ServeContent(writer, request, "file.bin", time.Time{}, bytes.NewReader(payload))
			}))
			defer server.Close()
			store := openTestStore(t)
			download := persistedDownload(t, store, server.URL, t.TempDir(), "file.bin")
			for _, err := range []error{
				store.UpdateRemoteMetadata(ctx, download.ID, 8, true, test.etag, test.modified, time.Now()),
				store.UpdateDownloadStatus(ctx, download.ID, model.StatusDownloading, time.Now(), ""),
				store.UpdateDownloadProgress(ctx, download.ID, 4, time.Now()),
			} {
				if err != nil {
					t.Fatal(err)
				}
			}
			download, err := store.Download(ctx, download.ID)
			if err != nil {
				t.Fatal(err)
			}
			parts := t.TempDir()
			prefix := []byte("AAAA")
			if test.reuse {
				prefix = payload[:4]
			}
			if err := os.WriteFile(filepath.Join(parts, download.ID.String()+".part"), prefix, 0o600); err != nil {
				t.Fatal(err)
			}
			engine, err := downloader.NewWithOptions(store, downloader.Options{MaximumChunks: 1, PartsDirectory: parts})
			if err != nil {
				t.Fatal(err)
			}
			canceled := download
			canceled.Status = model.StatusCanceled
			validationErr := engine.ValidateCanceledResume(ctx, canceled)
			if test.reuse && validationErr != nil {
				t.Fatal(validationErr)
			}
			if !test.reuse {
				var unavailable downloader.ResumeUnavailableError
				if !errors.As(validationErr, &unavailable) {
					t.Fatalf("unsafe canceled resume accepted: %v", validationErr)
				}
			}
			if err := engine.Download(ctx, download); err != nil {
				t.Fatal(err)
			}
			assertCompletedDownload(t, store, download, payload)
			mutex.Lock()
			defer mutex.Unlock()
			last := ranges[len(ranges)-1]
			if test.reuse && last != "bytes=4-" || !test.reuse && last != "" {
				t.Fatalf("unexpected transfer range %q", last)
			}
		})
	}
}

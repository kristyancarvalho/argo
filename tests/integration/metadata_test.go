package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/kristyancarvalho/argo/internal/downloader"
)

func TestRangeCapabilityAndValidatorsArePersisted(t *testing.T) {
	payload := []byte("range-capable payload")
	etag := `"metadata-v1"`
	lastModified := "Mon, 31 Aug 2026 12:00:00 GMT"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("ETag", etag)
		response.Header().Set("Last-Modified", lastModified)
		if request.Header.Get("Range") == "bytes=0-0" {
			response.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", len(payload)))
			response.Header().Set("Content-Length", "1")
			response.WriteHeader(http.StatusPartialContent)
			_, _ = response.Write(payload[:1])
			return
		}
		response.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = response.Write(payload)
	}))
	defer server.Close()

	store := openTestStore(t)
	download := persistedDownload(t, store, server.URL+"/metadata.bin", t.TempDir(), "metadata.bin")
	if err := downloader.New(store).Download(context.Background(), download); err != nil {
		t.Fatal(err)
	}
	persisted, err := store.Download(context.Background(), download.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.TotalSize != int64(len(payload)) || !persisted.RangeSupported {
		t.Fatalf("unexpected remote capability metadata: %+v", persisted)
	}
	if persisted.ETag != etag || persisted.LastModified != lastModified {
		t.Fatalf("unexpected validators: ETag %q, Last-Modified %q", persisted.ETag, persisted.LastModified)
	}
}

func TestInspectorDetectsNonRangeServerWithoutValidators(t *testing.T) {
	payload := []byte("ordinary response")
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = response.Write(payload)
	}))
	defer server.Close()

	metadata, err := downloader.NewInspector(server.Client()).Inspect(context.Background(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.RangeSupported || metadata.TotalSize != int64(len(payload)) {
		t.Fatalf("unexpected non-Range metadata: %+v", metadata)
	}
	if metadata.ETag != "" || metadata.LastModified != "" {
		t.Fatalf("missing validators were not preserved as empty: %+v", metadata)
	}
}

func TestInspectorHandlesUnknownContentLength(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		flusher, ok := response.(http.Flusher)
		if !ok {
			t.Error("response writer cannot flush")
			return
		}
		flusher.Flush()
		_, _ = response.Write([]byte("chunked payload"))
	}))
	defer server.Close()

	metadata, err := downloader.NewInspector(server.Client()).Inspect(context.Background(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.TotalSize != -1 || metadata.RangeSupported {
		t.Fatalf("unexpected unknown-length metadata: %+v", metadata)
	}
}

package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"math/rand"
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

func BenchmarkDownloadCheckpointThroughput(b *testing.B) {
	payload := make([]byte, 64<<20)
	_, _ = rand.New(rand.NewSource(41)).Read(payload)
	expected := sha256.Sum256(payload)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("ETag", `"benchmark"`)
		http.ServeContent(writer, request, "file.bin", time.Time{}, bytes.NewReader(payload))
	}))
	defer server.Close()
	for _, chunks := range []int{1, 4} {
		b.Run(fmt.Sprint(chunks), func(b *testing.B) {
			root := b.TempDir()
			ctx := context.Background()
			database, err := storage.Open(ctx, filepath.Join(root, "argo.db"))
			if err != nil {
				b.Fatal(err)
			}
			defer func() { _ = database.Close() }()
			store := &checkpointStore{Store: database}
			engine, err := downloader.NewWithOptions(store, downloader.Options{PartsDirectory: filepath.Join(root, "parts"), MaximumChunks: chunks})
			if err != nil {
				b.Fatal(err)
			}
			b.SetBytes(int64(len(payload)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				b.StopTimer()
				id, err := model.NewDownloadID()
				if err != nil {
					b.Fatal(err)
				}
				download := model.Download{ID: id, URL: server.URL, Destination: root, Filename: id.String() + ".bin",
					TotalSize: -1, Status: model.StatusQueued, Priority: model.PriorityNormal, CreatedAt: time.Now(), UpdatedAt: time.Now()}
				if err := store.CreateDownload(ctx, download); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				if err := engine.Download(ctx, download); err != nil {
					b.Fatal(err)
				}
				b.StopTimer()
				path := filepath.Join(root, download.Filename)
				file, err := os.Open(path)
				if err != nil {
					b.Fatal(err)
				}
				hash := sha256.New()
				_, err = io.Copy(hash, file)
				closeError := file.Close()
				if err != nil || closeError != nil || !bytes.Equal(hash.Sum(nil), expected[:]) {
					b.Fatalf("invalid completed content: %v, %v", err, closeError)
				}
				if err := os.Remove(path); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
			}
			b.StopTimer()
			b.ReportMetric(float64(store.calls.Load())/float64(b.N), "checkpoints/op")
		})
	}
}

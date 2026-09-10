package integration_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/daemon"
	"github.com/kristyancarvalho/argo/internal/downloader"
	"github.com/kristyancarvalho/argo/internal/ipc"
	"github.com/kristyancarvalho/argo/internal/model"
)

func TestAddDownloadThroughDaemonIPC(t *testing.T) {
	payload := []byte("daemon-managed download")
	httpServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write(payload)
	}))
	defer httpServer.Close()

	store := openTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	service, err := daemon.NewService(ctx, store, downloader.New(store))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		if err := service.Close(); err != nil {
			t.Error(err)
		}
	})
	socketDirectory := t.TempDir()
	if err := os.Chmod(socketDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(socketDirectory, "argod.sock")
	server, err := ipc.Listen(socketPath, service)
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		finished <- server.Serve(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-finished:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("IPC server did not stop")
		}
	})

	destination := t.TempDir()
	client := ipc.NewClient(socketPath)
	_, err = client.Add(context.Background(), "file:///tmp/payload.bin", destination)
	var remoteError ipc.RemoteError
	if !errors.As(err, &remoteError) || remoteError.Code != "invalid_request" {
		t.Fatalf("invalid URL returned %v, expected invalid_request", err)
	}
	_, err = client.AddWithChecksum(
		context.Background(),
		httpServer.URL+"/payload.bin",
		destination,
		"sha256:invalid",
	)
	if !errors.As(err, &remoteError) || remoteError.Code != "invalid_request" {
		t.Fatalf("invalid checksum returned %v, expected invalid_request", err)
	}

	added, err := client.Add(
		context.Background(),
		httpServer.URL+"/payload.bin",
		destination,
	)
	if err != nil {
		t.Fatal(err)
	}
	identifier, err := model.ParseDownloadID(added.ID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		download, err := store.Download(context.Background(), identifier)
		if err != nil {
			t.Fatal(err)
		}
		if download.Status == model.StatusCompleted {
			if download.DownloadedBytes != int64(len(payload)) {
				t.Fatalf("downloaded %d bytes, expected %d", download.DownloadedBytes, len(payload))
			}
			break
		}
		if download.Status == model.StatusFailed {
			t.Fatalf("daemon download failed: %s", download.Error)
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon download remained %s", download.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

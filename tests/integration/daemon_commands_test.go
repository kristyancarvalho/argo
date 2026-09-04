package integration_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kristyancarvalho/argo/internal/ipc"
	"github.com/kristyancarvalho/argo/internal/model"
)

func TestDaemonListShowAndCancelCommands(t *testing.T) {
	isolateDownloadState(t)
	payload := makePayload(512 * 1024)
	httpServer, _ := rangeFixtureServer(t, payload)
	store := openTestStore(t)
	running := startDownloadDaemon(t, store)
	defer stopDownloadDaemon(t, running)

	added, err := running.client.Add(context.Background(), httpServer.URL+"/commands.bin", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	identifier, err := model.ParseDownloadID(added.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitForDownload(t, store, identifier, func(download model.Download) bool {
		return download.Status == model.StatusDownloading && download.DownloadedBytes > 0
	})

	downloads, err := running.client.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(downloads) != 1 || downloads[0].ID != identifier.String() {
		t.Fatalf("unexpected list response: %+v", downloads)
	}
	shown, err := running.client.Show(context.Background(), identifier.String())
	if err != nil {
		t.Fatal(err)
	}
	if shown.ID != identifier.String() || shown.Filename != "commands.bin" {
		t.Fatalf("unexpected show response: %+v", shown)
	}
	priority, err := running.client.Priority(context.Background(), identifier.String(), string(model.PriorityHigh))
	if err != nil {
		t.Fatal(err)
	}
	if priority.Priority != string(model.PriorityHigh) {
		t.Fatalf("priority response is %q, expected high", priority.Priority)
	}
	_, err = running.client.Priority(context.Background(), identifier.String(), "urgent")
	var remoteError ipc.RemoteError
	if !errors.As(err, &remoteError) || remoteError.Code != "invalid_priority" {
		t.Fatalf("invalid priority returned %v", err)
	}
	canceled, err := running.client.Cancel(context.Background(), identifier.String())
	if err != nil {
		t.Fatal(err)
	}
	if canceled.Status != string(model.StatusCanceled) {
		t.Fatalf("cancel returned status %q", canceled.Status)
	}
	waitForDownload(t, store, identifier, func(download model.Download) bool {
		return download.Status == model.StatusCanceled
	})
}

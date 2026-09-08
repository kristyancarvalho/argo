package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/daemon"
	"github.com/kristyancarvalho/argo/internal/ipc"
	"github.com/kristyancarvalho/argo/internal/model"
	"github.com/kristyancarvalho/argo/internal/storage"
)

func TestDownloadHistoryIsRetrievedAcrossBoundedPages(t *testing.T) {
	store := openTestStore(t)
	createdAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	for index := range 501 {
		filename := fmt.Sprintf("history-%03d.bin", index)
		if index == 0 {
			filename = strings.Repeat("f", 10_000) + ".bin"
		}
		createPaginatedHistoryDownload(t, store, index, createdAt.Add(time.Duration(index)*time.Second), filename)
	}
	service, err := daemon.NewService(context.Background(), store, newControlledDownloadEngine(store))
	if err != nil {
		t.Fatal(err)
	}
	defer closeSchedulerService(t, service)
	server, cancel, finished := startIPCServerWithHandler(t, service)
	defer stopIPCServer(t, server, cancel, finished)

	downloads, err := ipc.NewClient(server.SocketPath()).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(downloads) != 501 {
		t.Fatalf("received %d downloads, expected 501", len(downloads))
	}
	seen := make(map[string]struct{}, len(downloads))
	for index, download := range downloads {
		expectedID := fmt.Sprintf("%032x", index+1)
		if download.ID != expectedID {
			t.Fatalf("download %d has ID %s, expected %s", index, download.ID, expectedID)
		}
		if download.URL != "" || download.Destination != "" || download.Error != "" {
			t.Fatalf("download summary contains detail-only fields: %+v", download)
		}
		if _, exists := seen[download.ID]; exists {
			t.Fatalf("duplicate download %s", download.ID)
		}
		seen[download.ID] = struct{}{}
	}
	if len([]rune(downloads[0].Filename)) > 1025 {
		t.Fatalf("summary filename was not bounded: %d runes", len([]rune(downloads[0].Filename)))
	}
}

func TestDownloadPaginationRemainsStableDuringHistoryChanges(t *testing.T) {
	store := openTestStore(t)
	createdAt := time.Date(2026, time.February, 3, 4, 5, 6, 0, time.UTC)
	for index := range 5 {
		createPaginatedHistoryDownload(t, store, index, createdAt.Add(time.Duration(index)*time.Second), fmt.Sprintf("item-%d", index))
	}
	service, err := daemon.NewService(context.Background(), store, newControlledDownloadEngine(store))
	if err != nil {
		t.Fatal(err)
	}
	defer closeSchedulerService(t, service)

	first := requestDownloadPage(t, service, "", 2)
	if len(first.Downloads) != 2 || first.NextCursor == "" {
		t.Fatalf("unexpected first page: %+v", first)
	}
	createPaginatedHistoryDownload(t, store, 5, createdAt.Add(5*time.Second), "inserted")
	deleted, err := model.ParseDownloadID(fmt.Sprintf("%032x", 5))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteDownload(context.Background(), deleted); err != nil {
		t.Fatal(err)
	}

	all := append([]ipc.Download(nil), first.Downloads...)
	cursor := first.NextCursor
	for cursor != "" {
		page := requestDownloadPage(t, service, cursor, 2)
		encoded, err := json.Marshal(page)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) >= 1<<20 {
			t.Fatalf("page response is not bounded: %d bytes", len(encoded))
		}
		all = append(all, page.Downloads...)
		cursor = page.NextCursor
	}
	wanted := []string{
		fmt.Sprintf("%032x", 1),
		fmt.Sprintf("%032x", 2),
		fmt.Sprintf("%032x", 3),
		fmt.Sprintf("%032x", 4),
		fmt.Sprintf("%032x", 6),
	}
	if len(all) != len(wanted) {
		t.Fatalf("received IDs %+v, expected %+v", downloadIDs(all), wanted)
	}
	for index, identifier := range wanted {
		if all[index].ID != identifier {
			t.Fatalf("received IDs %+v, expected %+v", downloadIDs(all), wanted)
		}
	}
}

func createPaginatedHistoryDownload(
	t *testing.T,
	store *storage.Store,
	index int,
	createdAt time.Time,
	filename string,
) {
	t.Helper()
	identifier, err := model.ParseDownloadID(fmt.Sprintf("%032x", index+1))
	if err != nil {
		t.Fatal(err)
	}
	download := model.Download{
		ID:              identifier,
		URL:             "https://example.test/" + fmt.Sprintf("%d", index) + "?token=" + strings.Repeat("x", 2_048),
		Destination:     "/tmp/history destination",
		Filename:        filename,
		TotalSize:       1,
		DownloadedBytes: 1,
		Status:          model.StatusCompleted,
		Priority:        model.PriorityNormal,
		CreatedAt:       createdAt,
		UpdatedAt:       createdAt,
		CompletedAt:     createdAt,
		Error:           strings.Repeat("failure detail ", 32),
	}
	if err := store.CreateDownload(context.Background(), download); err != nil {
		t.Fatal(err)
	}
}

func requestDownloadPage(t *testing.T, service *daemon.Service, cursor string, limit int) ipc.ListResponse {
	t.Helper()
	payload, err := json.Marshal(ipc.ListRequest{Cursor: cursor, Limit: limit})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Handle(context.Background(), ipc.Request{Operation: ipc.OperationList, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	response, valid := result.(ipc.ListResponse)
	if !valid {
		t.Fatalf("list returned %T", result)
	}

	return response
}

func downloadIDs(downloads []ipc.Download) []string {
	identifiers := make([]string, 0, len(downloads))
	for _, download := range downloads {
		identifiers = append(identifiers, download.ID)
	}

	return identifiers
}

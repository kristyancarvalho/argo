package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/model"
	"github.com/kristyancarvalho/argo/internal/storage"
)

func TestFreshDatabaseCreation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "argo.db")
	store, err := storage.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})

	version, err := store.SchemaVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if version != 4 {
		t.Fatalf("schema version is %d, expected 4", version)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("database permissions are %o, expected 600", info.Mode().Perm())
	}
}

func TestDatabaseMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argo.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE schema_migrations (
        version INTEGER PRIMARY KEY,
        applied_at TEXT NOT NULL
    )`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("INSERT INTO schema_migrations(version, applied_at) VALUES (1, 'now')"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE downloads (
        id TEXT PRIMARY KEY,
        status TEXT NOT NULL,
        priority TEXT NOT NULL,
        created_at TEXT NOT NULL
    )`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := storage.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	database, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	var version int
	if err := database.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 4 {
		t.Fatalf("schema version is %d, expected 4", version)
	}
	var indexName string
	if err := database.QueryRow(
		"SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?",
		"downloads_status_priority_created_idx",
	).Scan(&indexName); err != nil {
		t.Fatal(err)
	}
}

func TestCreateReadAndUpdateDownload(t *testing.T) {
	store := openTestStore(t)
	download := testDownload(t, time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC))
	if err := store.CreateDownload(context.Background(), download); err != nil {
		t.Fatal(err)
	}

	read, err := store.Download(context.Background(), download.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertDownloadEqual(t, read, download)

	progressAt := download.UpdatedAt.Add(time.Second)
	if err := store.UpdateDownloadProgress(context.Background(), download.ID, 512, progressAt); err != nil {
		t.Fatal(err)
	}
	startedAt := progressAt.Add(time.Second)
	if err := store.UpdateDownloadStatus(
		context.Background(),
		download.ID,
		model.StatusDownloading,
		startedAt,
		"",
	); err != nil {
		t.Fatal(err)
	}
	completedAt := startedAt.Add(time.Second)
	if err := store.UpdateDownloadProgress(context.Background(), download.ID, download.TotalSize, completedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateDownloadStatus(
		context.Background(),
		download.ID,
		model.StatusCompleted,
		completedAt,
		"",
	); err != nil {
		t.Fatal(err)
	}

	updated, err := store.Download(context.Background(), download.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.DownloadedBytes != download.TotalSize || updated.Status != model.StatusCompleted {
		t.Fatalf("unexpected updated download: %+v", updated)
	}
	if !updated.StartedAt.Equal(startedAt) || !updated.CompletedAt.Equal(completedAt) {
		t.Fatalf("unexpected lifecycle times: started %s, completed %s", updated.StartedAt, updated.CompletedAt)
	}
}

func TestDownloadRestorationAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argo.db")
	first, err := storage.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	download := testDownload(t, time.Date(2026, 8, 31, 13, 0, 0, 0, time.UTC))
	if err := first.CreateDownload(context.Background(), download); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := storage.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := restarted.Close(); err != nil {
			t.Error(err)
		}
	})
	downloads, err := restarted.Downloads(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(downloads) != 1 {
		t.Fatalf("restored %d downloads, expected 1", len(downloads))
	}
	assertDownloadEqual(t, downloads[0], download)
}

func TestChunkProgressRestorationAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argo.db")
	first, err := storage.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	download := testDownload(t, time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC))
	if err := first.CreateDownload(context.Background(), download); err != nil {
		t.Fatal(err)
	}
	chunks := []model.DownloadChunk{
		{DownloadID: download.ID, Index: 0, Start: 0, End: 511, DownloadedBytes: 128},
		{DownloadID: download.ID, Index: 1, Start: 512, End: 1023},
	}
	if err := first.ReplaceDownloadChunks(context.Background(), download.ID, chunks, download.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if err := first.UpdateChunkProgress(context.Background(), download.ID, 1, 256, download.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := storage.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, restarted)
	restored, err := restarted.DownloadChunks(context.Background(), download.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 2 || restored[0].DownloadedBytes != 128 || restored[1].DownloadedBytes != 256 {
		t.Fatalf("unexpected restored chunks: %+v", restored)
	}
	persisted, err := restarted.Download(context.Background(), download.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.DownloadedBytes != 384 {
		t.Fatalf("aggregate progress is %d, expected 384", persisted.DownloadedBytes)
	}
}

func TestStorageRejectsInvalidUpdates(t *testing.T) {
	store := openTestStore(t)
	download := testDownload(t, time.Now().UTC())
	if err := store.CreateDownload(context.Background(), download); err != nil {
		t.Fatal(err)
	}

	err := store.UpdateDownloadProgress(context.Background(), download.ID, download.TotalSize+1, time.Now())
	var progressError storage.InvalidProgressError
	if !errors.As(err, &progressError) {
		t.Fatalf("progress update returned %T, expected InvalidProgressError", err)
	}
	err = store.UpdateDownloadStatus(
		context.Background(),
		download.ID,
		model.StatusCompleted,
		time.Now(),
		"",
	)
	var transitionError model.InvalidTransitionError
	if !errors.As(err, &transitionError) {
		t.Fatalf("status update returned %T, expected InvalidTransitionError", err)
	}
}

func openTestStore(t *testing.T) *storage.Store {
	t.Helper()
	store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "argo.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})

	return store
}

func testDownload(t *testing.T, createdAt time.Time) model.Download {
	t.Helper()
	id, err := model.NewDownloadID()
	if err != nil {
		t.Fatal(err)
	}

	return model.Download{
		ID:              id,
		URL:             "https://example.test/archive.tar",
		Destination:     "/tmp/downloads",
		Filename:        "archive.tar",
		TotalSize:       1024,
		DownloadedBytes: 0,
		Status:          model.StatusQueued,
		Priority:        model.PriorityNormal,
		CreatedAt:       createdAt,
		UpdatedAt:       createdAt,
		ETag:            `"fixture-v1"`,
		LastModified:    "Sun, 31 Aug 2026 12:00:00 GMT",
		RangeSupported:  true,
	}
}

func assertDownloadEqual(t *testing.T, actual, expected model.Download) {
	t.Helper()
	if actual.ID != expected.ID ||
		actual.URL != expected.URL ||
		actual.Destination != expected.Destination ||
		actual.Filename != expected.Filename ||
		actual.TotalSize != expected.TotalSize ||
		actual.DownloadedBytes != expected.DownloadedBytes ||
		actual.Status != expected.Status ||
		actual.Priority != expected.Priority ||
		!actual.CreatedAt.Equal(expected.CreatedAt) ||
		!actual.UpdatedAt.Equal(expected.UpdatedAt) ||
		!actual.StartedAt.Equal(expected.StartedAt) ||
		!actual.CompletedAt.Equal(expected.CompletedAt) ||
		actual.ETag != expected.ETag ||
		actual.LastModified != expected.LastModified ||
		actual.RangeSupported != expected.RangeSupported ||
		actual.Error != expected.Error {
		t.Fatalf("downloads differ:\nactual:   %+v\nexpected: %+v", actual, expected)
	}
}

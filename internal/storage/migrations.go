package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type migration struct {
	version    int
	statements []string
}

var migrations = []migration{
	{
		version: 1,
		statements: []string{
			`CREATE TABLE downloads (
                id TEXT PRIMARY KEY,
                url TEXT NOT NULL,
                destination TEXT NOT NULL,
                filename TEXT NOT NULL,
                total_size INTEGER NOT NULL CHECK (total_size >= -1),
                downloaded_bytes INTEGER NOT NULL CHECK (
                    downloaded_bytes >= 0 AND
                    (total_size = -1 OR downloaded_bytes <= total_size)
                ),
                status TEXT NOT NULL CHECK (
                    status IN ('queued', 'resolving', 'downloading', 'paused', 'completed', 'failed', 'canceled')
                ),
                priority TEXT NOT NULL CHECK (priority IN ('low', 'normal', 'high')),
                created_at TEXT NOT NULL,
                updated_at TEXT NOT NULL,
                started_at TEXT,
                completed_at TEXT,
                etag TEXT NOT NULL,
                last_modified TEXT NOT NULL,
                error_message TEXT NOT NULL
            )`,
		},
	},
	{
		version: 2,
		statements: []string{
			`CREATE INDEX downloads_status_priority_created_idx
             ON downloads (status, priority, created_at, id)`,
		},
	},
	{
		version: 3,
		statements: []string{
			`ALTER TABLE downloads
             ADD COLUMN range_supported INTEGER NOT NULL DEFAULT 0
             CHECK (range_supported IN (0, 1))`,
		},
	},
}

func migrate(ctx context.Context, database *sql.DB) error {
	if _, err := database.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
        version INTEGER PRIMARY KEY,
        applied_at TEXT NOT NULL
    )`); err != nil {
		return fmt.Errorf("initialize schema migrations: %w", err)
	}

	var version int
	if err := database.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > len(migrations) {
		return SchemaTooNewError{Found: version, Supported: len(migrations)}
	}

	for _, change := range migrations {
		if change.version <= version {
			continue
		}
		if err := applyMigration(ctx, database, change); err != nil {
			return err
		}
	}

	return nil
}

func applyMigration(ctx context.Context, database *sql.DB, change migration) error {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin schema migration %d: %w", change.version, err)
	}

	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()

	for _, statement := range change.statements {
		if _, err := transaction.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply schema migration %d: %w", change.version, err)
		}
	}
	if _, err := transaction.ExecContext(
		ctx,
		"INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)",
		change.version,
		formatTime(time.Now()),
	); err != nil {
		return fmt.Errorf("record schema migration %d: %w", change.version, err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit schema migration %d: %w", change.version, err)
	}
	committed = true

	return nil
}

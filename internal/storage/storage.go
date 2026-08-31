package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	database *sql.DB
}

func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("database path is empty")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}

	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	database.SetMaxOpenConns(1)

	closeOnError := func(openErr error) (*Store, error) {
		if closeErr := database.Close(); closeErr != nil {
			return nil, errors.Join(openErr, fmt.Errorf("close database: %w", closeErr))
		}
		return nil, openErr
	}

	if err := database.PingContext(ctx); err != nil {
		return closeOnError(fmt.Errorf("connect to database: %w", err))
	}
	if _, err := database.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return closeOnError(fmt.Errorf("enable foreign keys: %w", err))
	}
	if _, err := database.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return closeOnError(fmt.Errorf("configure busy timeout: %w", err))
	}
	if err := migrate(ctx, database); err != nil {
		return closeOnError(err)
	}
	if path != ":memory:" {
		if err := os.Chmod(path, 0o600); err != nil {
			return closeOnError(fmt.Errorf("secure database permissions: %w", err))
		}
	}

	return &Store{database: database}, nil
}

func (store *Store) Close() error {
	if err := store.database.Close(); err != nil {
		return fmt.Errorf("close database: %w", err)
	}

	return nil
}

func (store *Store) SchemaVersion(ctx context.Context) (int, error) {
	var version int
	if err := store.database.QueryRowContext(
		ctx,
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations",
	).Scan(&version); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}

	return version, nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}

	return formatTime(value)
}

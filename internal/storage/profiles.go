package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const activeProfileKey = "active_profile"

func (store *Store) ActiveProfile(ctx context.Context) (string, error) {
	var name string
	err := store.database.QueryRowContext(
		ctx,
		"SELECT value FROM application_state WHERE key = ?",
		activeProfileKey,
	).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read active profile: %w", err)
	}

	return name, nil
}

func (store *Store) SetActiveProfile(ctx context.Context, name string) error {
	if name == "" {
		return fmt.Errorf("active profile name is empty")
	}
	_, err := store.database.ExecContext(
		ctx,
		`INSERT INTO application_state(key, value) VALUES (?, ?)
         ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		activeProfileKey,
		name,
	)
	if err != nil {
		return fmt.Errorf("persist active profile: %w", err)
	}

	return nil
}

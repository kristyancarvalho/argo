package storage

import (
	"fmt"
	"os"
	"path/filepath"
)

func DefaultPath() (string, error) {
	if dataDirectory := os.Getenv("XDG_DATA_HOME"); dataDirectory != "" {
		return filepath.Join(dataDirectory, "argo", "argo.db"), nil
	}

	homeDirectory, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory for database: %w", err)
	}

	return filepath.Join(homeDirectory, ".local", "share", "argo", "argo.db"), nil
}

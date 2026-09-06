package storage

import (
	"fmt"
	"path/filepath"

	"github.com/kristyancarvalho/argo/internal/xdg"
)

func DefaultPath() (string, error) {
	dataDirectory, configured, err := xdg.EnvironmentDirectory("XDG_DATA_HOME")
	if err != nil {
		return "", err
	}
	if configured {
		return filepath.Join(dataDirectory, "argo", "argo.db"), nil
	}

	homeDirectory, err := xdg.HomeDirectory()
	if err != nil {
		return "", fmt.Errorf("determine home directory for database: %w", err)
	}

	return filepath.Join(homeDirectory, ".local", "share", "argo", "argo.db"), nil
}

package xdg

import (
	"fmt"
	"os"
	"path/filepath"
)

func EnvironmentDirectory(name string) (string, bool, error) {
	value := os.Getenv(name)
	if value == "" {
		return "", false, nil
	}
	if !filepath.IsAbs(value) {
		return "", false, fmt.Errorf("%s must be an absolute path", name)
	}

	return filepath.Clean(value), true, nil
}

func HomeDirectory() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}
	if !filepath.IsAbs(home) {
		return "", fmt.Errorf("HOME must resolve to an absolute path")
	}

	return filepath.Clean(home), nil
}

func TemporaryDirectory() (string, error) {
	directory := os.TempDir()
	if !filepath.IsAbs(directory) {
		return "", fmt.Errorf("temporary directory must resolve to an absolute path")
	}

	return filepath.Clean(directory), nil
}

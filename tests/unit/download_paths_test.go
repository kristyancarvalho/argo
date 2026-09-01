package unit_test

import (
	"path/filepath"
	"testing"

	"github.com/kristyancarvalho/argo/internal/downloader"
)

func TestDefaultPartsDirectoryUsesXDGState(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	directory, err := downloader.DefaultPartsDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if directory != filepath.Join(state, "argo", "parts") {
		t.Fatalf("partial directory is %q", directory)
	}
}

func TestDefaultPartsDirectoryFallsBackToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", home)
	directory, err := downloader.DefaultPartsDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if directory != filepath.Join(home, ".local", "state", "argo", "parts") {
		t.Fatalf("partial directory is %q", directory)
	}
}

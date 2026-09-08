package unit_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kristyancarvalho/argo/internal/config"
	"github.com/kristyancarvalho/argo/internal/downloader"
	"github.com/kristyancarvalho/argo/internal/ipc"
	"github.com/kristyancarvalho/argo/internal/storage"
	"github.com/kristyancarvalho/argo/internal/xdg"
)

func TestEveryXDGBaseRejectsRelativePaths(t *testing.T) {
	tests := []struct {
		name     string
		variable string
		resolve  func() (string, error)
	}{
		{name: "data", variable: "XDG_DATA_HOME", resolve: storage.DefaultPath},
		{name: "config", variable: "XDG_CONFIG_HOME", resolve: config.DefaultPath},
		{name: "state", variable: "XDG_STATE_HOME", resolve: downloader.DefaultPartsDirectory},
		{name: "runtime", variable: "XDG_RUNTIME_DIR", resolve: ipc.DefaultSocketPath},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(test.variable, filepath.Join("relative", "with spaces"))
			path, err := test.resolve()
			if err == nil || path != "" || !strings.Contains(err.Error(), test.variable) ||
				!strings.Contains(err.Error(), "absolute") {
				t.Fatalf("relative %s resolved to %q with error %v", test.variable, path, err)
			}
		})
	}
}

func TestEveryXDGBaseAcceptsAbsolutePathsWithSpaces(t *testing.T) {
	base := filepath.Join(t.TempDir(), "xdg base with spaces")
	tests := []struct {
		variable string
		resolve  func() (string, error)
		suffix   string
	}{
		{variable: "XDG_DATA_HOME", resolve: storage.DefaultPath, suffix: filepath.Join("argo", "argo.db")},
		{variable: "XDG_CONFIG_HOME", resolve: config.DefaultPath, suffix: filepath.Join("argo", config.Filename)},
		{variable: "XDG_STATE_HOME", resolve: downloader.DefaultPartsDirectory, suffix: filepath.Join("argo", "parts")},
		{variable: "XDG_RUNTIME_DIR", resolve: ipc.DefaultSocketPath, suffix: filepath.Join("argo", "argod.sock")},
	}
	for _, test := range tests {
		t.Run(test.variable, func(t *testing.T) {
			t.Setenv(test.variable, base)
			path, err := test.resolve()
			if err != nil {
				t.Fatal(err)
			}
			if path != filepath.Join(base, test.suffix) {
				t.Fatalf("resolved %s to %q", test.variable, path)
			}
		})
	}
}

func TestUnsetPersistentXDGPathsUseAbsoluteHomeFallbacks(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home with spaces")
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	tests := []struct {
		resolve func() (string, error)
		want    string
	}{
		{resolve: storage.DefaultPath, want: filepath.Join(home, ".local", "share", "argo", "argo.db")},
		{resolve: config.DefaultPath, want: filepath.Join(home, ".config", "argo", config.Filename)},
		{resolve: downloader.DefaultPartsDirectory, want: filepath.Join(home, ".local", "state", "argo", "parts")},
	}
	for _, test := range tests {
		path, err := test.resolve()
		if err != nil || path != test.want {
			t.Fatalf("home fallback resolved to %q, want %q, error %v", path, test.want, err)
		}
	}
}

func TestUnsetRuntimeXDGPathUsesAbsoluteTemporaryFallback(t *testing.T) {
	temporary := filepath.Join(t.TempDir(), "temporary with spaces")
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("TMPDIR", temporary)
	path, err := ipc.DefaultSocketPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(temporary, "argo-"+fmt.Sprintf("%d", os.Getuid()), "argod.sock")
	if path != want {
		t.Fatalf("runtime fallback resolved to %q, want %q", path, want)
	}
}

func TestRelativeHomeAndTemporaryDirectoriesAreRejected(t *testing.T) {
	t.Setenv("HOME", "relative-home")
	if _, err := xdg.HomeDirectory(); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative HOME returned %v", err)
	}
	t.Setenv("TMPDIR", "relative-tmp")
	if _, err := xdg.TemporaryDirectory(); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative TMPDIR returned %v", err)
	}
}

func TestMissingHomeDoesNotResolvePersistentStateAgainstWorkingDirectory(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	workingDirectory, cwdErr := os.Getwd()
	if cwdErr != nil {
		t.Fatal(cwdErr)
	}
	for _, resolve := range []func() (string, error){storage.DefaultPath, config.DefaultPath, downloader.DefaultPartsDirectory} {
		path, err := resolve()
		if err == nil || path != "" {
			t.Fatalf("missing HOME resolved persistent path %q with error %v", path, err)
		}
		if strings.HasPrefix(path, workingDirectory) {
			t.Fatalf("missing HOME resolved inside working directory: %q", path)
		}
	}
}

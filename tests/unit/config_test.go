package unit_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kristyancarvalho/argo/internal/config"
	"github.com/kristyancarvalho/argo/internal/model"
)

func TestConfigurationDefaultsAndReloadStrategy(t *testing.T) {
	configuration := config.Defaults()
	if configuration.Download.DefaultPriority != model.PriorityNormal ||
		configuration.Download.MaxConcurrentDownloads != 3 ||
		configuration.Download.MaxChunksPerDownload != 4 ||
		configuration.Network.PauseOnMetered ||
		configuration.Network.ResumeAfterMetered ||
		configuration.QoS.Policy != "off" {
		t.Fatalf("unexpected configuration defaults: %+v", configuration)
	}
	if configuration.ReloadStrategy() != config.ReloadOnRestart {
		t.Fatalf("reload strategy is %q, expected restart", configuration.ReloadStrategy())
	}
}

func TestLoadValidConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := []byte(`[download]
default_priority = "high"
max_concurrent_downloads = 2
max_chunks_per_download = 8

[network]
pause_on_metered = true
resume_after_metered = true

[qos]
policy = "latency"
`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	configuration, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Download.DefaultPriority != model.PriorityHigh ||
		configuration.Download.MaxConcurrentDownloads != 2 ||
		configuration.Download.MaxChunksPerDownload != 8 ||
		!configuration.Network.PauseOnMetered ||
		!configuration.Network.ResumeAfterMetered ||
		configuration.QoS.Policy != "latency" {
		t.Fatalf("unexpected loaded configuration: %+v", configuration)
	}
}

func TestLoadInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"toml", "[download\n"},
		{"priority", "[download]\ndefault_priority = \"urgent\"\n"},
		{"concurrency", "[download]\nmax_concurrent_downloads = 0\n"},
		{"chunks", "[download]\nmax_chunks_per_download = -1\n"},
		{"resume", "[network]\nresume_after_metered = true\n"},
		{"qos", "[qos]\npolicy = \"maximum\"\n"},
		{"unknown", "[download]\nunknown = true\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := config.Load(path)
			if err == nil {
				t.Fatal("invalid configuration succeeded")
			}
			if test.name != "toml" && test.name != "unknown" {
				var validationError config.ValidationError
				if !errors.As(err, &validationError) {
					t.Fatalf("configuration error has type %T, expected ValidationError", err)
				}
			}
		})
	}
}

func TestMissingConfigurationUsesDefaults(t *testing.T) {
	configuration, err := config.Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if configuration != config.Defaults() {
		t.Fatalf("missing configuration returned %+v", configuration)
	}
}

func TestConfigurationXDGPath(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "xdg-config")
	t.Setenv("XDG_CONFIG_HOME", directory)
	path, err := config.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(directory, "argo", config.Filename)
	if path != expected {
		t.Fatalf("configuration path is %q, expected %q", path, expected)
	}
}

func TestConfigurationDefaultXDGPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", home)
	path, err := config.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(home, ".config", "argo", config.Filename)
	if path != expected {
		t.Fatalf("configuration path is %q, expected %q", path, expected)
	}
}

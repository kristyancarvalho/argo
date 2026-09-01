package unit_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kristyancarvalho/argo/internal/config"
	"github.com/kristyancarvalho/argo/internal/model"
)

func TestConfigurationDefaultsAndReloadStrategy(t *testing.T) {
	configuration := config.Defaults()
	if configuration.Download.DefaultPriority != model.PriorityNormal ||
		configuration.Download.MaxConcurrentDownloads != 3 ||
		configuration.Download.MaxChunksPerDownload != 4 ||
		configuration.Download.RateLimit != "0" ||
		configuration.Network.PauseOnMetered ||
		configuration.Network.ResumeAfterMetered ||
		configuration.QoS.Policy != "off" ||
		configuration.QoS.LinkRate != "0" {
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
rate_limit = "10M"

[network]
pause_on_metered = true
resume_after_metered = true

[qos]
policy = "latency"
link_rate = "100M"
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
		configuration.QoS.Policy != "latency" ||
		configuration.QoS.LinkRate != "100M" {
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
		{"qos link rate", "[qos]\nlink_rate = \"fast\"\n"},
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
	if !reflect.DeepEqual(configuration, config.Defaults()) {
		t.Fatalf("missing configuration returned %+v", configuration)
	}
}

func TestConfigurationProfiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := []byte(`[download]
default_priority = "low"
max_concurrent_downloads = 5
rate_limit = "2M"

[network]
pause_on_metered = true

[qos]
policy = "balanced"

[profiles.gaming]
download_limit = "30M"
default_priority = "high"
max_concurrent_downloads = 2
pause_on_metered = false
policy = "latency"

[profiles.overnight]
download_limit = "0"
`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	configuration, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	gaming, err := configuration.Profile("gaming")
	if err != nil {
		t.Fatal(err)
	}
	if gaming.BytesPerSecond != 30_000_000 ||
		gaming.DefaultPriority != model.PriorityHigh ||
		gaming.MaxConcurrentDownloads != 2 ||
		gaming.PauseOnMetered ||
		gaming.ResumeAfterMetered ||
		gaming.Policy != "latency" {
		t.Fatalf("unexpected gaming profile: %+v", gaming)
	}
	overnight, err := configuration.Profile("overnight")
	if err != nil {
		t.Fatal(err)
	}
	if overnight.BytesPerSecond != 0 ||
		overnight.DefaultPriority != model.PriorityLow ||
		overnight.MaxConcurrentDownloads != 5 ||
		!overnight.PauseOnMetered ||
		overnight.Policy != "balanced" {
		t.Fatalf("unexpected inherited profile: %+v", overnight)
	}
	if _, err := configuration.Profile("missing"); err == nil {
		t.Fatal("unknown profile succeeded")
	}
}

func TestConfigurationRejectsInvalidProfiles(t *testing.T) {
	tests := []string{
		"[profiles.bad]\ndownload_limit = \"fast\"\n",
		"[profiles.bad]\ndefault_priority = \"urgent\"\n",
		"[profiles.bad]\nmax_concurrent_downloads = 0\n",
		"[profiles.bad]\nresume_after_metered = true\n",
		"[profiles.bad]\npolicy = \"maximum\"\n",
	}
	for _, content := range tests {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := config.Load(path); err == nil {
			t.Fatalf("invalid profile succeeded: %s", content)
		}
	}
}

func TestParseConfigurationRate(t *testing.T) {
	for input, expected := range map[string]int64{
		"0":   0,
		"12":  12,
		"3K":  3_000,
		"30M": 30_000_000,
		"2G":  2_000_000_000,
	} {
		actual, err := config.ParseRate(input)
		if err != nil {
			t.Fatal(err)
		}
		if actual != expected {
			t.Fatalf("rate %q is %d, expected %d", input, actual, expected)
		}
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

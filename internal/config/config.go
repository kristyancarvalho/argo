package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kristyancarvalho/argo/internal/model"
	"github.com/pelletier/go-toml/v2"
)

const (
	Filename          = "config.toml"
	ReloadOnRestart   = "restart"
	defaultConcurrent = 3
	defaultChunks     = 4
)

type Config struct {
	Download Download `toml:"download"`
	Network  Network  `toml:"network"`
	QoS      QoS      `toml:"qos"`
}

type Download struct {
	DefaultPriority        model.Priority `toml:"default_priority"`
	MaxConcurrentDownloads int            `toml:"max_concurrent_downloads"`
	MaxChunksPerDownload   int            `toml:"max_chunks_per_download"`
}

type Network struct {
	PauseOnMetered     bool `toml:"pause_on_metered"`
	ResumeAfterMetered bool `toml:"resume_after_metered"`
}

type QoS struct {
	Policy string `toml:"policy"`
}

type ValidationError struct {
	Field  string
	Reason string
}

func (err ValidationError) Error() string {
	return fmt.Sprintf("invalid configuration field %s: %s", err.Field, err.Reason)
}

func Defaults() Config {
	return Config{
		Download: Download{
			DefaultPriority:        model.PriorityNormal,
			MaxConcurrentDownloads: defaultConcurrent,
			MaxChunksPerDownload:   defaultChunks,
		},
		Network: Network{},
		QoS:     QoS{Policy: "off"},
	}
}

func DefaultPath() (string, error) {
	if directory := os.Getenv("XDG_CONFIG_HOME"); directory != "" {
		return filepath.Join(directory, "argo", Filename), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory for configuration: %w", err)
	}

	return filepath.Join(home, ".config", "argo", Filename), nil
}

func LoadDefault() (Config, error) {
	path, err := DefaultPath()
	if err != nil {
		return Config{}, err
	}

	return Load(path)
}

func Load(path string) (Config, error) {
	configuration := Defaults()
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return configuration, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("open configuration %s: %w", path, err)
	}
	defer func() {
		_ = file.Close()
	}()
	decoder := toml.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&configuration); err != nil {
		return Config{}, fmt.Errorf("decode configuration %s: %w", path, err)
	}
	if err := configuration.Validate(); err != nil {
		return Config{}, err
	}

	return configuration, nil
}

func (configuration Config) Validate() error {
	if _, err := model.ParsePriority(string(configuration.Download.DefaultPriority)); err != nil {
		return ValidationError{Field: "download.default_priority", Reason: err.Error()}
	}
	if configuration.Download.MaxConcurrentDownloads <= 0 {
		return ValidationError{
			Field:  "download.max_concurrent_downloads",
			Reason: "must be positive",
		}
	}
	if configuration.Download.MaxChunksPerDownload <= 0 {
		return ValidationError{
			Field:  "download.max_chunks_per_download",
			Reason: "must be positive",
		}
	}
	if configuration.Network.ResumeAfterMetered && !configuration.Network.PauseOnMetered {
		return ValidationError{
			Field:  "network.resume_after_metered",
			Reason: "requires network.pause_on_metered",
		}
	}
	switch configuration.QoS.Policy {
	case "off", "balanced", "throughput", "latency", "focus":
	default:
		return ValidationError{Field: "qos.policy", Reason: "is not recognized"}
	}

	return nil
}

func (Config) ReloadStrategy() string {
	return ReloadOnRestart
}

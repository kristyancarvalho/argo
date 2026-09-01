package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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
	Download Download           `toml:"download"`
	Network  Network            `toml:"network"`
	QoS      QoS                `toml:"qos"`
	Profiles map[string]Profile `toml:"profiles"`
}

type Download struct {
	DefaultPriority        model.Priority `toml:"default_priority"`
	MaxConcurrentDownloads int            `toml:"max_concurrent_downloads"`
	MaxChunksPerDownload   int            `toml:"max_chunks_per_download"`
	RateLimit              string         `toml:"rate_limit"`
}

type Network struct {
	PauseOnMetered     bool `toml:"pause_on_metered"`
	ResumeAfterMetered bool `toml:"resume_after_metered"`
}

type QoS struct {
	Policy   string `toml:"policy"`
	LinkRate string `toml:"link_rate"`
}

type Profile struct {
	DownloadLimit          string         `toml:"download_limit"`
	DefaultPriority        model.Priority `toml:"default_priority"`
	MaxConcurrentDownloads *int           `toml:"max_concurrent_downloads"`
	PauseOnMetered         *bool          `toml:"pause_on_metered"`
	ResumeAfterMetered     *bool          `toml:"resume_after_metered"`
	Policy                 string         `toml:"policy"`
}

type EffectiveProfile struct {
	Name                   string
	BytesPerSecond         int64
	DefaultPriority        model.Priority
	MaxConcurrentDownloads int
	PauseOnMetered         bool
	ResumeAfterMetered     bool
	Policy                 string
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
			RateLimit:              "0",
		},
		Network:  Network{},
		QoS:      QoS{Policy: "off", LinkRate: "0"},
		Profiles: make(map[string]Profile),
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
	if _, err := ParseRate(configuration.Download.RateLimit); err != nil {
		return ValidationError{Field: "download.rate_limit", Reason: err.Error()}
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
	if _, err := ParseRate(configuration.QoS.LinkRate); err != nil {
		return ValidationError{Field: "qos.link_rate", Reason: err.Error()}
	}
	for name := range configuration.Profiles {
		if _, err := configuration.Profile(name); err != nil {
			return err
		}
	}

	return nil
}

func (configuration Config) Profile(name string) (EffectiveProfile, error) {
	profile, exists := configuration.Profiles[name]
	if !exists {
		return EffectiveProfile{}, fmt.Errorf("unknown profile %q", name)
	}
	if strings.TrimSpace(name) == "" {
		return EffectiveProfile{}, ValidationError{Field: "profiles", Reason: "profile name must not be empty"}
	}
	effective := EffectiveProfile{
		Name:                   name,
		DefaultPriority:        configuration.Download.DefaultPriority,
		MaxConcurrentDownloads: configuration.Download.MaxConcurrentDownloads,
		PauseOnMetered:         configuration.Network.PauseOnMetered,
		ResumeAfterMetered:     configuration.Network.ResumeAfterMetered,
		Policy:                 configuration.QoS.Policy,
	}
	rate := configuration.Download.RateLimit
	if profile.DownloadLimit != "" {
		rate = profile.DownloadLimit
	}
	bytesPerSecond, err := ParseRate(rate)
	if err != nil {
		return EffectiveProfile{}, ValidationError{Field: "profiles." + name + ".download_limit", Reason: err.Error()}
	}
	effective.BytesPerSecond = bytesPerSecond
	if profile.DefaultPriority != "" {
		priority, err := model.ParsePriority(string(profile.DefaultPriority))
		if err != nil {
			return EffectiveProfile{}, ValidationError{Field: "profiles." + name + ".default_priority", Reason: err.Error()}
		}
		effective.DefaultPriority = priority
	}
	if profile.MaxConcurrentDownloads != nil {
		if *profile.MaxConcurrentDownloads <= 0 {
			return EffectiveProfile{}, ValidationError{
				Field:  "profiles." + name + ".max_concurrent_downloads",
				Reason: "must be positive",
			}
		}
		effective.MaxConcurrentDownloads = *profile.MaxConcurrentDownloads
	}
	if profile.PauseOnMetered != nil {
		effective.PauseOnMetered = *profile.PauseOnMetered
	}
	if profile.ResumeAfterMetered != nil {
		effective.ResumeAfterMetered = *profile.ResumeAfterMetered
	}
	if effective.ResumeAfterMetered && !effective.PauseOnMetered {
		return EffectiveProfile{}, ValidationError{
			Field:  "profiles." + name + ".resume_after_metered",
			Reason: "requires pause_on_metered",
		}
	}
	if profile.Policy != "" {
		switch profile.Policy {
		case "off", "balanced", "throughput", "latency", "focus":
			effective.Policy = profile.Policy
		default:
			return EffectiveProfile{}, ValidationError{
				Field:  "profiles." + name + ".policy",
				Reason: "is not recognized",
			}
		}
	}

	return effective, nil
}

func ParseRate(value string) (int64, error) {
	normalized := strings.TrimSpace(strings.ToUpper(value))
	if normalized == "" {
		return 0, fmt.Errorf("must not be empty")
	}
	multiplier := int64(1)
	for suffix, factor := range map[string]int64{"K": 1_000, "M": 1_000_000, "G": 1_000_000_000} {
		if strings.HasSuffix(normalized, suffix) {
			multiplier = factor
			normalized = strings.TrimSuffix(normalized, suffix)
			break
		}
	}
	amount, err := strconv.ParseInt(normalized, 10, 64)
	if err != nil || amount < 0 {
		return 0, fmt.Errorf("must be a non-negative byte rate with optional K, M, or G suffix")
	}
	if amount > (1<<63-1)/multiplier {
		return 0, fmt.Errorf("exceeds the maximum supported byte rate")
	}

	return amount * multiplier, nil
}

func (Config) ReloadStrategy() string {
	return ReloadOnRestart
}

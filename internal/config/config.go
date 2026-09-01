package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
	Directory              string         `toml:"directory"`
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
	Policy         string `toml:"policy"`
	LinkRate       string `toml:"link_rate"`
	LatencyTarget  string `toml:"latency_target"`
	MinimumRate    string `toml:"min_rate"`
	MaximumRate    string `toml:"max_rate"`
	ProbeTarget    string `toml:"probe_target"`
	ProbeTimeout   string `toml:"probe_timeout"`
	SampleInterval string `toml:"sample_interval"`
	ManualBaseline string `toml:"manual_baseline"`
}

type Profile struct {
	DownloadLimit          string         `toml:"download_limit"`
	DefaultPriority        model.Priority `toml:"default_priority"`
	MaxConcurrentDownloads *int           `toml:"max_concurrent_downloads"`
	PauseOnMetered         *bool          `toml:"pause_on_metered"`
	ResumeAfterMetered     *bool          `toml:"resume_after_metered"`
	Policy                 string         `toml:"policy"`
	LatencyTarget          string         `toml:"latency_target"`
	MinimumRate            string         `toml:"min_rate"`
	MaximumRate            string         `toml:"max_rate"`
}

type EffectiveProfile struct {
	Name                   string
	BytesPerSecond         int64
	DefaultPriority        model.Priority
	MaxConcurrentDownloads int
	PauseOnMetered         bool
	ResumeAfterMetered     bool
	Policy                 string
	LatencyTarget          time.Duration
	MinimumRate            uint64
	MaximumRate            uint64
}

type AdaptiveSettings struct {
	LatencyTarget  time.Duration
	MinimumRate    uint64
	MaximumRate    uint64
	ProbeTarget    string
	ProbeTimeout   time.Duration
	SampleInterval time.Duration
	ManualBaseline time.Duration
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
			Directory:              "~/Downloads",
			DefaultPriority:        model.PriorityNormal,
			MaxConcurrentDownloads: defaultConcurrent,
			MaxChunksPerDownload:   defaultChunks,
			RateLimit:              "0",
		},
		Network: Network{},
		QoS: QoS{
			Policy:         "off",
			LinkRate:       "0",
			LatencyTarget:  "20ms",
			ProbeTimeout:   "1s",
			SampleInterval: "1s",
		},
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
	if _, err := configuration.DownloadDirectory(); err != nil {
		return ValidationError{Field: "download.directory", Reason: err.Error()}
	}
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
	if _, err := parsePositiveDuration(configuration.QoS.LatencyTarget); err != nil {
		return ValidationError{Field: "qos.latency_target", Reason: err.Error()}
	}
	if _, err := parsePositiveDuration(configuration.QoS.ProbeTimeout); err != nil {
		return ValidationError{Field: "qos.probe_timeout", Reason: err.Error()}
	}
	if _, err := parsePositiveDuration(configuration.QoS.SampleInterval); err != nil {
		return ValidationError{Field: "qos.sample_interval", Reason: err.Error()}
	}
	if configuration.QoS.ManualBaseline != "" {
		if _, err := parsePositiveDuration(configuration.QoS.ManualBaseline); err != nil {
			return ValidationError{Field: "qos.manual_baseline", Reason: err.Error()}
		}
	}
	if configuration.QoS.ProbeTarget != "" {
		host, port, err := net.SplitHostPort(configuration.QoS.ProbeTarget)
		if err != nil || strings.TrimSpace(host) == "" || strings.TrimSpace(port) == "" {
			return ValidationError{Field: "qos.probe_target", Reason: "must use host:port"}
		}
	}
	if _, _, err := configuration.adaptiveSettings(Profile{}); err != nil {
		return err
	}
	for name := range configuration.Profiles {
		if _, err := configuration.Profile(name); err != nil {
			return err
		}
	}

	return nil
}

func (configuration Config) DownloadDirectory() (string, error) {
	value := strings.TrimSpace(configuration.Download.Directory)
	if value == "" {
		value = "~/Downloads"
	}
	if value == "~" || strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("determine home directory: %w", err)
		}
		if value == "~" {
			value = home
		} else {
			value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
		}
	} else if strings.HasPrefix(value, "~") {
		return "", fmt.Errorf("home shorthand must be ~ or start with ~/")
	}
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("must be an absolute path or start with ~/")
	}
	value = filepath.Clean(value)
	info, err := os.Stat(value)
	if err == nil && !info.IsDir() {
		return "", fmt.Errorf("path is not a directory: %s", value)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect directory %s: %w", value, err)
	}

	return value, nil
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
	target, rates, err := configuration.adaptiveSettings(profile)
	if err != nil {
		return EffectiveProfile{}, err
	}
	effective.LatencyTarget = target
	effective.MinimumRate = rates[0]
	effective.MaximumRate = rates[1]

	return effective, nil
}

func (configuration Config) Adaptive() (AdaptiveSettings, error) {
	target, rates, err := configuration.adaptiveSettings(Profile{})
	if err != nil {
		return AdaptiveSettings{}, err
	}
	probeTimeout, err := parsePositiveDuration(configuration.QoS.ProbeTimeout)
	if err != nil {
		return AdaptiveSettings{}, err
	}
	sampleInterval, err := parsePositiveDuration(configuration.QoS.SampleInterval)
	if err != nil {
		return AdaptiveSettings{}, err
	}
	var manual time.Duration
	if configuration.QoS.ManualBaseline != "" {
		manual, err = parsePositiveDuration(configuration.QoS.ManualBaseline)
		if err != nil {
			return AdaptiveSettings{}, err
		}
	}

	return AdaptiveSettings{
		LatencyTarget:  target,
		MinimumRate:    rates[0],
		MaximumRate:    rates[1],
		ProbeTarget:    configuration.QoS.ProbeTarget,
		ProbeTimeout:   probeTimeout,
		SampleInterval: sampleInterval,
		ManualBaseline: manual,
	}, nil
}

func (configuration Config) adaptiveSettings(profile Profile) (time.Duration, [2]uint64, error) {
	targetValue := configuration.QoS.LatencyTarget
	if profile.LatencyTarget != "" {
		targetValue = profile.LatencyTarget
	}
	target, err := parsePositiveDuration(targetValue)
	if err != nil {
		return 0, [2]uint64{}, ValidationError{Field: "profiles latency_target", Reason: err.Error()}
	}
	linkRate, err := ParseRate(configuration.QoS.LinkRate)
	if err != nil {
		return 0, [2]uint64{}, ValidationError{Field: "qos.link_rate", Reason: err.Error()}
	}
	minimum := uint64(linkRate) / 10
	maximum := uint64(linkRate) / 10 * 8
	minimumValue := configuration.QoS.MinimumRate
	maximumValue := configuration.QoS.MaximumRate
	if profile.MinimumRate != "" {
		minimumValue = profile.MinimumRate
	}
	if profile.MaximumRate != "" {
		maximumValue = profile.MaximumRate
	}
	if minimumValue != "" {
		value, parseErr := ParseRate(minimumValue)
		if parseErr != nil {
			return 0, [2]uint64{}, ValidationError{Field: "adaptive min_rate", Reason: parseErr.Error()}
		}
		minimum = uint64(value)
	}
	if maximumValue != "" {
		value, parseErr := ParseRate(maximumValue)
		if parseErr != nil {
			return 0, [2]uint64{}, ValidationError{Field: "adaptive max_rate", Reason: parseErr.Error()}
		}
		maximum = uint64(value)
	}
	if linkRate > 0 && (minimum == 0 || maximum <= minimum || maximum >= uint64(linkRate)) {
		return 0, [2]uint64{}, ValidationError{
			Field:  "adaptive rates",
			Reason: "must be positive, increasing, and below qos.link_rate",
		}
	}
	if linkRate == 0 && (minimum != 0 || maximum != 0) {
		return 0, [2]uint64{}, ValidationError{Field: "adaptive rates", Reason: "require qos.link_rate"}
	}

	return target, [2]uint64{minimum, maximum}, nil
}

func parsePositiveDuration(value string) (time.Duration, error) {
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("must be a positive duration")
	}

	return duration, nil
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

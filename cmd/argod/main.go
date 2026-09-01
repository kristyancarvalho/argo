package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kristyancarvalho/argo/internal/config"
	"github.com/kristyancarvalho/argo/internal/daemon"
	"github.com/kristyancarvalho/argo/internal/downloader"
	"github.com/kristyancarvalho/argo/internal/ipc"
	"github.com/kristyancarvalho/argo/internal/network"
	"github.com/kristyancarvalho/argo/internal/qos"
	"github.com/kristyancarvalho/argo/internal/qosipc"
	"github.com/kristyancarvalho/argo/internal/storage"
	"github.com/kristyancarvalho/argo/internal/telemetry"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string) (runError error) {
	configuration, err := config.LoadDefault()
	if err != nil {
		return err
	}
	downloadDirectory, err := configuration.DownloadDirectory()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(downloadDirectory, 0o755); err != nil {
		return fmt.Errorf("create download directory: %w", err)
	}
	partsDirectory, err := downloader.DefaultPartsDirectory()
	if err != nil {
		return err
	}
	configuredRate, err := config.ParseRate(configuration.Download.RateLimit)
	if err != nil {
		return err
	}
	configuredLinkRate, err := config.ParseRate(configuration.QoS.LinkRate)
	if err != nil {
		return err
	}
	policy, err := qos.ParsePolicy(configuration.QoS.Policy)
	if err != nil {
		return err
	}
	adaptiveSettings, err := configuration.Adaptive()
	if err != nil {
		return err
	}
	latencyPolicy, err := newLatencyPolicy(
		adaptiveSettings.LatencyTarget,
		adaptiveSettings.MinimumRate,
		adaptiveSettings.MaximumRate,
		adaptiveSettings.ManualBaseline,
	)
	if err != nil {
		return err
	}
	profiles := make(map[string]daemon.Profile, len(configuration.Profiles))
	for name := range configuration.Profiles {
		profile, err := configuration.Profile(name)
		if err != nil {
			return err
		}
		profileLatencyPolicy, err := newLatencyPolicy(
			profile.LatencyTarget,
			profile.MinimumRate,
			profile.MaximumRate,
			adaptiveSettings.ManualBaseline,
		)
		if err != nil {
			return err
		}
		profiles[name] = daemon.Profile{
			Name:                       profile.Name,
			BytesPerSecond:             profile.BytesPerSecond,
			DefaultPriority:            profile.DefaultPriority,
			MaximumConcurrentDownloads: profile.MaxConcurrentDownloads,
			PauseOnMetered:             profile.PauseOnMetered,
			ResumeAfterMetered:         profile.ResumeAfterMetered,
			Policy:                     profile.Policy,
			LatencyPolicy:              profileLatencyPolicy,
		}
	}
	flags := flag.NewFlagSet("argod", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	rateLimit := flags.Int64("rate-limit", configuredRate, "maximum download bytes per second")
	maximumConcurrent := flags.Int(
		"max-concurrent-downloads",
		configuration.Download.MaxConcurrentDownloads,
		"maximum concurrent downloads",
	)
	maximumChunks := flags.Int(
		"max-chunks-per-download",
		configuration.Download.MaxChunksPerDownload,
		"maximum chunks per download",
	)
	pauseOnMetered := flags.Bool(
		"pause-on-metered",
		configuration.Network.PauseOnMetered,
		"pause downloads on metered connections",
	)
	resumeAfterMetered := flags.Bool(
		"resume-after-metered",
		configuration.Network.ResumeAfterMetered,
		"resume automatically paused downloads on unmetered connections",
	)
	if err := flags.Parse(arguments); err != nil {
		return fmt.Errorf("parse daemon arguments: %w", err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected daemon arguments: %v", flags.Args())
	}
	if *rateLimit < 0 {
		return fmt.Errorf("rate limit must not be negative")
	}
	if *maximumConcurrent <= 0 {
		return fmt.Errorf("maximum concurrent downloads must be positive")
	}
	if *maximumChunks <= 0 {
		return fmt.Errorf("maximum chunks per download must be positive")
	}
	if *resumeAfterMetered && !*pauseOnMetered {
		return fmt.Errorf("resume after metered requires pause on metered")
	}
	cgroupID, _ := qos.CurrentCgroupID()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	socketPath, err := ipc.DefaultSocketPath()
	if err != nil {
		return err
	}
	databasePath, err := storage.DefaultPath()
	if err != nil {
		return err
	}
	store, err := storage.Open(ctx, databasePath)
	if err != nil {
		return err
	}
	defer func() {
		runError = errors.Join(runError, store.Close())
	}()
	var latencyProbe telemetry.LatencyProbe
	if adaptiveSettings.ProbeTarget != "" {
		latencyProbe, err = telemetry.NewTCPProbe(
			adaptiveSettings.ProbeTarget,
			adaptiveSettings.ProbeTimeout,
		)
		if err != nil {
			return err
		}
	}
	var telemetryObserver daemon.TelemetryObserver
	if adaptiveSettings.MaximumRate > 0 {
		telemetryObserver, err = telemetry.NewObserver(
			store,
			telemetry.NewCollector(telemetry.NewSampler(0), latencyProbe),
			adaptiveSettings.SampleInterval,
		)
		if err != nil {
			return err
		}
	}
	engine, err := downloader.NewWithOptions(store, downloader.Options{
		HTTPClient:     http.DefaultClient,
		BytesPerSecond: *rateLimit,
		MaximumChunks:  *maximumChunks,
		PartsDirectory: partsDirectory,
	})
	if err != nil {
		return err
	}
	var networkObserver daemon.NetworkObserver
	networkClient, networkError := network.ConnectSystem()
	if networkError == nil {
		networkObserver = networkClient
		defer func() {
			runError = errors.Join(runError, networkClient.Close())
		}()
	}
	service, err := daemon.NewServiceWithOptions(ctx, store, engine, daemon.ServiceOptions{
		MaximumConcurrentDownloads: *maximumConcurrent,
		NetworkObserver:            networkObserver,
		PauseOnMetered:             *pauseOnMetered,
		ResumeAfterMetered:         *resumeAfterMetered,
		DefaultPriority:            configuration.Download.DefaultPriority,
		DefaultDestination:         downloadDirectory,
		Profiles:                   profiles,
		TrafficPolicy:              policy,
		TrafficLinkRate:            uint64(configuredLinkRate),
		TrafficCgroupID:            cgroupID,
		TrafficBackend:             qosipc.NewClient(qosipc.DefaultSocketPath),
		TelemetryObserver:          telemetryObserver,
		LatencyPolicy:              latencyPolicy,
	})
	if err != nil {
		return err
	}
	defer func() {
		runError = errors.Join(runError, service.Close())
	}()
	server, err := ipc.Listen(socketPath, service)
	if err != nil {
		return err
	}

	return server.Serve(ctx)
}

func newLatencyPolicy(
	target time.Duration,
	minimum uint64,
	maximum uint64,
	manualBaseline time.Duration,
) (*qos.LatencyPolicy, error) {
	if minimum == 0 && maximum == 0 {
		return nil, nil
	}
	span := maximum - minimum
	increaseStep := span / 20
	decreaseStep := span / 10
	if increaseStep == 0 {
		increaseStep = 1
	}
	if decreaseStep == 0 {
		decreaseStep = 1
	}
	baseline, err := telemetry.NewBaselineEstimator(telemetry.BaselineOptions{Manual: manualBaseline})
	if err != nil {
		return nil, err
	}
	controller, err := qos.NewAdaptiveController(qos.AdaptiveOptions{
		MinimumRateBitsPerSecond:  minimum,
		MaximumRateBitsPerSecond:  maximum,
		InitialRateBitsPerSecond:  maximum,
		IncreaseStepBitsPerSecond: increaseStep,
		DecreaseStepBitsPerSecond: decreaseStep,
		AcceptableLatencyIncrease: target,
		RequiredSamples:           3,
	})
	if err != nil {
		return nil, err
	}

	return qos.NewLatencyPolicy(baseline, controller)
}

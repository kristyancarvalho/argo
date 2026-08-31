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

	"github.com/kristyancarvalho/argo/internal/config"
	"github.com/kristyancarvalho/argo/internal/daemon"
	"github.com/kristyancarvalho/argo/internal/downloader"
	"github.com/kristyancarvalho/argo/internal/ipc"
	"github.com/kristyancarvalho/argo/internal/network"
	"github.com/kristyancarvalho/argo/internal/storage"
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
	flags := flag.NewFlagSet("argod", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	rateLimit := flags.Int64("rate-limit", 0, "maximum download bytes per second")
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
	engine, err := downloader.NewWithOptions(store, downloader.Options{
		HTTPClient:     http.DefaultClient,
		BytesPerSecond: *rateLimit,
		MaximumChunks:  *maximumChunks,
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

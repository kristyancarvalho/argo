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

	"github.com/kristyancarvalho/argo/internal/daemon"
	"github.com/kristyancarvalho/argo/internal/downloader"
	"github.com/kristyancarvalho/argo/internal/ipc"
	"github.com/kristyancarvalho/argo/internal/storage"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string) (runError error) {
	flags := flag.NewFlagSet("argod", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	rateLimit := flags.Int64("rate-limit", 0, "maximum download bytes per second")
	if err := flags.Parse(arguments); err != nil {
		return fmt.Errorf("parse daemon arguments: %w", err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected daemon arguments: %v", flags.Args())
	}
	if *rateLimit < 0 {
		return fmt.Errorf("rate limit must not be negative")
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
	engine := downloader.NewWithRateLimit(store, http.DefaultClient, *rateLimit)
	service, err := daemon.NewService(ctx, store, engine)
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

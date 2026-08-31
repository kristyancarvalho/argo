package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/kristyancarvalho/argo/internal/daemon"
	"github.com/kristyancarvalho/argo/internal/downloader"
	"github.com/kristyancarvalho/argo/internal/ipc"
	"github.com/kristyancarvalho/argo/internal/storage"
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() (runError error) {
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
	service := daemon.NewService(ctx, store, downloader.New(store))
	defer func() {
		runError = errors.Join(runError, service.Close())
	}()
	server, err := ipc.Listen(socketPath, service)
	if err != nil {
		return err
	}

	return server.Serve(ctx)
}

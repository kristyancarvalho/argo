package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/kristyancarvalho/argo/internal/ipc"
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	socketPath, err := ipc.DefaultSocketPath()
	if err != nil {
		return err
	}
	handler := ipc.NewStatusHandler()
	server, err := ipc.Listen(socketPath, handler)
	if err != nil {
		return err
	}

	return server.Serve(ctx)
}

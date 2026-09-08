package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kristyancarvalho/argo/internal/qos"
	"github.com/kristyancarvalho/argo/internal/qosbackend"
	"github.com/kristyancarvalho/argo/internal/qosipc"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	userID := os.Getuid()
	if userID < 0 {
		return fmt.Errorf("determine authorized user ID")
	}
	flags := flag.NewFlagSet("argo-qosd", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	socketPath := flags.String("socket", qosipc.DefaultSocketPath, "QoS helper Unix socket")
	statePath := flags.String("state", qosipc.DefaultStatePath, "QoS helper recovery state")
	allowedUID := flags.Uint("allowed-uid", uint(userID), "authorized peer user ID")
	if err := flags.Parse(arguments); err != nil {
		return fmt.Errorf("parse QoS helper arguments: %w", err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected QoS helper arguments: %v", flags.Args())
	}
	if uint(uint32(*allowedUID)) != *allowedUID {
		return fmt.Errorf("authorized user ID exceeds supported range")
	}
	authorizer, err := qosipc.NewPeerUIDAuthorizer(uint32(*allowedUID))
	if err != nil {
		return err
	}
	persistentBackend, err := qosbackend.NewPersistent(qosbackend.New(), *statePath)
	if err != nil {
		return err
	}
	controller, err := qos.NewController(persistentBackend)
	if err != nil {
		return err
	}
	if err := controller.Recover(context.Background()); err != nil {
		return err
	}
	service, err := qosipc.NewService(controller)
	if err != nil {
		return err
	}
	server, err := qosipc.Listen(*socketPath, service, authorizer)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveError := server.Serve(ctx)
	cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
	cleanupError := controller.Reconcile(cleanupContext, qos.DesiredState{Policy: qos.PolicyOff})
	cleanupCancel()

	return errors.Join(serveError, cleanupError)
}

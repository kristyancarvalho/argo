package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/kristyancarvalho/argo/internal/cli"
	"github.com/kristyancarvalho/argo/internal/console"
	"github.com/kristyancarvalho/argo/internal/doctor"
	"github.com/kristyancarvalho/argo/internal/ipc"
	"github.com/kristyancarvalho/argo/internal/tui"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(cli.ExitCode(err))
	}
}

func run(arguments []string) error {
	if len(arguments) > 0 {
		switch arguments[0] {
		case "help", "-h", "--help":
			return cli.RunWithOptions(
				context.Background(), nil, os.Stdout, arguments, cli.Options{
					Color: console.Enabled(os.Stdout), Interactive: console.Terminal(os.Stdout),
				},
			)
		}
	}
	socketPath, err := ipc.DefaultSocketPath()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client := ipc.NewClient(socketPath)
	diagnostics := doctor.NewDefault(client)
	if len(arguments) > 0 && arguments[0] == "tui" {
		if len(arguments) != 1 {
			return cli.UsageError{Message: "argo tui"}
		}

		return tui.Run(ctx, client, os.Stdin, os.Stdout)
	}

	return cli.RunWithOptions(ctx, client, os.Stdout, arguments, cli.Options{
		Color: console.Enabled(os.Stdout), Interactive: console.Terminal(os.Stdout), Doctor: diagnostics.Run,
	})
}

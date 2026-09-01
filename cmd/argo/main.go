package main

import (
	"context"
	"fmt"
	"os"

	"github.com/kristyancarvalho/argo/internal/cli"
	"github.com/kristyancarvalho/argo/internal/console"
	"github.com/kristyancarvalho/argo/internal/ipc"
	"github.com/kristyancarvalho/argo/internal/tui"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) > 0 {
		switch arguments[0] {
		case "help", "-h", "--help":
			return cli.RunWithOptions(
				context.Background(), nil, os.Stdout, arguments, cli.Options{Color: console.Enabled(os.Stdout)},
			)
		}
	}
	socketPath, err := ipc.DefaultSocketPath()
	if err != nil {
		return err
	}

	ctx := context.Background()
	client := ipc.NewClient(socketPath)
	if len(arguments) > 0 && arguments[0] == "tui" {
		if len(arguments) != 1 {
			return cli.UsageError{Message: "argo tui"}
		}

		return tui.Run(ctx, client, os.Stdin, os.Stdout)
	}

	return cli.RunWithOptions(ctx, client, os.Stdout, arguments, cli.Options{Color: console.Enabled(os.Stdout)})
}

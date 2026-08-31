package main

import (
	"context"
	"fmt"
	"os"

	"github.com/kristyancarvalho/argo/internal/cli"
	"github.com/kristyancarvalho/argo/internal/ipc"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	socketPath, err := ipc.DefaultSocketPath()
	if err != nil {
		return err
	}

	return cli.Run(context.Background(), ipc.NewClient(socketPath), os.Stdout, arguments)
}

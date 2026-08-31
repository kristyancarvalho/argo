package cli

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/kristyancarvalho/argo/internal/ipc"
)

type Client interface {
	Add(context.Context, string, string) (ipc.AddResponse, error)
	List(context.Context) ([]ipc.Download, error)
	Show(context.Context, string) (ipc.Download, error)
	Pause(context.Context, string) (ipc.DownloadActionResponse, error)
	Resume(context.Context, string) (ipc.DownloadActionResponse, error)
	Cancel(context.Context, string) (ipc.DownloadActionResponse, error)
	Priority(context.Context, string, string) (ipc.PriorityResponse, error)
	Status(context.Context) (ipc.Status, error)
}

func Run(ctx context.Context, client Client, output io.Writer, arguments []string) error {
	if len(arguments) == 0 {
		return UsageError{Message: "command is required"}
	}
	command := arguments[0]
	operands := arguments[1:]

	switch command {
	case "add":
		return runAdd(ctx, client, output, operands)
	case "list":
		return runList(ctx, client, output, operands)
	case "show":
		return runShow(ctx, client, output, operands)
	case "pause":
		return runAction(ctx, client.Pause, output, command, operands)
	case "resume":
		return runAction(ctx, client.Resume, output, command, operands)
	case "cancel":
		return runAction(ctx, client.Cancel, output, command, operands)
	case "priority":
		return runPriority(ctx, client, output, operands)
	case "watch":
		return runWatch(ctx, client, output, operands)
	case "status":
		return runStatus(ctx, client, output, operands)
	default:
		return UsageError{Message: fmt.Sprintf("unknown command %q", command)}
	}
}

func runAdd(ctx context.Context, client Client, output io.Writer, arguments []string) error {
	if len(arguments) != 1 {
		return UsageError{Message: "argo add <url>"}
	}
	response, err := client.Add(ctx, arguments[0], "")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Added %s %s (%s)\n", response.ID, response.Filename, response.Status)

	return err
}

func runList(ctx context.Context, client Client, output io.Writer, arguments []string) error {
	if len(arguments) != 0 {
		return UsageError{Message: "argo list"}
	}
	downloads, err := client.List(ctx)
	if err != nil {
		return err
	}
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "ID\tSTATUS\tPROGRESS\tFILENAME"); err != nil {
		return err
	}
	for _, download := range downloads {
		if _, err := fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\n",
			download.ID,
			download.Status,
			formatProgress(download),
			download.Filename,
		); err != nil {
			return err
		}
	}

	return writer.Flush()
}

func runShow(ctx context.Context, client Client, output io.Writer, arguments []string) error {
	if len(arguments) != 1 {
		return UsageError{Message: "argo show <id>"}
	}
	download, err := client.Show(ctx, arguments[0])
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(
		output,
		"ID: %s\nFilename: %s\nURL: %s\nDestination: %s\nStatus: %s\nPriority: %s\nProgress: %s\nError: %s\n",
		download.ID,
		download.Filename,
		download.URL,
		download.Destination,
		download.Status,
		download.Priority,
		formatProgress(download),
		download.Error,
	)

	return err
}

func runAction(
	ctx context.Context,
	action func(context.Context, string) (ipc.DownloadActionResponse, error),
	output io.Writer,
	name string,
	arguments []string,
) error {
	if len(arguments) != 1 {
		return UsageError{Message: fmt.Sprintf("argo %s <id>", name)}
	}
	response, err := action(ctx, arguments[0])
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "%s: %s\n", response.ID, response.Status)

	return err
}

func runWatch(ctx context.Context, client Client, output io.Writer, arguments []string) error {
	if len(arguments) != 0 {
		return UsageError{Message: "argo watch"}
	}

	return NewWatcher(client).Stream(ctx, func(snapshot WatchSnapshot) error {
		return renderWatchSnapshot(output, snapshot)
	})
}

func runPriority(ctx context.Context, client Client, output io.Writer, arguments []string) error {
	if len(arguments) != 2 {
		return UsageError{Message: "argo priority <id> <priority>"}
	}
	response, err := client.Priority(ctx, arguments[0], arguments[1])
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "%s: %s\n", response.ID, response.Priority)

	return err
}

func runStatus(ctx context.Context, client Client, output io.Writer, arguments []string) error {
	if len(arguments) != 0 {
		return UsageError{Message: "argo status"}
	}
	status, err := client.Status(ctx)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(
		output,
		"Daemon: %s\nPID: %d\nStarted: %s\nProtocol: %d\n",
		status.State,
		status.PID,
		status.StartedAt.Format("2006-01-02 15:04:05Z07:00"),
		status.ProtocolVersion,
	)

	return err
}

func formatProgress(download ipc.Download) string {
	if download.TotalSize < 0 {
		return fmt.Sprintf("%d/? bytes", download.DownloadedBytes)
	}

	return fmt.Sprintf("%d/%d bytes", download.DownloadedBytes, download.TotalSize)
}

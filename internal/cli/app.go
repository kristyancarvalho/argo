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
	Profile(context.Context, string) (ipc.ProfileResponse, error)
	Policy(context.Context, string) (ipc.PolicyResponse, error)
}

func Run(ctx context.Context, client Client, output io.Writer, arguments []string) error {
	return RunWithOptions(ctx, client, output, arguments, Options{})
}

func RunWithOptions(ctx context.Context, client Client, output io.Writer, arguments []string, options Options) error {
	if options.Color {
		output = styledWriter{output: output}
	}
	if len(arguments) == 0 {
		return UsageError{Message: "command is required"}
	}
	command := arguments[0]
	operands := arguments[1:]

	switch command {
	case "help", "-h", "--help":
		return runHelp(output, operands)
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
	case "profile":
		return runProfile(ctx, client, output, operands)
	case "policy":
		return runPolicy(ctx, client, output, operands)
	default:
		return UsageError{Message: fmt.Sprintf("unknown command %q", command)}
	}
}

func runHelp(output io.Writer, arguments []string) error {
	if len(arguments) != 0 {
		return UsageError{Message: "argo help"}
	}
	_, err := fmt.Fprint(output, `Argo download manager and network traffic governor

Usage:
  argo <command> [arguments]

Commands:
  add <url>                         Add a download
  list                              List downloads
  show <id>                         Show download details
  pause <id>                        Pause a download
  resume <id>                       Resume a download
  cancel <id>                       Cancel a download
  priority <id> <low|normal|high>   Order queued Argo downloads
  watch                             Stream download progress
  status                            Show daemon, network, and QoS state
  policy <name>                     Select a system traffic policy
  profile <name>                    Activate a configured profile
  tui                               Open the terminal interface
  help                              Show this help

Traffic policies:
  off         Disable system traffic shaping
  focus       Favor system responsiveness; reserve 20% for Argo
  balanced    Split guaranteed capacity equally
  throughput  Favor Argo downloads; reserve 80% for Argo
  latency     Adapt the Argo limit from measured latency

Priorities only order queued downloads inside Argo. Policies control how Argo
competes with other applications and require an active download, a configured
link rate, a connected interface, and the privileged argo-qosd helper.
`)

	return err
}

func runPolicy(ctx context.Context, client Client, output io.Writer, arguments []string) error {
	if len(arguments) != 1 {
		return UsageError{Message: "argo policy <off|balanced|throughput|latency|focus>"}
	}
	policy, err := client.Policy(ctx, arguments[0])
	if err != nil {
		return err
	}
	state := "inactive"
	if policy.Applied {
		state = "active"
	}
	_, err = fmt.Fprintf(output, "Traffic policy: %s (%s)\n", policy.Policy, state)

	return err
}

func runProfile(ctx context.Context, client Client, output io.Writer, arguments []string) error {
	if len(arguments) != 1 {
		return UsageError{Message: "argo profile <name>"}
	}
	profile, err := client.Profile(ctx, arguments[0])
	if err != nil {
		return err
	}
	state := "inactive"
	if profile.PolicyApplied {
		state = "active"
	}
	if _, err = fmt.Fprintf(
		output,
		"Active profile: %s\nTraffic policy: %s (%s)\n",
		profile.Name,
		statusValue(profile.Policy),
		state,
	); err != nil {
		return err
	}
	if profile.QoSError != "" {
		_, err = fmt.Fprintf(output, "QoS warning: %s\n", profile.QoSError)
	}

	return err
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
	networkState := status.Network.State
	if !status.Network.Available {
		networkState = "unavailable"
	} else if networkState == "" {
		networkState = "unknown"
	}
	_, err = fmt.Fprintf(
		output,
		"Daemon: %s\nPID: %d\nStarted: %s\nProtocol: %d\nNetwork: %s\nInterface: %s\nConnection type: %s\nMetered: %s\nActive profile: %s\n",
		status.State,
		status.PID,
		status.StartedAt.Format("2006-01-02 15:04:05Z07:00"),
		status.ProtocolVersion,
		networkState,
		statusValue(status.Network.Interface),
		statusValue(status.Network.ConnectionType),
		statusValue(status.Network.Metered),
		statusValue(status.ActiveProfile),
	)
	if err != nil {
		return err
	}
	shaping := "inactive"
	if status.Traffic.Applied {
		shaping = "active"
	}
	if _, err = fmt.Fprintf(
		output,
		"Traffic policy: %s\nQoS shaping: %s\n",
		statusValue(status.Traffic.Policy),
		shaping,
	); err != nil {
		return err
	}
	if status.Traffic.Error != "" {
		if _, err = fmt.Fprintf(output, "QoS error: %s\n", status.Traffic.Error); err != nil {
			return err
		}
	}
	if status.Traffic.CurrentRateBitsPerSecond > 0 {
		if _, err = fmt.Fprintf(
			output,
			"Current limit: %d bit/s\n",
			status.Traffic.CurrentRateBitsPerSecond,
		); err != nil {
			return err
		}
	}
	if status.Traffic.Policy == "latency" {
		latency := "unavailable"
		if status.Traffic.LatencyAvailable {
			latency = status.Traffic.MeasuredLatency.String()
		}
		baseline := "unavailable"
		if status.Traffic.BaselineAvailable {
			baseline = status.Traffic.BaselineLatency.String()
		}
		_, err = fmt.Fprintf(
			output,
			"Measured latency: %s\nBaseline latency: %s\nController: %s\n",
			latency,
			baseline,
			statusValue(status.Traffic.ControllerState),
		)
	}

	return err
}

func statusValue(value string) string {
	if value == "" {
		return "unknown"
	}

	return value
}

func formatProgress(download ipc.Download) string {
	if download.TotalSize < 0 {
		return fmt.Sprintf("%d/? bytes", download.DownloadedBytes)
	}

	return fmt.Sprintf("%d/%d bytes", download.DownloadedBytes, download.TotalSize)
}

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/kristyancarvalho/argo/internal/diagnostic"
	"github.com/kristyancarvalho/argo/internal/doctor"
	"github.com/kristyancarvalho/argo/internal/ipc"
)

type Client interface {
	Add(context.Context, string, string) (ipc.AddResponse, error)
	AddWithChecksum(context.Context, string, string, string) (ipc.AddResponse, error)
	List(context.Context) ([]ipc.Download, error)
	Show(context.Context, string) (ipc.Download, error)
	Pause(context.Context, string) (ipc.DownloadActionResponse, error)
	Resume(context.Context, string) (ipc.DownloadActionResponse, error)
	Cancel(context.Context, string) (ipc.DownloadActionResponse, error)
	Remove(context.Context, string) (ipc.DownloadActionResponse, error)
	Clear(context.Context) (ipc.ClearResponse, error)
	Retry(context.Context, string) (ipc.AddResponse, error)
	Verify(context.Context, string) (ipc.VerifyResponse, error)
	Priority(context.Context, string, string) (ipc.PriorityResponse, error)
	Status(context.Context) (ipc.Status, error)
	Profile(context.Context, string) (ipc.ProfileResponse, error)
	Policy(context.Context, string) (ipc.PolicyResponse, error)
}

func Run(ctx context.Context, client Client, output io.Writer, arguments []string) error {
	return RunWithOptions(ctx, client, output, arguments, Options{})
}

func RunWithOptions(ctx context.Context, client Client, output io.Writer, arguments []string, options Options) error {
	if len(arguments) == 0 {
		return UsageError{Message: "command is required"}
	}
	command := arguments[0]
	operands := arguments[1:]
	jsonOutput, operands, err := parseJSONOutput(command, operands)
	if err != nil {
		return err
	}
	terminalOutput := output
	if options.Color && !jsonOutput {
		output = styledWriter{output: output}
	}

	switch command {
	case "help", "-h", "--help":
		return runHelp(output, operands)
	case "add":
		return runAdd(ctx, client, output, operands)
	case "list":
		return runList(ctx, client, output, operands, jsonOutput)
	case "show":
		return runShow(ctx, client, output, operands, jsonOutput)
	case "pause":
		return runAction(ctx, client.Pause, output, command, operands)
	case "resume":
		return runAction(ctx, client.Resume, output, command, operands)
	case "cancel":
		return runAction(ctx, client.Cancel, output, command, operands)
	case "remove":
		return runAction(ctx, client.Remove, output, command, operands)
	case "clear":
		return runClear(ctx, client, output, operands)
	case "retry":
		return runRetry(ctx, client, output, operands)
	case "verify":
		return runVerify(ctx, client, output, operands)
	case "priority":
		return runPriority(ctx, client, output, operands)
	case "watch":
		return runWatch(ctx, client, output, terminalOutput, operands, options)
	case "status":
		return runStatus(ctx, client, output, operands, jsonOutput)
	case "doctor":
		return runDoctor(ctx, output, operands, options, jsonOutput)
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
  add [--checksum sha256:<hex>] <url>
                                    Add a download
  list [--json]                     List downloads
  show <id> [--json]                Show download details
  pause <id>                        Pause a download
  resume <id>                       Resume a download
  cancel <id>                       Cancel a download
  remove <id>                       Remove a historical download
  clear                             Clear completed, failed, and canceled history
  retry <id>                        Start a new transfer from historical source
  verify <id>                       Verify a completed download
  priority <id> <low|normal|high>   Order queued Argo downloads
  watch                             Stream download progress
  status [--json]                   Show daemon, network, and QoS state
  doctor [--json]                   Diagnose core and optional capabilities
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

Resume preserves a transfer ID and requires valid partial data. Retry creates a
new transfer ID from completed, failed, or canceled history.
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
	_, err = fmt.Fprintf(output, "Traffic policy: %s (%s)\n", diagnostic.Display(policy.Policy), state)

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
		diagnostic.Display(profile.Name),
		statusValue(profile.Policy),
		state,
	); err != nil {
		return err
	}
	if profile.QoSError != "" {
		_, err = fmt.Fprintf(output, "QoS warning: %s\n", diagnostic.Display(diagnostic.Text(profile.QoSError)))
	}

	return err
}

func runAdd(ctx context.Context, client Client, output io.Writer, arguments []string) error {
	rawURL := ""
	checksum := ""
	for index := 0; index < len(arguments); index++ {
		if arguments[index] == "--checksum" {
			if checksum != "" || index+1 >= len(arguments) {
				return UsageError{Message: "argo add [--checksum sha256:<hex>] <url>"}
			}
			checksum = arguments[index+1]
			index++
			continue
		}
		if rawURL != "" {
			return UsageError{Message: "argo add [--checksum sha256:<hex>] <url>"}
		}
		rawURL = arguments[index]
	}
	if rawURL == "" {
		return UsageError{Message: "argo add [--checksum sha256:<hex>] <url>"}
	}
	response, err := client.AddWithChecksum(ctx, rawURL, "", checksum)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Added %s %s (%s)\n", diagnostic.Display(response.ID), diagnostic.Display(response.Filename), diagnostic.Display(response.Status))

	return err
}

func runVerify(ctx context.Context, client Client, output io.Writer, arguments []string) error {
	if len(arguments) != 1 {
		return UsageError{Message: "argo verify <id>"}
	}
	response, err := client.Verify(ctx, arguments[0])
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(
		output,
		"Verified %s (%s)\n",
		diagnostic.Display(response.ID),
		diagnostic.Display(response.Checksum),
	)

	return err
}

func runClear(ctx context.Context, client Client, output io.Writer, arguments []string) error {
	if len(arguments) != 0 {
		return UsageError{Message: "argo clear"}
	}
	response, err := client.Clear(ctx)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Removed %d historical downloads\n", response.Removed)

	return err
}

func runRetry(ctx context.Context, client Client, output io.Writer, arguments []string) error {
	if len(arguments) != 1 {
		return UsageError{Message: "argo retry <id>"}
	}
	response, err := client.Retry(ctx, arguments[0])
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Added %s %s (%s)\n", diagnostic.Display(response.ID), diagnostic.Display(response.Filename), diagnostic.Display(response.Status))

	return err
}

func runList(ctx context.Context, client Client, output io.Writer, arguments []string, jsonOutput bool) error {
	if len(arguments) != 0 {
		return UsageError{Message: "argo list"}
	}
	downloads, err := client.List(ctx)
	if err != nil {
		return err
	}
	if jsonOutput {
		for index := range downloads {
			downloads[index] = safeJSONDownload(downloads[index])
		}
		return WriteJSON(output, "download_list", struct {
			Downloads []ipc.Download `json:"downloads"`
		}{Downloads: downloads})
	}
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "ID\tSTATUS\tPROGRESS\tFILENAME"); err != nil {
		return err
	}
	for _, download := range downloads {
		if _, err := fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\n",
			diagnostic.Display(download.ID),
			diagnostic.Display(download.Status),
			formatProgress(download),
			diagnostic.Display(download.Filename),
		); err != nil {
			return err
		}
	}

	return writer.Flush()
}

func runShow(ctx context.Context, client Client, output io.Writer, arguments []string, jsonOutput bool) error {
	if len(arguments) != 1 {
		return UsageError{Message: "argo show <id>"}
	}
	download, err := client.Show(ctx, arguments[0])
	if err != nil {
		return err
	}
	if jsonOutput {
		return WriteJSON(output, "download", safeJSONDownload(download))
	}
	_, err = fmt.Fprintf(
		output,
		"ID: %s\nFilename: %s\nURL: %s\nDestination: %s\nStatus: %s\nPriority: %s\nProgress: %s\nChecksum: %s\nError: %s\n",
		diagnostic.Display(download.ID),
		diagnostic.Display(download.Filename),
		diagnostic.Display(diagnostic.URL(download.URL)),
		diagnostic.Display(download.Destination),
		diagnostic.Display(download.Status),
		diagnostic.Display(download.Priority),
		formatProgress(download),
		statusValue(download.Checksum),
		diagnostic.Display(diagnostic.Text(download.Error)),
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
	_, err = fmt.Fprintf(output, "%s: %s\n", diagnostic.Display(response.ID), diagnostic.Display(response.Status))

	return err
}

func runWatch(
	ctx context.Context,
	client Client,
	output io.Writer,
	terminalOutput io.Writer,
	arguments []string,
	options Options,
) error {
	if len(arguments) != 0 {
		return UsageError{Message: "argo watch"}
	}

	watcher := NewWatcher(client)
	if !options.Interactive {
		snapshot, err := watcher.Snapshot(ctx)
		if err != nil {
			return err
		}

		return renderWatchSnapshot(output, snapshot)
	}
	size := options.TerminalSize
	if size == nil {
		size = watchTerminalSize(terminalOutput)
	}
	renderer := newWatchRegionRenderer(output, size)
	err := watcher.Stream(ctx, renderer.Render)
	if errors.Is(err, context.Canceled) {
		return nil
	}

	return err
}

func runPriority(ctx context.Context, client Client, output io.Writer, arguments []string) error {
	if len(arguments) != 2 {
		return UsageError{Message: "argo priority <id> <priority>"}
	}
	response, err := client.Priority(ctx, arguments[0], arguments[1])
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "%s: %s\n", diagnostic.Display(response.ID), diagnostic.Display(response.Priority))

	return err
}

func runStatus(ctx context.Context, client Client, output io.Writer, arguments []string, jsonOutput bool) error {
	if len(arguments) != 0 {
		return UsageError{Message: "argo status"}
	}
	status, err := client.Status(ctx)
	if err != nil {
		return err
	}
	if jsonOutput {
		return WriteJSON(output, "daemon_status", safeJSONStatus(status))
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
		diagnostic.Display(status.State),
		status.PID,
		status.StartedAt.Format("2006-01-02 15:04:05Z07:00"),
		status.ProtocolVersion,
		diagnostic.Display(networkState),
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
		if _, err = fmt.Fprintf(output, "QoS error: %s\n", diagnostic.Display(diagnostic.Text(status.Traffic.Error))); err != nil {
			return err
		}
	}
	if status.Network.Error != "" {
		if _, err = fmt.Fprintf(output, "Network error: %s\n", diagnostic.Display(diagnostic.Text(status.Network.Error))); err != nil {
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

func runDoctor(ctx context.Context, output io.Writer, arguments []string, options Options, jsonOutput bool) error {
	if len(arguments) != 0 {
		return UsageError{Message: "argo doctor [--json]"}
	}
	if options.Doctor == nil {
		return fmt.Errorf("doctor diagnostics are unavailable")
	}
	report := options.Doctor(ctx)
	if jsonOutput {
		if err := WriteJSON(output, "doctor", report); err != nil {
			return err
		}
	} else {
		writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
		if _, err := fmt.Fprintln(writer, "CHECK\tSCOPE\tSTATE\tDETAIL"); err != nil {
			return err
		}
		for _, check := range report.Checks {
			detail := check.Detail
			if check.Action != "" {
				detail += "; action: " + check.Action
			}
			if _, err := fmt.Fprintf(
				writer,
				"%s\t%s\t%s\t%s\n",
				diagnostic.Display(check.Name),
				diagnostic.Display(check.Scope),
				diagnostic.Display(string(check.State)),
				diagnostic.Display(diagnostic.Text(detail)),
			); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(writer, "Core downloads\t\t%s\nSystem QoS\t\t%s\n", readiness(report.CoreReady), readiness(report.QoSReady)); err != nil {
			return err
		}
		if err := writer.Flush(); err != nil {
			return err
		}
	}
	if !report.CoreReady {
		daemonUnavailable := false
		for _, check := range report.Checks {
			if check.Name == "daemon" && check.State != doctor.StateAvailable {
				daemonUnavailable = true
			}
		}
		return DoctorError{DaemonUnavailable: daemonUnavailable}
	}
	return nil
}

func readiness(ready bool) string {
	if ready {
		return "ready"
	}
	return "unavailable"
}

func parseJSONOutput(command string, arguments []string) (bool, []string, error) {
	jsonOutput := false
	operands := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		if argument != "--json" {
			operands = append(operands, argument)
			continue
		}
		if jsonOutput {
			return false, nil, UsageError{Message: "--json may only be specified once"}
		}
		jsonOutput = true
	}
	if jsonOutput {
		switch command {
		case "list", "show", "status", "doctor":
		default:
			return false, nil, UsageError{Message: "--json is supported by list, show, status, and doctor"}
		}
	}
	if command == "list" || command == "show" || command == "status" || command == "doctor" {
		for _, operand := range operands {
			if strings.HasPrefix(operand, "-") {
				return false, nil, UsageError{Message: fmt.Sprintf("unknown option %q for argo %s", operand, command)}
			}
		}
	}
	return jsonOutput, operands, nil
}

func statusValue(value string) string {
	if value == "" {
		return "unknown"
	}

	return diagnostic.Display(value)
}

func formatProgress(download ipc.Download) string {
	if download.TotalSize < 0 {
		return fmt.Sprintf("%d/? bytes", download.DownloadedBytes)
	}

	return fmt.Sprintf("%d/%d bytes", download.DownloadedBytes, download.TotalSize)
}

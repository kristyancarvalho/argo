package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/kristyancarvalho/argo/internal/config"
	"github.com/kristyancarvalho/argo/internal/ipc"
	"github.com/kristyancarvalho/argo/internal/qos"
	"github.com/kristyancarvalho/argo/internal/qosipc"
	"github.com/kristyancarvalho/argo/internal/storage"
	"golang.org/x/sys/unix"
)

type State string

const (
	StateAvailable   State = "available"
	StateUnavailable State = "unavailable"
	StateDegraded    State = "degraded"
	StateError       State = "error"
)

type Check struct {
	Name   string `json:"name"`
	Scope  string `json:"scope"`
	State  State  `json:"state"`
	Detail string `json:"detail"`
	Action string `json:"action,omitempty"`
}

type Report struct {
	CoreReady bool    `json:"core_ready"`
	QoSReady  bool    `json:"qos_ready"`
	Checks    []Check `json:"checks"`
}

type StatusClient interface {
	Status(context.Context) (ipc.Status, error)
}

type HelperClient interface {
	Status(context.Context) (qosipc.Status, error)
}

type Probes struct {
	Daemon        StatusClient
	Helper        HelperClient
	Configuration func() (config.Config, error)
	DatabasePath  func() (string, error)
	Stat          func(string) (os.FileInfo, error)
	Access        func(string, uint32) error
	LookPath      func(string) (string, error)
	Command       func(context.Context, string, ...string) error
	Cgroup        func() (qos.CgroupSelector, error)
	EffectiveUID  func() int
}

type Runner struct {
	probes Probes
}

func New(probes Probes) *Runner {
	if probes.Configuration == nil {
		probes.Configuration = config.LoadDefault
	}
	if probes.DatabasePath == nil {
		probes.DatabasePath = storage.DefaultPath
	}
	if probes.Stat == nil {
		probes.Stat = os.Stat
	}
	if probes.Access == nil {
		probes.Access = unix.Access
	}
	if probes.LookPath == nil {
		probes.LookPath = exec.LookPath
	}
	if probes.Command == nil {
		probes.Command = func(ctx context.Context, name string, arguments ...string) error {
			return exec.CommandContext(ctx, name, arguments...).Run()
		}
	}
	if probes.Cgroup == nil {
		probes.Cgroup = qos.CurrentCgroup
	}
	if probes.EffectiveUID == nil {
		probes.EffectiveUID = os.Geteuid
	}
	return &Runner{probes: probes}
}

func NewDefault(daemon StatusClient) *Runner {
	helper := qosipc.NewClient(qosipc.DefaultSocketPath)
	helper.Timeout = 750 * time.Millisecond
	return New(Probes{Daemon: daemon, Helper: helper})
}

func (runner *Runner) Run(ctx context.Context) Report {
	checks := make([]Check, 0, 11)
	status, daemonCheck := runner.daemon(ctx)
	checks = append(checks, daemonCheck)
	checks = append(checks, runner.database(status, daemonCheck.State))
	checks = append(checks, runner.destination())
	checks = append(checks, runner.network(status, daemonCheck.State))
	tcCheck := runner.executable(ctx, "tc", "optional", "-Version")
	if tcCheck.State != StateAvailable {
		tcCheck.Action = "install iproute2 to enable traffic control"
	}
	checks = append(checks, tcCheck)
	nftCheck := runner.executable(ctx, "nftables", "optional", "--version")
	if nftCheck.State != StateAvailable {
		nftCheck.Action = "install nftables to enable traffic classification"
	}
	checks = append(checks, nftCheck)
	ifbCheck := runner.ifb()
	checks = append(checks, ifbCheck)
	cgroupCheck := runner.cgroup()
	checks = append(checks, cgroupCheck)
	helperCheck := runner.helper(ctx)
	checks = append(checks, helperCheck)
	checks = append(checks, runner.privilege())
	qosReady := allAvailable(tcCheck, nftCheck, ifbCheck, cgroupCheck, helperCheck)
	rxState := StateUnavailable
	rxDetail := "receive-path shaping prerequisites are incomplete"
	rxAction := "resolve unavailable optional checks before enabling a traffic policy"
	if qosReady {
		rxState = StateAvailable
		rxDetail = "nftables classification and tc IFB receive-path shaping are available"
		rxAction = ""
	}
	checks = append(checks, Check{Name: "rx_shaping", Scope: "optional", State: rxState, Detail: rxDetail, Action: rxAction})
	coreReady := coreChecksReady(checks)
	return Report{CoreReady: coreReady, QoSReady: qosReady, Checks: checks}
}

func (runner *Runner) daemon(ctx context.Context) (ipc.Status, Check) {
	if runner.probes.Daemon == nil {
		return ipc.Status{}, Check{Name: "daemon", Scope: "core", State: StateUnavailable, Detail: "daemon client is not configured", Action: "start argod"}
	}
	probeContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	status, err := runner.probes.Daemon.Status(probeContext)
	if err != nil {
		return ipc.Status{}, Check{Name: "daemon", Scope: "core", State: StateUnavailable, Detail: err.Error(), Action: "start or restart argod for the current user"}
	}
	return status, Check{Name: "daemon", Scope: "core", State: StateAvailable, Detail: fmt.Sprintf("running with protocol %d", status.ProtocolVersion)}
}

func (runner *Runner) database(status ipc.Status, daemonState State) Check {
	path, err := runner.probes.DatabasePath()
	if err != nil {
		return Check{Name: "sqlite", Scope: "core", State: StateError, Detail: err.Error(), Action: "repair HOME or XDG_DATA_HOME"}
	}
	info, err := runner.probes.Stat(path)
	if errors.Is(err, os.ErrNotExist) && daemonState != StateAvailable {
		return Check{Name: "sqlite", Scope: "core", State: StateDegraded, Detail: "database is not initialized at " + path, Action: "start argod to initialize persistent state"}
	}
	if err != nil {
		return Check{Name: "sqlite", Scope: "core", State: StateError, Detail: err.Error(), Action: "verify the database path and permissions"}
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return Check{Name: "sqlite", Scope: "core", State: StateError, Detail: "database path is not a private regular file", Action: "restore a user-owned regular database with mode 0600"}
	}
	detail := "private database at " + path
	if status.ProtocolVersion > 0 {
		detail += "; open daemon confirms usable state"
	}
	return Check{Name: "sqlite", Scope: "core", State: StateAvailable, Detail: detail}
}

func (runner *Runner) destination() Check {
	configuration, err := runner.probes.Configuration()
	if err != nil {
		return Check{Name: "download_directory", Scope: "core", State: StateError, Detail: err.Error(), Action: "repair the Argo configuration"}
	}
	path, err := configuration.DownloadDirectory()
	if err != nil {
		return Check{Name: "download_directory", Scope: "core", State: StateError, Detail: err.Error(), Action: "configure an absolute writable download directory"}
	}
	info, err := runner.probes.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Check{Name: "download_directory", Scope: "core", State: StateDegraded, Detail: "directory does not exist yet: " + path, Action: "start a download to create it or create it with private ownership"}
	}
	if err != nil {
		return Check{Name: "download_directory", Scope: "core", State: StateError, Detail: err.Error(), Action: "repair destination access"}
	}
	if !info.IsDir() {
		return Check{Name: "download_directory", Scope: "core", State: StateError, Detail: "configured destination is not a directory", Action: "select a directory in download.directory"}
	}
	if err := runner.probes.Access(path, unix.W_OK|unix.X_OK); err != nil {
		return Check{Name: "download_directory", Scope: "core", State: StateError, Detail: "configured destination is not writable", Action: "repair destination ownership and permissions"}
	}
	return Check{Name: "download_directory", Scope: "core", State: StateAvailable, Detail: "writable directory at " + path}
}

func (runner *Runner) network(status ipc.Status, daemonState State) Check {
	if daemonState != StateAvailable {
		return Check{Name: "networkmanager", Scope: "optional", State: StateUnavailable, Detail: "daemon status is unavailable", Action: "start argod and verify NetworkManager D-Bus availability"}
	}
	if !status.Network.Available {
		detail := status.Network.Error
		if detail == "" {
			detail = "NetworkManager observation is unavailable"
		}
		return Check{Name: "networkmanager", Scope: "optional", State: StateUnavailable, Detail: detail, Action: "start NetworkManager or use Argo without network-aware policies"}
	}
	state := status.Network.State
	if state == "" {
		state = "unknown"
	}
	return Check{Name: "networkmanager", Scope: "optional", State: StateAvailable, Detail: "observer state " + state}
}

func (runner *Runner) executable(ctx context.Context, name, scope string, arguments ...string) Check {
	executable := name
	if name == "nftables" {
		executable = "nft"
	}
	path, err := runner.probes.LookPath(executable)
	if err != nil {
		return Check{Name: name, Scope: scope, State: StateUnavailable, Detail: executable + " executable was not found"}
	}
	probeContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := runner.probes.Command(probeContext, path, arguments...); err != nil {
		return Check{Name: name, Scope: scope, State: StateError, Detail: executable + " executable failed its version probe: " + err.Error()}
	}
	return Check{Name: name, Scope: scope, State: StateAvailable, Detail: "validated executable at " + path}
}

func (runner *Runner) ifb() Check {
	for _, path := range []string{"/sys/module/ifb", "/sys/class/net/ifb0"} {
		if info, err := runner.probes.Stat(path); err == nil && info.IsDir() {
			return Check{Name: "ifb", Scope: "optional", State: StateAvailable, Detail: "IFB kernel support is loaded"}
		}
	}
	return Check{Name: "ifb", Scope: "optional", State: StateUnavailable, Detail: "IFB kernel support is not loaded", Action: "load the ifb module kernel module before enabling RX shaping"}
}

func (runner *Runner) cgroup() Check {
	selector, err := runner.probes.Cgroup()
	if err != nil {
		return Check{Name: "cgroup_v2", Scope: "optional", State: StateUnavailable, Detail: err.Error(), Action: "run argod in a non-root cgroup v2 service"}
	}
	return Check{Name: "cgroup_v2", Scope: "optional", State: StateAvailable, Detail: fmt.Sprintf("selector %s at level %d", selector.Path, selector.Level)}
}

func (runner *Runner) helper(ctx context.Context) Check {
	if runner.probes.Helper == nil {
		return Check{Name: "qos_helper", Scope: "optional", State: StateUnavailable, Detail: "QoS helper client is not configured", Action: "install and start argo-qosd for the current user"}
	}
	probeContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	status, err := runner.probes.Helper.Status(probeContext)
	if err != nil {
		return Check{Name: "qos_helper", Scope: "optional", State: StateUnavailable, Detail: err.Error(), Action: "install and start argo-qosd for the current user"}
	}
	detail := "authenticated helper is reachable"
	if status.Applied {
		detail += "; Argo-owned QoS state is active"
	}
	return Check{Name: "qos_helper", Scope: "optional", State: StateAvailable, Detail: detail}
}

func (runner *Runner) privilege() Check {
	if runner.probes.EffectiveUID() == 0 {
		return Check{Name: "privilege_boundary", Scope: "core", State: StateDegraded, Detail: "the CLI is running as root", Action: "run argo and argod as the regular user; privilege only argo-qosd"}
	}
	return Check{Name: "privilege_boundary", Scope: "core", State: StateAvailable, Detail: "CLI and main daemon can remain unprivileged"}
}

func allAvailable(checks ...Check) bool {
	for _, check := range checks {
		if check.State != StateAvailable {
			return false
		}
	}
	return true
}

func coreChecksReady(checks []Check) bool {
	for _, check := range checks {
		if check.Scope == "core" && (check.State == StateUnavailable || check.State == StateError) {
			return false
		}
	}
	return true
}

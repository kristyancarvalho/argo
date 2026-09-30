package unit_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kristyancarvalho/argo/internal/config"
	"github.com/kristyancarvalho/argo/internal/doctor"
	"github.com/kristyancarvalho/argo/internal/ipc"
	"github.com/kristyancarvalho/argo/internal/qos"
	"github.com/kristyancarvalho/argo/internal/qosipc"
)

type doctorDaemon struct {
	status ipc.Status
	err    error
}

func (daemon doctorDaemon) Status(context.Context) (ipc.Status, error) {
	return daemon.status, daemon.err
}

type doctorHelper struct {
	status qosipc.Status
	err    error
}

func (helper doctorHelper) Status(context.Context) (qosipc.Status, error) {
	return helper.status, helper.err
}

func TestDoctorReportsCompleteCoreAndQoSCapability(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "downloads")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(root, "argo.db")
	if err := os.WriteFile(database, []byte("sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	configuration := config.Defaults()
	configuration.Download.Directory = destination
	directoryInfo, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	runner := doctor.New(doctor.Probes{
		Daemon: doctorDaemon{status: ipc.Status{
			ProtocolVersion: ipc.ProtocolVersion,
			Network:         ipc.NetworkStatus{Available: true, State: "connected-global"},
		}},
		Helper:        doctorHelper{},
		Configuration: func() (config.Config, error) { return configuration, nil },
		DatabasePath:  func() (string, error) { return database, nil },
		Stat: func(path string) (os.FileInfo, error) {
			if path == "/sys/module/ifb" {
				return directoryInfo, nil
			}
			return os.Stat(path)
		},
		Access:   func(string, uint32) error { return nil },
		LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		Command:  func(context.Context, string, ...string) error { return nil },
		Cgroup: func() (qos.CgroupSelector, error) {
			return qos.CgroupSelector{Path: "user.slice/argo.service", Level: 2}, nil
		},
		EffectiveUID: func() int { return 1000 },
	})
	report := runner.Run(context.Background())
	if !report.CoreReady || !report.QoSReady {
		t.Fatalf("doctor reported incomplete capability: %+v", report)
	}
	if len(report.Checks) != 11 {
		t.Fatalf("doctor returned %d checks, expected 11", len(report.Checks))
	}
	for _, check := range report.Checks {
		if check.State != doctor.StateAvailable {
			t.Errorf("check %s is %s: %s", check.Name, check.State, check.Detail)
		}
	}
}

func TestDoctorSeparatesUnavailableOptionalCapabilities(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "downloads")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(root, "argo.db")
	if err := os.WriteFile(database, []byte("sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	configuration := config.Defaults()
	configuration.Download.Directory = destination
	runner := doctor.New(doctor.Probes{
		Daemon:        doctorDaemon{status: ipc.Status{ProtocolVersion: ipc.ProtocolVersion}},
		Helper:        doctorHelper{err: errors.New("helper missing")},
		Configuration: func() (config.Config, error) { return configuration, nil },
		DatabasePath:  func() (string, error) { return database, nil },
		Stat: func(path string) (os.FileInfo, error) {
			if path == "/sys/module/ifb" || path == "/sys/class/net/ifb0" {
				return nil, os.ErrNotExist
			}
			return os.Stat(path)
		},
		LookPath:     func(string) (string, error) { return "", errors.New("missing") },
		Cgroup:       func() (qos.CgroupSelector, error) { return qos.CgroupSelector{}, errors.New("legacy hierarchy") },
		EffectiveUID: func() int { return 1000 },
	})
	report := runner.Run(context.Background())
	if !report.CoreReady || report.QoSReady {
		t.Fatalf("doctor conflated core and QoS capability: %+v", report)
	}
	for _, name := range []string{"networkmanager", "tc", "nftables", "ifb", "cgroup_v2", "qos_helper", "rx_shaping"} {
		check := doctorCheck(t, report, name)
		if check.State == doctor.StateAvailable || check.Action == "" {
			t.Errorf("optional check %s is not actionable: %+v", name, check)
		}
	}
}

func TestDoctorRejectsUnsafeCorePaths(t *testing.T) {
	root := t.TempDir()
	database := filepath.Join(root, "database-directory")
	if err := os.Mkdir(database, 0o700); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(destination, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	configuration := config.Defaults()
	configuration.Download.Directory = destination
	runner := doctor.New(doctor.Probes{
		Daemon:        doctorDaemon{status: ipc.Status{ProtocolVersion: ipc.ProtocolVersion}},
		Configuration: func() (config.Config, error) { return configuration, nil },
		DatabasePath:  func() (string, error) { return database, nil },
		LookPath:      func(string) (string, error) { return "", errors.New("missing") },
		Cgroup:        func() (qos.CgroupSelector, error) { return qos.CgroupSelector{}, errors.New("missing") },
		EffectiveUID:  func() int { return 0 },
	})
	report := runner.Run(context.Background())
	if report.CoreReady {
		t.Fatalf("unsafe core paths were reported ready: %+v", report)
	}
	if doctorCheck(t, report, "sqlite").State != doctor.StateError || doctorCheck(t, report, "download_directory").State != doctor.StateError {
		t.Fatalf("unsafe paths were not errors: %+v", report)
	}
	if doctorCheck(t, report, "privilege_boundary").State != doctor.StateDegraded {
		t.Fatalf("root execution was not degraded: %+v", report)
	}
}

func doctorCheck(t *testing.T, report doctor.Report, name string) doctor.Check {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("doctor report has no %s check", name)
	return doctor.Check{}
}

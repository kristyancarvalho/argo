package unit_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSystemdArgodUserServiceDefinition(t *testing.T) {
	content := readSystemdUnit(t, "argod.service")
	requireUnitSettings(t, content, []string{
		"[Unit]",
		"Wants=network-online.target",
		"After=network-online.target",
		"[Service]",
		"Type=exec",
		"ExecStart=/usr/bin/argod",
		"Restart=on-failure",
		"KillSignal=SIGTERM",
		"NoNewPrivileges=yes",
		"CapabilityBoundingSet=",
		"UMask=0077",
		"[Install]",
		"WantedBy=default.target",
	})
	if strings.Contains(content, "User=root") || strings.Contains(content, "CAP_NET_ADMIN") {
		t.Fatal("argod user service grants privileged identity or capabilities")
	}
}

func TestSystemdQoSHelperSystemServiceDefinition(t *testing.T) {
	content := readSystemdUnit(t, "argo-qosd@.service")
	requireUnitSettings(t, content, []string{
		"[Unit]",
		"Before=network.target",
		"[Service]",
		"Type=exec",
		"User=%i",
		"ExecStart=/usr/bin/argo-qosd",
		"RuntimeDirectory=argo",
		"RuntimeDirectoryMode=0700",
		"Restart=on-failure",
		"KillSignal=SIGTERM",
		"CapabilityBoundingSet=CAP_NET_ADMIN",
		"AmbientCapabilities=CAP_NET_ADMIN",
		"NoNewPrivileges=yes",
		"ProtectSystem=strict",
		"ProtectHome=yes",
		"RestrictAddressFamilies=AF_UNIX AF_NETLINK",
		"UMask=0077",
		"[Install]",
		"WantedBy=multi-user.target",
	})
	if strings.Contains(content, "User=root") || strings.Contains(content, "CAP_SYS_ADMIN") {
		t.Fatal("QoS helper unit grants broader privilege than required")
	}
}

func readSystemdUnit(t *testing.T, name string) string {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workingDirectory, "..", "..", "packaging", "systemd", name)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return string(content)
}

func requireUnitSettings(t *testing.T, content string, settings []string) {
	t.Helper()
	for _, setting := range settings {
		if !strings.Contains(content, setting+"\n") {
			t.Errorf("systemd unit is missing %q", setting)
		}
	}
}

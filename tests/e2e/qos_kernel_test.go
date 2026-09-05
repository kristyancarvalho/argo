package e2e_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/kristyancarvalho/argo/internal/qos"
	"github.com/kristyancarvalho/argo/internal/qosbackend"
)

func TestQoSKernelPolicyLifecycle(t *testing.T) {
	if address := os.Getenv("ARGO_QOS_PACKET_WORKER"); address != "" {
		var signal [1]byte
		if _, err := os.Stdin.Read(signal[:]); err != nil {
			t.Fatal(err)
		}
		connection, err := net.Dial("udp4", address)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := connection.Write([]byte("argo-cgroup-v2")); err != nil {
			t.Fatal(err)
		}
		if err := connection.Close(); err != nil {
			t.Fatal(err)
		}
		return
	}
	if os.Getenv("ARGO_QOS_TEST_NETNS") == "" {
		runQoSKernelNamespace(t)
		return
	}
	testQoSKernelLifecycle(t)
}

func runQoSKernelNamespace(t *testing.T) {
	for _, executable := range []string{"unshare", "ip", "tc", "nft"} {
		if _, err := exec.LookPath(executable); err != nil {
			t.Skipf("%s is unavailable", executable)
		}
	}
	command := exec.Command("unshare", "-Urn", os.Args[0], "-test.run=^TestQoSKernelPolicyLifecycle$")
	command.Env = append(os.Environ(), "ARGO_QOS_TEST_NETNS=1")
	output, err := command.CombinedOutput()
	if err != nil {
		if strings.Contains(string(output), "Operation not permitted") {
			t.Skipf("unprivileged network namespaces are unavailable: %s", output)
		}
		t.Fatalf("isolated QoS lifecycle: %v: %s", err, output)
	}
}

func testQoSKernelLifecycle(t *testing.T) {
	runKernelCommand(t, "ip", "link", "add", "argo-test", "type", "dummy")
	runKernelCommand(t, "ip", "address", "add", "192.0.2.1/24", "dev", "argo-test")
	runKernelCommand(t, "ip", "link", "set", "argo-test", "up")
	cgroup, cleanup := createPacketCgroup(t)
	defer cleanup()
	worker := exec.Command(os.Args[0], "-test.run=^TestQoSKernelPolicyLifecycle$")
	worker.Env = append(os.Environ(), "ARGO_QOS_PACKET_WORKER=192.0.2.2:9")
	signal, err := worker.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join("/sys/fs/cgroup", cgroup.Path, "cgroup.procs"),
		[]byte(strconv.Itoa(worker.Process.Pid)),
		0o600,
	); err != nil {
		_ = worker.Process.Kill()
		t.Fatalf("move packet worker to delegated cgroup: %v", err)
	}
	backend := qosbackend.New()
	for _, test := range []struct {
		policy qos.Policy
		rate   uint64
	}{
		{qos.PolicyFocus, 20_000_000},
		{qos.PolicyBalanced, 50_000_000},
		{qos.PolicyThroughput, 80_000_000},
	} {
		state, err := qos.MapPolicy(test.policy, qos.PolicyEnvironment{
			Interface: "argo-test", LinkRateBitsPerSecond: 100_000_000,
			Cgroup: cgroup, ActiveDownloads: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := backend.Apply(context.Background(), state); err != nil {
			t.Fatalf("apply %s policy: %v", test.policy, err)
		}
		classes := runKernelCommand(t, "tc", "class", "show", "dev", "argo-test")
		for _, value := range []string{"class htb a400:10", "class htb a400:20", formatMegabits(test.rate)} {
			if !strings.Contains(classes, value) {
				t.Fatalf("%s classes %q do not contain %q", test.policy, classes, value)
			}
		}
	}
	if _, err := signal.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := signal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := worker.Wait(); err != nil {
		t.Fatal(err)
	}
	connection, err := net.Dial("udp4", "192.0.2.2:9")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write([]byte("unrelated-cgroup")); err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	rules := runKernelCommand(t, "nft", "list", "table", "inet", "argo")
	for _, value := range []string{
		fmt.Sprintf("socket cgroupv2 level %d %q", cgroup.Level, cgroup.Path),
		"meta mark set",
		"0x0000a400",
	} {
		if !strings.Contains(rules, value) {
			t.Fatalf("classification rules %q do not contain %q", rules, value)
		}
	}
	if strings.Count(rules, "socket cgroupv2") != 1 {
		t.Fatalf("classification must mark only the Argo cgroup: %q", rules)
	}
	if strings.Contains(rules, "counter packets 0 bytes 0") {
		t.Fatalf("Argo cgroup rule did not classify the test socket: %q", rules)
	}
	matches := regexp.MustCompile(`counter packets ([0-9]+) bytes`).FindStringSubmatch(rules)
	if len(matches) != 2 || matches[1] != "1" {
		t.Fatalf("classification counted unrelated cgroup traffic: %q", rules)
	}
	if err := backend.Remove(context.Background(), "argo-test"); err != nil {
		t.Fatal(err)
	}
	qdiscs := runKernelCommand(t, "tc", "qdisc", "show", "dev", "argo-test")
	if strings.Contains(qdiscs, "a400:") {
		t.Fatalf("Argo qdisc remains after cleanup: %q", qdiscs)
	}
	command := exec.Command("nft", "list", "table", "inet", "argo")
	if err := command.Run(); err == nil {
		t.Fatal("Argo nftables table remains after cleanup")
	}
}

func createPacketCgroup(t *testing.T) (qos.CgroupSelector, func()) {
	t.Helper()
	parent, err := qos.CurrentCgroup()
	if err != nil {
		t.Fatal(err)
	}
	name := "argo-qos-test-" + strconv.Itoa(os.Getpid()) + ".scope"
	path := filepath.Join("/sys/fs/cgroup", parent.Path, name)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Skipf("delegated cgroup is unavailable: %v", err)
	}
	selector := qos.CgroupSelector{Path: parent.Path + "/" + name, Level: parent.Level + 1}
	return selector, func() {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove delegated cgroup: %v", err)
		}
	}
}

func runKernelCommand(t *testing.T, name string, arguments ...string) string {
	t.Helper()
	output, err := exec.Command(name, arguments...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v: %s", name, arguments, err, output)
	}

	return string(output)
}

func formatMegabits(bits uint64) string {
	return fmt.Sprintf("rate %dMbit", bits/1_000_000)
}

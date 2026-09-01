package e2e_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/kristyancarvalho/argo/internal/qos"
	"github.com/kristyancarvalho/argo/internal/qosbackend"
)

func TestQoSKernelPolicyLifecycle(t *testing.T) {
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
	runKernelCommand(t, "ip", "link", "set", "argo-test", "up")
	cgroupID, err := qos.CurrentCgroupID()
	if err != nil {
		t.Fatal(err)
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
			CgroupID: cgroupID, ActiveDownloads: 1,
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
	rules := runKernelCommand(t, "nft", "list", "table", "inet", "argo")
	for _, value := range []string{"meta cgroup " + strconv.FormatUint(cgroupID, 10), "meta mark set", "0x0000a400"} {
		if !strings.Contains(rules, value) {
			t.Fatalf("classification rules %q do not contain %q", rules, value)
		}
	}
	if strings.Count(rules, "meta cgroup") != 1 {
		t.Fatalf("classification must mark only the Argo cgroup: %q", rules)
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

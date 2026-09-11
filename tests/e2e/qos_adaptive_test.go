package e2e_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/qos"
	"github.com/kristyancarvalho/argo/internal/qos/tc"
	"github.com/kristyancarvalho/argo/internal/qosbackend"
	"github.com/kristyancarvalho/argo/internal/telemetry"
)

const adaptiveMeasurementDuration = 12 * time.Second

type adaptiveMeasurement struct {
	phase   string
	latency time.Duration
	rate    uint64
	reason  qos.AdaptiveReason
	changed bool
}

func TestQoSAdaptiveControlLoop(t *testing.T) {
	if os.Getenv("ARGO_QOS_ADAPTIVE_NETNS") == "" {
		runQoSAdaptiveNamespace(t)
		return
	}
	testQoSAdaptiveControlLoop(t)
}

func runQoSAdaptiveNamespace(t *testing.T) {
	for _, executable := range []string{"unshare", "nsenter", "ip", "tc", "nft", "ping"} {
		if _, err := exec.LookPath(executable); err != nil {
			t.Skipf("%s is unavailable", executable)
		}
	}
	command := exec.Command("unshare", "-Urn", os.Args[0], "-test.run=^TestQoSAdaptiveControlLoop$", "-test.v")
	command.Env = append(os.Environ(), "ARGO_QOS_ADAPTIVE_NETNS=1")
	output, err := command.CombinedOutput()
	if err != nil {
		if strings.Contains(string(output), "Operation not permitted") {
			t.Skipf("unprivileged network namespaces are unavailable: %s", output)
		}
		t.Fatalf("isolated adaptive QoS measurement: %v: %s", err, output)
	}
	t.Log(strings.TrimSpace(string(output)))
}

func testQoSAdaptiveControlLoop(t *testing.T) {
	runKernelCommand(t, "ip", "link", "set", "lo", "up")
	server, signal, ready := startRxServerNamespace(t)
	t.Cleanup(func() {
		if server.ProcessState == nil {
			_ = server.Process.Kill()
			_ = server.Wait()
		}
	})
	runKernelCommand(t, "ip", "link", "add", "argo-rx-client", "type", "veth", "peer", "name", "argo-rx-server")
	runKernelCommand(t, "ip", "link", "set", "argo-rx-server", "netns", strconv.Itoa(server.Process.Pid))
	runKernelCommand(t, "ip", "address", "add", "10.232.0.1/24", "dev", "argo-rx-client")
	runKernelCommand(t, "ip", "-6", "address", "add", "2001:db8:232::1/64", "dev", "argo-rx-client", "nodad")
	runKernelCommand(t, "ip", "link", "set", "argo-rx-client", "up")
	serverCommand := func(arguments ...string) string {
		return runKernelCommand(t, "nsenter", append([]string{"-t", strconv.Itoa(server.Process.Pid), "-n"}, arguments...)...)
	}
	serverCommand("ip", "link", "set", "lo", "up")
	serverCommand("ip", "address", "add", "10.232.0.2/24", "dev", "argo-rx-server")
	serverCommand("ip", "-6", "address", "add", "2001:db8:232::2/64", "dev", "argo-rx-server", "nodad")
	serverCommand("ip", "link", "set", "argo-rx-server", "up")
	serverCommand("tc", "qdisc", "add", "dev", "argo-rx-server", "root", "handle", "1:", "htb", "default", "10")
	serverCommand("tc", "class", "add", "dev", "argo-rx-server", "parent", "1:", "classid", "1:10", "htb", "rate", "20mbit", "ceil", "20mbit")
	serverCommand("tc", "qdisc", "add", "dev", "argo-rx-server", "parent", "1:10", "handle", "10:", "netem", "delay", "10ms", "limit", "1000")
	if _, err := signal.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if line, err := ready.ReadString('\n'); err != nil || strings.TrimSpace(line) != "ARGO_RX_READY" {
		t.Fatalf("adaptive RX server did not start: %q, %v", line, err)
	}

	baseline := medianLatency(t, 5)
	estimator, err := telemetry.NewBaselineEstimator(telemetry.BaselineOptions{Manual: baseline})
	if err != nil {
		t.Fatal(err)
	}
	adaptive, err := qos.NewBackgroundController(2_000_000, 18_000_000, 15*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := qos.NewLatencyPolicy(estimator, adaptive)
	if err != nil {
		t.Fatal(err)
	}
	cgroup, cleanup := createPacketCgroup(t)
	t.Cleanup(cleanup)
	controller, err := qos.NewController(qosbackend.New())
	if err != nil {
		t.Fatal(err)
	}
	reconcileAdaptiveState(t, controller, cgroup, policy.Current().RateBitsPerSecond)
	t.Cleanup(func() {
		state, applied := controller.Current()
		if applied {
			if err := controller.Remove(context.Background(), state.Interface); err != nil {
				t.Error(err)
			}
		}
	})

	argo, argoSignal, argoOutput := startRxClientFor(t, rxIPv4Address, adaptiveMeasurementDuration)
	other, otherSignal, otherOutput := startRxClientFor(t, rxIPv4Address, 6*time.Second)
	for _, command := range []*exec.Cmd{argo, other} {
		command := command
		t.Cleanup(func() {
			if command.ProcessState == nil {
				_ = command.Process.Kill()
				_ = command.Wait()
			}
		})
	}
	if err := os.WriteFile("/sys/fs/cgroup/"+cgroup.Path+"/cgroup.procs", []byte(strconv.Itoa(argo.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := argoSignal.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if _, err := otherSignal.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}

	measurements := observeAdaptivePhase(t, "warmup", 4, policy, controller, cgroup)
	serverCommand("tc", "qdisc", "change", "dev", "argo-rx-server", "parent", "1:10", "handle", "10:", "netem", "delay", "35ms", "limit", "1000")
	measurements = append(measurements, observeAdaptivePhase(t, "contended", 4, policy, controller, cgroup)...)
	otherBytes := finishRxClient(t, other, otherOutput)
	serverCommand("tc", "qdisc", "change", "dev", "argo-rx-server", "parent", "1:10", "handle", "10:", "netem", "delay", "10ms", "limit", "1000")
	measurements = append(measurements, observeAdaptivePhase(t, "recovery", 4, policy, controller, cgroup)...)
	argoBytes := finishRxClient(t, argo, argoOutput)

	assertAdaptiveMeasurements(t, baseline, measurements)
	if argoBytes <= 0 || otherBytes <= 0 {
		t.Fatalf("adaptive traffic workers did not receive data: Argo=%d other=%d", argoBytes, otherBytes)
	}
	t.Logf("adaptive baseline=%s Argo=%.2fMbit/s other=%.2fMbit/s", baseline, bitsPerSecond(argoBytes, adaptiveMeasurementDuration)/1_000_000, bitsPerSecond(otherBytes, 6*time.Second)/1_000_000)
	for _, measurement := range measurements {
		t.Logf("adaptive phase=%s latency=%s rate=%.2fMbit/s changed=%t reason=%s", measurement.phase, measurement.latency, float64(measurement.rate)/1_000_000, measurement.changed, measurement.reason)
	}

	runKernelCommand(t, "ip", "link", "set", "argo-rx-client", "down")
	if err := controller.Reconcile(context.Background(), qos.DesiredState{Policy: qos.PolicyOff}); err != nil {
		t.Fatal(err)
	}
	assertAdaptiveStateRemoved(t)
	runKernelCommand(t, "ip", "link", "set", "argo-rx-client", "up")
	reconcileAdaptiveState(t, controller, cgroup, policy.Current().RateBitsPerSecond)
	assertAdaptiveStateApplied(t)

	serverCommand("tc", "qdisc", "change", "dev", "argo-rx-server", "parent", "1:10", "handle", "10:", "netem", "delay", "10ms", "loss", "100%", "limit", "1000")
	if _, err := probeLatency(); err == nil {
		t.Fatal("100% probe loss unexpectedly produced a latency sample")
	}
	before := policy.Current().RateBitsPerSecond
	for range 2 {
		state := policy.Observe(telemetry.Snapshot{})
		if state.Changed {
			reconcileAdaptiveState(t, controller, cgroup, state.RateBitsPerSecond)
		}
	}
	after := policy.Current().RateBitsPerSecond
	if after >= before || after < 2_000_000 {
		t.Fatalf("missing telemetry fallback was not bounded: before=%d after=%d", before, after)
	}
	t.Logf("adaptive loss=100%% telemetry=missing rate_before=%.2fMbit/s rate_after=%.2fMbit/s", float64(before)/1_000_000, float64(after)/1_000_000)
}

func observeAdaptivePhase(t *testing.T, phase string, samples int, policy *qos.LatencyPolicy, controller *qos.Controller, cgroup qos.CgroupSelector) []adaptiveMeasurement {
	t.Helper()
	measurements := make([]adaptiveMeasurement, 0, samples)
	for range samples {
		latency := medianLatency(t, 3)
		state := policy.Observe(telemetry.Snapshot{
			ActiveTransfers:         1,
			AggregateBytesPerSecond: 1,
			Latency:                 latency,
			LatencyAvailable:        true,
			LatencySampledAt:        time.Now().UTC(),
		})
		if state.Changed {
			reconcileAdaptiveState(t, controller, cgroup, state.RateBitsPerSecond)
		}
		measurements = append(measurements, adaptiveMeasurement{phase: phase, latency: latency, rate: state.RateBitsPerSecond, reason: state.Reason, changed: state.Changed})
	}
	return measurements
}

func reconcileAdaptiveState(t *testing.T, controller *qos.Controller, cgroup qos.CgroupSelector, rate uint64) {
	t.Helper()
	state, err := qos.MapAdaptivePolicyFor(qos.PolicyBackground, qos.PolicyEnvironment{
		Interface: "argo-rx-client", LinkRateBitsPerSecond: 20_000_000,
		Cgroup: cgroup, ActiveDownloads: 1,
	}, rate)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Reconcile(context.Background(), state); err != nil {
		t.Fatal(err)
	}
}

func medianLatency(t *testing.T, count int) time.Duration {
	t.Helper()
	values := make([]time.Duration, 0, 3)
	for range 3 {
		latency, err := probeLatencyCount(count)
		if err != nil {
			t.Fatal(err)
		}
		values = append(values, latency)
	}
	sort.Slice(values, func(left, right int) bool { return values[left] < values[right] })
	return values[len(values)/2]
}

func probeLatency() (time.Duration, error) {
	return probeLatencyCount(3)
}

func probeLatencyCount(count int) (time.Duration, error) {
	output, err := exec.Command("ping", "-n", "-q", "-c", strconv.Itoa(count), "-i", "0.1", "-W", "1", "10.232.0.2").CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("probe latency: %w: %s", err, output)
	}
	match := regexp.MustCompile(`= [0-9.]+/([0-9.]+)/`).FindStringSubmatch(string(output))
	if len(match) != 2 {
		return 0, fmt.Errorf("parse ping latency: %s", output)
	}
	milliseconds, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return 0, err
	}
	return time.Duration(milliseconds * float64(time.Millisecond)), nil
}

func assertAdaptiveMeasurements(t *testing.T, baseline time.Duration, measurements []adaptiveMeasurement) {
	t.Helper()
	if len(measurements) != 12 {
		t.Fatalf("unexpected adaptive measurement count: %d", len(measurements))
	}
	minimum := uint64(18_000_000)
	for _, measurement := range measurements {
		if measurement.rate < 2_000_000 || measurement.rate > 18_000_000 {
			t.Fatalf("adaptive rate escaped bounds: %+v", measurement)
		}
		minimum = min(minimum, measurement.rate)
	}
	warmupRate := measurements[3].rate
	contendedRate := measurements[7].rate
	recoveryRate := measurements[11].rate
	if warmupRate <= 2_000_000 {
		t.Fatalf("background controller did not reclaim idle capacity: rate=%d", warmupRate)
	}
	if contendedRate >= warmupRate || minimum != 2_000_000 {
		t.Fatal("adaptive controller did not yield during elevated latency")
	}
	if recoveryRate <= contendedRate {
		t.Fatalf("adaptive controller did not reclaim capacity: contended=%d recovery=%d", contendedRate, recoveryRate)
	}
	for start := 0; start < len(measurements); start += 4 {
		direction := 0
		previous := measurements[start].rate
		for _, measurement := range measurements[start+1 : start+4] {
			change := 0
			if measurement.rate < previous {
				change = -1
			} else if measurement.rate > previous {
				change = 1
			}
			if change != 0 && direction != 0 && change != direction {
				t.Fatalf("adaptive controller oscillated within phase: %+v", measurements[start:start+4])
			}
			if change != 0 {
				direction = change
			}
			previous = measurement.rate
		}
	}
	for _, measurement := range measurements[4:8] {
		if measurement.latency <= baseline+15*time.Millisecond {
			t.Fatalf("contended latency did not exceed SLO: baseline=%s measurement=%+v", baseline, measurement)
		}
	}
}

func assertAdaptiveStateRemoved(t *testing.T) {
	t.Helper()
	if err := exec.Command("ip", "link", "show", "dev", tc.IFBInterface("argo-rx-client")).Run(); err == nil {
		t.Fatal("adaptive IFB remains after disconnect cleanup")
	}
	if err := exec.Command("nft", "list", "table", "inet", "argo").Run(); err == nil {
		t.Fatal("adaptive nftables state remains after disconnect cleanup")
	}
}

func assertAdaptiveStateApplied(t *testing.T) {
	t.Helper()
	runKernelCommand(t, "ip", "link", "show", "dev", tc.IFBInterface("argo-rx-client"))
	runKernelCommand(t, "nft", "list", "table", "inet", "argo")
}

func bitsPerSecond(bytes int64, duration time.Duration) float64 {
	return float64(bytes*8) / duration.Seconds()
}

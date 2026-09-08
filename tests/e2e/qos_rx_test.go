package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/qos"
	"github.com/kristyancarvalho/argo/internal/qos/tc"
	"github.com/kristyancarvalho/argo/internal/qosbackend"
)

const (
	rxIPv4Address         = "10.232.0.2:38081"
	rxIPv6Address         = "[2001:db8:232::2]:38081"
	rxMeasurementDuration = 3 * time.Second
)

func TestQoSRxTrafficShare(t *testing.T) {
	if os.Getenv("ARGO_QOS_RX_SERVER") != "" {
		runRxServerWorker(t)
		return
	}
	if address := os.Getenv("ARGO_QOS_RX_CLIENT"); address != "" {
		runRxClientWorker(t, address)
		return
	}
	if os.Getenv("ARGO_QOS_RX_NETNS") == "" {
		runQoSRxNamespace(t)
		return
	}
	testQoSRxTrafficShare(t)
}

func runQoSRxNamespace(t *testing.T) {
	for _, executable := range []string{"unshare", "nsenter", "ip", "tc", "nft"} {
		if _, err := exec.LookPath(executable); err != nil {
			t.Skipf("%s is unavailable", executable)
		}
	}
	command := exec.Command("unshare", "-Urn", os.Args[0], "-test.run=^TestQoSRxTrafficShare$")
	command.Env = append(os.Environ(), "ARGO_QOS_RX_NETNS=1")
	output, err := command.CombinedOutput()
	if err != nil {
		if strings.Contains(string(output), "Operation not permitted") {
			t.Skipf("unprivileged network namespaces are unavailable: %s", output)
		}
		t.Fatalf("isolated RX QoS measurement: %v: %s", err, output)
	}
}

func testQoSRxTrafficShare(t *testing.T) {
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
		t.Fatalf("RX server did not start: %q, %v", line, err)
	}
	cgroup, cleanup := createPacketCgroup(t)
	t.Cleanup(cleanup)
	offArgo, offOther := measureRxPair(t, rxIPv4Address, cgroup)
	backend := qosbackend.New()
	state, err := qos.MapPolicy(qos.PolicyThroughput, qos.PolicyEnvironment{
		Interface: "argo-rx-client", LinkRateBitsPerSecond: 20_000_000,
		Cgroup: cgroup, ActiveDownloads: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.Apply(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := backend.Remove(context.Background(), "argo-rx-client"); err != nil {
			t.Error(err)
		}
	})
	ipv4Argo, ipv4Other := measureRxPair(t, rxIPv4Address, cgroup)
	ipv6Argo, ipv6Other := measureRxPair(t, rxIPv6Address, cgroup)
	if offArgo*2 < offOther || offOther*2 < offArgo {
		t.Fatalf("unshaped baseline is unexpectedly asymmetric: Argo=%d other=%d", offArgo, offOther)
	}
	for family, measurement := range map[string][2]int64{
		"IPv4": {ipv4Argo, ipv4Other},
		"IPv6": {ipv6Argo, ipv6Other},
	} {
		if measurement[0] < measurement[1]*2 {
			t.Fatalf("%s RX shaping did not prioritize Argo: Argo=%d other=%d", family, measurement[0], measurement[1])
		}
	}
	adaptive, err := qos.MapAdaptivePolicy(qos.PolicyEnvironment{
		Interface: "argo-rx-client", LinkRateBitsPerSecond: 20_000_000,
		Cgroup: cgroup, ActiveDownloads: 1,
	}, 4_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.Apply(context.Background(), adaptive); err != nil {
		t.Fatal(err)
	}
	limitedArgo, limitedOther := measureRxPair(t, rxIPv4Address, cgroup)
	if limitedArgo >= ipv4Argo/2 {
		t.Fatalf("adaptive ceiling did not reduce classified RX throughput: before=%d after=%d", ipv4Argo, limitedArgo)
	}
	if limitedArgo*2 >= limitedOther {
		t.Fatalf("adaptive ceiling did not constrain Argo below competing traffic: Argo=%d other=%d", limitedArgo, limitedOther)
	}
	limitedBitsPerSecond := uint64(limitedArgo) * 8 / uint64(rxMeasurementDuration/time.Second)
	if limitedBitsPerSecond > 6_000_000 {
		t.Fatalf("4 Mbit/s adaptive ceiling allowed %d bit/s", limitedBitsPerSecond)
	}
	classes := runKernelCommand(t, "tc", "-s", "class", "show", "dev", tc.IFBInterface("argo-rx-client"))
	if !strings.Contains(classes, "class htb a400:10") || !strings.Contains(classes, "class htb a400:20") {
		t.Fatalf("RX traffic did not traverse both IFB classes: %s", classes)
	}
}

func startRxServerNamespace(t *testing.T) (*exec.Cmd, io.WriteCloser, *bufio.Reader) {
	t.Helper()
	command := exec.Command("unshare", "-n", os.Args[0], "-test.run=^TestQoSRxTrafficShare$")
	command.Env = append(os.Environ(), "ARGO_QOS_RX_SERVER=1")
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}

	return command, input, bufio.NewReader(output)
}

func runRxServerWorker(t *testing.T) {
	var signal [1]byte
	if _, err := os.Stdin.Read(signal[:]); err != nil {
		t.Fatal(err)
	}
	listeners := make([]net.Listener, 0, 2)
	for _, address := range []string{rxIPv4Address, rxIPv6Address} {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, listener)
	}
	for _, listener := range listeners {
		go serveRxConnections(listener)
	}
	_, _ = fmt.Fprintln(os.Stdout, "ARGO_RX_READY")
	select {}
}

func serveRxConnections(listener net.Listener) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer func() {
				_ = connection.Close()
			}()
			block := make([]byte, 64*1024)
			for {
				if _, err := connection.Write(block); err != nil {
					return
				}
			}
		}()
	}
}

func measureRxPair(t *testing.T, address string, cgroup qos.CgroupSelector) (int64, int64) {
	t.Helper()
	argo, argoSignal, argoOutput := startRxClient(t, address)
	other, otherSignal, otherOutput := startRxClient(t, address)
	for _, command := range []*exec.Cmd{argo, other} {
		t.Cleanup(func() {
			if command.ProcessState == nil {
				_ = command.Process.Kill()
				_ = command.Wait()
			}
		})
	}
	if err := os.WriteFile(filepath.Join("/sys/fs/cgroup", cgroup.Path, "cgroup.procs"), []byte(strconv.Itoa(argo.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := argoSignal.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if _, err := otherSignal.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	argoBytes := finishRxClient(t, argo, argoOutput)
	otherBytes := finishRxClient(t, other, otherOutput)
	time.Sleep(100 * time.Millisecond)

	return argoBytes, otherBytes
}

func startRxClient(t *testing.T, address string) (*exec.Cmd, io.WriteCloser, *bytes.Buffer) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestQoSRxTrafficShare$")
	command.Env = append(os.Environ(), "ARGO_QOS_RX_CLIENT="+address)
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}

	return command, input, &output
}

func runRxClientWorker(t *testing.T, address string) {
	var signal [1]byte
	if _, err := os.Stdin.Read(signal[:]); err != nil {
		t.Fatal(err)
	}
	connection, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = connection.Close()
	}()
	if err := connection.SetReadDeadline(time.Now().Add(rxMeasurementDuration)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 64*1024)
	var received int64
	for {
		count, err := connection.Read(buffer)
		received += int64(count)
		if err != nil {
			break
		}
	}
	_, _ = fmt.Fprintf(os.Stdout, "ARGO_RX_BYTES=%d\n", received)
}

func finishRxClient(t *testing.T, command *exec.Cmd, output *bytes.Buffer) int64 {
	t.Helper()
	if err := command.Wait(); err != nil {
		t.Fatalf("RX client failed: %v: %s", err, output.String())
	}
	for _, line := range strings.Split(output.String(), "\n") {
		value, found := strings.CutPrefix(line, "ARGO_RX_BYTES=")
		if !found {
			continue
		}
		bytes, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return bytes
	}
	t.Fatalf("RX client did not report bytes: %s", output.String())
	return 0
}

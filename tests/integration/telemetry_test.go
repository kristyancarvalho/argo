package integration_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/telemetry"
)

func TestTCPProbeMeasuresConfiguredEndpoint(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = listener.Close()
	}()
	accepted := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err == nil {
			err = connection.Close()
		}
		accepted <- err
	}()
	probe, err := telemetry.NewTCPProbe(listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	latency, err := probe.Measure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if latency < 0 || latency > time.Second {
		t.Fatalf("measured latency is %s", latency)
	}
	if err := <-accepted; err != nil {
		t.Fatal(err)
	}
}

func TestTCPProbeReturnsMissingSignalForUnavailableEndpoint(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	probe, err := telemetry.NewTCPProbe(address, 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := probe.Measure(context.Background()); err == nil {
		t.Fatal("unavailable endpoint produced a latency signal")
	}
}

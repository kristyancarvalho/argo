package telemetry

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

type TCPProbe struct {
	address string
	timeout time.Duration
	now     func() time.Time
	dial    func(context.Context, string, string) (net.Conn, error)
}

func NewTCPProbe(address string, timeout time.Duration) (*TCPProbe, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || strings.TrimSpace(host) == "" || strings.TrimSpace(port) == "" {
		return nil, fmt.Errorf("latency probe target must use host:port")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("latency probe timeout must be positive")
	}
	dialer := net.Dialer{Timeout: timeout}

	return &TCPProbe{
		address: address,
		timeout: timeout,
		now:     time.Now,
		dial:    dialer.DialContext,
	}, nil
}

func (probe *TCPProbe) Measure(ctx context.Context) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, probe.timeout)
	defer cancel()
	started := probe.now()
	connection, err := probe.dial(ctx, "tcp", probe.address)
	latency := probe.now().Sub(started)
	if err != nil {
		return 0, fmt.Errorf("measure TCP latency to %s: %w", probe.address, err)
	}
	if err := connection.Close(); err != nil {
		return 0, fmt.Errorf("close latency probe connection: %w", err)
	}
	if latency < 0 {
		return 0, fmt.Errorf("latency probe clock moved backwards")
	}

	return latency, nil
}

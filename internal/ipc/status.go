package ipc

import (
	"context"
	"os"
	"time"
)

type Status struct {
	State           string        `json:"state"`
	PID             int           `json:"pid"`
	StartedAt       time.Time     `json:"started_at"`
	ProtocolVersion int           `json:"protocol_version"`
	Network         NetworkStatus `json:"network"`
	ActiveProfile   string        `json:"active_profile"`
	Traffic         TrafficStatus `json:"traffic"`
}

type TrafficStatus struct {
	Policy                   string        `json:"policy"`
	Applied                  bool          `json:"applied"`
	CurrentRateBitsPerSecond uint64        `json:"current_rate_bits_per_second"`
	MeasuredLatency          time.Duration `json:"measured_latency"`
	LatencyAvailable         bool          `json:"latency_available"`
	BaselineLatency          time.Duration `json:"baseline_latency"`
	BaselineAvailable        bool          `json:"baseline_available"`
	ControllerState          string        `json:"controller_state"`
	Error                    string        `json:"error"`
}

type NetworkStatus struct {
	Available        bool   `json:"available"`
	Connected        bool   `json:"connected"`
	State            string `json:"state"`
	Connectivity     string `json:"connectivity"`
	ActiveConnection string `json:"active_connection"`
	ConnectionType   string `json:"connection_type"`
	Interface        string `json:"interface"`
	Metered          string `json:"metered"`
}

type Handler interface {
	Handle(context.Context, Request) (any, error)
}

type StatusHandler struct {
	startedAt time.Time
	processID int
}

func NewStatusHandler() *StatusHandler {
	return &StatusHandler{
		startedAt: time.Now().UTC(),
		processID: os.Getpid(),
	}
}

func (handler *StatusHandler) Handle(_ context.Context, request Request) (any, error) {
	if request.Operation != OperationStatus {
		return nil, UnsupportedOperationError{Operation: request.Operation}
	}

	return handler.Status(), nil
}

func (handler *StatusHandler) Status() Status {
	return Status{
		State:           "running",
		PID:             handler.processID,
		StartedAt:       handler.startedAt,
		ProtocolVersion: ProtocolVersion,
	}
}

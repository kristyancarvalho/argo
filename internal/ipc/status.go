package ipc

import (
	"context"
	"os"
	"time"
)

type Status struct {
	State           string    `json:"state"`
	PID             int       `json:"pid"`
	StartedAt       time.Time `json:"started_at"`
	ProtocolVersion int       `json:"protocol_version"`
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

	return Status{
		State:           "running",
		PID:             handler.processID,
		StartedAt:       handler.startedAt,
		ProtocolVersion: ProtocolVersion,
	}, nil
}

package model

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

const downloadIDBytes = 16

type DownloadID string

func NewDownloadID() (DownloadID, error) {
	value := make([]byte, downloadIDBytes)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate download identifier: %w", err)
	}

	return DownloadID(hex.EncodeToString(value)), nil
}

func ParseDownloadID(value string) (DownloadID, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != downloadIDBytes {
		return "", InvalidDownloadIDError{Value: value}
	}

	return DownloadID(value), nil
}

func (id DownloadID) String() string {
	return string(id)
}

type Status string

const (
	StatusQueued      Status = "queued"
	StatusResolving   Status = "resolving"
	StatusDownloading Status = "downloading"
	StatusPaused      Status = "paused"
	StatusCompleted   Status = "completed"
	StatusFailed      Status = "failed"
	StatusCanceled    Status = "canceled"
)

var statuses = []Status{
	StatusQueued,
	StatusResolving,
	StatusDownloading,
	StatusPaused,
	StatusCompleted,
	StatusFailed,
	StatusCanceled,
}

var transitions = map[Status]map[Status]struct{}{
	StatusQueued: {
		StatusResolving:   {},
		StatusDownloading: {},
		StatusPaused:      {},
		StatusCanceled:    {},
	},
	StatusResolving: {
		StatusDownloading: {},
		StatusPaused:      {},
		StatusFailed:      {},
		StatusCanceled:    {},
	},
	StatusDownloading: {
		StatusPaused:    {},
		StatusCompleted: {},
		StatusFailed:    {},
		StatusCanceled:  {},
	},
	StatusPaused: {
		StatusDownloading: {},
		StatusCanceled:    {},
	},
	StatusFailed: {
		StatusQueued:   {},
		StatusCanceled: {},
	},
	StatusCompleted: {},
	StatusCanceled:  {},
}

func ParseStatus(value string) (Status, error) {
	status := Status(value)
	for _, candidate := range statuses {
		if status == candidate {
			return status, nil
		}
	}

	return "", InvalidStatusError{Value: value}
}

func ValidateTransition(from, to Status) error {
	if _, err := ParseStatus(string(from)); err != nil {
		return err
	}
	if _, err := ParseStatus(string(to)); err != nil {
		return err
	}
	if _, valid := transitions[from][to]; !valid {
		return InvalidTransitionError{From: from, To: to}
	}

	return nil
}

type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityNormal Priority = "normal"
	PriorityHigh   Priority = "high"
)

var priorities = []Priority{
	PriorityLow,
	PriorityNormal,
	PriorityHigh,
}

func ParsePriority(value string) (Priority, error) {
	priority := Priority(value)
	for _, candidate := range priorities {
		if priority == candidate {
			return priority, nil
		}
	}

	return "", InvalidPriorityError{Value: value}
}

type Download struct {
	ID              DownloadID
	URL             string
	Destination     string
	Filename        string
	TotalSize       int64
	DownloadedBytes int64
	Status          Status
	Priority        Priority
	CreatedAt       time.Time
	UpdatedAt       time.Time
	StartedAt       time.Time
	CompletedAt     time.Time
	ETag            string
	LastModified    string
	RangeSupported  bool
	Error           string
}

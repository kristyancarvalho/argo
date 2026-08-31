package ipc

import (
	"encoding/json"
	"fmt"
	"time"
)

const ProtocolVersion = 1

type Operation string

const (
	OperationStatus   Operation = "status"
	OperationAdd      Operation = "add"
	OperationPause    Operation = "pause"
	OperationResume   Operation = "resume"
	OperationCancel   Operation = "cancel"
	OperationPriority Operation = "priority"
	OperationList     Operation = "list"
	OperationShow     Operation = "show"
	OperationProfile  Operation = "profile"
)

type Request struct {
	Version   int             `json:"version"`
	ID        string          `json:"id"`
	Operation Operation       `json:"operation"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

type Response struct {
	Version int             `json:"version"`
	ID      string          `json:"id"`
	OK      bool            `json:"ok"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *ResponseError  `json:"error,omitempty"`
}

type ResponseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type AddRequest struct {
	URL         string `json:"url"`
	Destination string `json:"destination"`
}

type AddResponse struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	Destination string `json:"destination"`
	Status      string `json:"status"`
}

type DownloadActionRequest struct {
	ID string `json:"id"`
}

type DownloadActionResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type PriorityRequest struct {
	ID       string `json:"id"`
	Priority string `json:"priority"`
}

type PriorityResponse struct {
	ID       string `json:"id"`
	Priority string `json:"priority"`
}

type ShowRequest struct {
	ID string `json:"id"`
}

type ProfileRequest struct {
	Name string `json:"name"`
}

type ProfileResponse struct {
	Name                   string `json:"name"`
	BytesPerSecond         int64  `json:"bytes_per_second"`
	DefaultPriority        string `json:"default_priority"`
	MaxConcurrentDownloads int    `json:"max_concurrent_downloads"`
	PauseOnMetered         bool   `json:"pause_on_metered"`
	ResumeAfterMetered     bool   `json:"resume_after_metered"`
	Policy                 string `json:"policy"`
}

type Download struct {
	ID              string    `json:"id"`
	URL             string    `json:"url"`
	Destination     string    `json:"destination"`
	Filename        string    `json:"filename"`
	TotalSize       int64     `json:"total_size"`
	DownloadedBytes int64     `json:"downloaded_bytes"`
	Status          string    `json:"status"`
	Priority        string    `json:"priority"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	Error           string    `json:"error,omitempty"`
}

type ListResponse struct {
	Downloads []Download `json:"downloads"`
}

type RemoteError struct {
	Code    string
	Message string
}

type CodedError interface {
	error
	Code() string
}

func (err RemoteError) Error() string {
	return fmt.Sprintf("daemon error %s: %s", err.Code, err.Message)
}

type UnsupportedOperationError struct {
	Operation Operation
}

func (err UnsupportedOperationError) Error() string {
	return fmt.Sprintf("unsupported operation %q", err.Operation)
}

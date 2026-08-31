package ipc

import (
	"encoding/json"
	"fmt"
)

const ProtocolVersion = 1

type Operation string

const (
	OperationStatus Operation = "status"
	OperationAdd    Operation = "add"
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

package qosipc

import (
	"encoding/json"
	"fmt"

	"github.com/kristyancarvalho/argo/internal/qos"
)

const ProtocolVersion = 1

type Operation string

const (
	OperationStatus Operation = "status"
	OperationApply  Operation = "apply"
	OperationRemove Operation = "remove"
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

type ApplyRequest struct {
	State qos.DesiredState `json:"state"`
}

type RemoveRequest struct {
	Interface string `json:"interface"`
}

type Status struct {
	Applied bool             `json:"applied"`
	State   qos.DesiredState `json:"state"`
}

type RemoteError struct {
	Code    string
	Message string
}

func (err RemoteError) Error() string {
	return fmt.Sprintf("QoS helper error %s: %s", err.Code, err.Message)
}

type RequestError struct {
	ErrorCode string
	Message   string
}

func (err RequestError) Error() string {
	return err.Message
}

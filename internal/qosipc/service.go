package qosipc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/kristyancarvalho/argo/internal/qos"
)

type Service struct {
	controller *qos.Controller
}

func NewService(controller *qos.Controller) (*Service, error) {
	if controller == nil {
		return nil, fmt.Errorf("QoS controller is required")
	}

	return &Service{controller: controller}, nil
}

func (service *Service) Handle(ctx context.Context, request Request) (any, error) {
	switch request.Operation {
	case OperationStatus:
		if len(request.Payload) != 0 && string(request.Payload) != "null" {
			return nil, RequestError{ErrorCode: "invalid_request", Message: "status payload must be empty"}
		}
		return service.status(), nil
	case OperationApply:
		var payload ApplyRequest
		if err := decodePayload(request.Payload, &payload); err != nil {
			return nil, err
		}
		if !payload.State.Enabled {
			return nil, RequestError{ErrorCode: "invalid_request", Message: "apply requires enabled desired state"}
		}
		if err := payload.State.Validate(); err != nil {
			return nil, RequestError{ErrorCode: "invalid_request", Message: err.Error()}
		}
		if err := service.controller.Reconcile(ctx, payload.State); err != nil {
			return nil, err
		}
		return service.status(), nil
	case OperationRemove:
		var payload RemoveRequest
		if err := decodePayload(request.Payload, &payload); err != nil {
			return nil, err
		}
		if err := qos.ValidateInterface(payload.Interface); err != nil {
			return nil, RequestError{ErrorCode: "invalid_request", Message: err.Error()}
		}
		current, applied := service.controller.Current()
		if applied && current.Interface != payload.Interface {
			return nil, RequestError{
				ErrorCode: "invalid_request",
				Message:   fmt.Sprintf("interface %q does not own current QoS state", payload.Interface),
			}
		}
		if err := service.controller.Reconcile(ctx, qos.DesiredState{Policy: qos.PolicyOff}); err != nil {
			return nil, err
		}
		return service.status(), nil
	default:
		return nil, RequestError{ErrorCode: "unsupported_operation", Message: fmt.Sprintf("unsupported operation %q", request.Operation)}
	}
}

func (service *Service) status() Status {
	state, applied := service.controller.Current()
	return Status{Applied: applied, State: state}
}

func decodePayload(payload json.RawMessage, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return RequestError{ErrorCode: "invalid_request", Message: "invalid request payload"}
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return RequestError{ErrorCode: "invalid_request", Message: "request payload contains trailing data"}
	}

	return nil
}

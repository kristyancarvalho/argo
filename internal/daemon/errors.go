package daemon

import (
	"fmt"

	"github.com/kristyancarvalho/argo/internal/model"
)

type InvalidAddRequestError struct {
	Reason string
}

func (err InvalidAddRequestError) Error() string {
	return fmt.Sprintf("invalid add request: %s", err.Reason)
}

func (err InvalidAddRequestError) Code() string {
	return "invalid_request"
}

type InvalidDownloadActionError struct {
	ID     string
	Action string
	Status model.Status
	Reason string
}

func (err InvalidDownloadActionError) Error() string {
	if err.Reason != "" {
		return fmt.Sprintf("invalid %s request for download %q: %s", err.Action, err.ID, err.Reason)
	}

	return fmt.Sprintf("cannot %s download %q in status %q", err.Action, err.ID, err.Status)
}

func (err InvalidDownloadActionError) Code() string {
	return "invalid_request"
}

type UnknownProfileError struct {
	Name string
}

func (err UnknownProfileError) Error() string {
	return fmt.Sprintf("unknown profile %q", err.Name)
}

func (err UnknownProfileError) Code() string {
	return "unknown_profile"
}

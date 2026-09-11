package cli

import (
	"errors"
	"fmt"

	"github.com/kristyancarvalho/argo/internal/ipc"
)

const (
	ExitSuccess           = 0
	ExitGeneralFailure    = 1
	ExitUsage             = 2
	ExitDaemonUnavailable = 3
	ExitItemNotFound      = 4
	ExitNetworkFailure    = 5
	ExitPolicyUnavailable = 6
)

type UsageError struct {
	Message string
}

type DoctorError struct {
	DaemonUnavailable bool
}

func (err DoctorError) Error() string {
	return "doctor found unavailable core capabilities"
}

func (err UsageError) Error() string {
	return fmt.Sprintf("usage error: %s", err.Message)
}

func ExitCode(err error) int {
	if err == nil {
		return ExitSuccess
	}
	var usage UsageError
	if errors.As(err, &usage) {
		return ExitUsage
	}
	var unavailable ipc.DaemonUnavailableError
	if errors.As(err, &unavailable) {
		return ExitDaemonUnavailable
	}
	var doctor DoctorError
	if errors.As(err, &doctor) {
		if doctor.DaemonUnavailable {
			return ExitDaemonUnavailable
		}
		return ExitGeneralFailure
	}
	var remote ipc.RemoteError
	if errors.As(err, &remote) {
		switch remote.Code {
		case "not_found":
			return ExitItemNotFound
		case "network_failure":
			return ExitNetworkFailure
		case "policy_unavailable":
			return ExitPolicyUnavailable
		case "invalid_request", "unknown_profile":
			return ExitUsage
		}
	}
	return ExitGeneralFailure
}

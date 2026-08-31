package cli

import "fmt"

type UsageError struct {
	Message string
}

func (err UsageError) Error() string {
	return fmt.Sprintf("usage error: %s", err.Message)
}

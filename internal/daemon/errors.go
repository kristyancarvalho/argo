package daemon

import "fmt"

type InvalidAddRequestError struct {
	Reason string
}

func (err InvalidAddRequestError) Error() string {
	return fmt.Sprintf("invalid add request: %s", err.Reason)
}

func (err InvalidAddRequestError) Code() string {
	return "invalid_request"
}

type QueueFullError struct {
	Capacity int
}

func (err QueueFullError) Error() string {
	return fmt.Sprintf("download queue capacity %d reached", err.Capacity)
}

func (err QueueFullError) Code() string {
	return "queue_full"
}

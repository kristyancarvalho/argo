package model

import "fmt"

type InvalidDownloadIDError struct {
	Value string
}

func (err InvalidDownloadIDError) Error() string {
	return fmt.Sprintf("invalid download identifier %q", err.Value)
}

type InvalidStatusError struct {
	Value string
}

func (err InvalidStatusError) Error() string {
	return fmt.Sprintf("invalid download status %q", err.Value)
}

type InvalidPriorityError struct {
	Value string
}

func (err InvalidPriorityError) Error() string {
	return fmt.Sprintf("invalid download priority %q", err.Value)
}

func (err InvalidPriorityError) Code() string {
	return "invalid_priority"
}

type InvalidTransitionError struct {
	From Status
	To   Status
}

func (err InvalidTransitionError) Error() string {
	return fmt.Sprintf("invalid download transition from %q to %q", err.From, err.To)
}

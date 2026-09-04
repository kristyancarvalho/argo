package downloader

import "fmt"

type HTTPStatusError struct {
	StatusCode int
	Status     string
}

func (err HTTPStatusError) Error() string {
	return fmt.Sprintf("HTTP request failed with status %s", err.Status)
}

type DestinationExistsError struct {
	Path string
}

type ResumeUnavailableError struct {
	ID     string
	Reason string
}

func (err ResumeUnavailableError) Error() string {
	return fmt.Sprintf("download %s cannot resume: %s; use retry to start a new transfer", err.ID, err.Reason)
}

type InvalidChunkPlanError struct {
	Reason string
}

func (err InvalidChunkPlanError) Error() string {
	return fmt.Sprintf("invalid chunk plan: %s", err.Reason)
}

type RangeMismatchError struct {
	Chunk        Chunk
	ContentRange string
}

func (err RangeMismatchError) Error() string {
	return fmt.Sprintf(
		"range response %q does not match chunk %d bytes %d-%d",
		err.ContentRange,
		err.Chunk.Index,
		err.Chunk.Start,
		err.Chunk.End,
	)
}

func (err DestinationExistsError) Error() string {
	return fmt.Sprintf("download destination already exists: %s", err.Path)
}

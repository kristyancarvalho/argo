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

func (err DestinationExistsError) Error() string {
	return fmt.Sprintf("download destination already exists: %s", err.Path)
}

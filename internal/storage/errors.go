package storage

import (
	"errors"
	"fmt"
)

var ErrDownloadNotFound = errors.New("download not found")

type SchemaTooNewError struct {
	Found     int
	Supported int
}

func (err SchemaTooNewError) Error() string {
	return fmt.Sprintf("database schema version %d is newer than supported version %d", err.Found, err.Supported)
}

type InvalidProgressError struct {
	Downloaded int64
	Total      int64
}

func (err InvalidProgressError) Error() string {
	return fmt.Sprintf("invalid downloaded byte count %d for total size %d", err.Downloaded, err.Total)
}

package downloader

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
)

type RemoteMetadata struct {
	TotalSize      int64
	RangeSupported bool
	ETag           string
	LastModified   string
}

type Inspector struct {
	httpClient *http.Client
}

func NewInspector(httpClient *http.Client) *Inspector {
	return &Inspector{httpClient: httpClient}
}

func (inspector *Inspector) Inspect(ctx context.Context, rawURL string) (RemoteMetadata, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return RemoteMetadata{}, fmt.Errorf("create metadata request: %w", err)
	}
	request.Header.Set("Range", "bytes=0-0")
	response, err := inspector.httpClient.Do(request)
	if err != nil {
		return RemoteMetadata{}, fmt.Errorf("request remote metadata: %w", err)
	}
	defer func() {
		_ = response.Body.Close()
	}()

	metadata := RemoteMetadata{
		TotalSize:    response.ContentLength,
		ETag:         response.Header.Get("ETag"),
		LastModified: response.Header.Get("Last-Modified"),
	}
	switch response.StatusCode {
	case http.StatusPartialContent:
		start, end, total, err := parseContentRange(response.Header.Get("Content-Range"))
		if err != nil {
			return RemoteMetadata{}, err
		}
		if start != 0 || end != 0 {
			return RemoteMetadata{}, fmt.Errorf("metadata range response returned bytes %d-%d", start, end)
		}
		metadata.TotalSize = total
		metadata.RangeSupported = true
	case http.StatusRequestedRangeNotSatisfiable:
		if response.Header.Get("Content-Range") != "bytes */0" {
			return RemoteMetadata{}, HTTPStatusError{
				StatusCode: response.StatusCode,
				Status:     response.Status,
			}
		}
		metadata.TotalSize = 0
	case http.StatusOK:
	case http.StatusNoContent:
		metadata.TotalSize = 0
	default:
		return RemoteMetadata{}, HTTPStatusError{
			StatusCode: response.StatusCode,
			Status:     response.Status,
		}
	}
	if value := response.Header.Get("Content-Length"); value == "" && !metadata.RangeSupported && response.StatusCode != http.StatusNoContent {
		metadata.TotalSize = -1
	} else if metadata.TotalSize < -1 {
		return RemoteMetadata{}, fmt.Errorf("invalid remote content length %s", strconv.FormatInt(metadata.TotalSize, 10))
	}

	return metadata, nil
}

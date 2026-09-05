package downloader

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

type RemoteMetadata struct {
	TotalSize      int64
	RangeSupported bool
	ETag           string
	LastModified   string
}

func (metadata RemoteMetadata) supportsBoundRequests() bool {
	return strongETag(metadata.ETag) != "" || metadata.LastModified != ""
}

func bindRepresentation(request *http.Request, metadata RemoteMetadata) {
	if etag := strongETag(metadata.ETag); etag != "" {
		request.Header.Set("If-Match", etag)
		return
	}
	if metadata.LastModified != "" {
		request.Header.Set("If-Unmodified-Since", metadata.LastModified)
	}
}

func validateRepresentation(response *http.Response, metadata RemoteMetadata) error {
	if response.StatusCode == http.StatusPreconditionFailed {
		return RepresentationChangedError{Expected: representationName(metadata), Actual: "a changed resource"}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil
	}
	if etag := strongETag(metadata.ETag); etag != "" {
		actual := response.Header.Get("ETag")
		if actual != etag {
			return RepresentationChangedError{Expected: etag, Actual: displayValidator(actual)}
		}
		return nil
	}
	if metadata.LastModified != "" {
		actual := response.Header.Get("Last-Modified")
		if actual != metadata.LastModified {
			return RepresentationChangedError{Expected: metadata.LastModified, Actual: displayValidator(actual)}
		}
	}

	return nil
}

func strongETag(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "W/") || len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return ""
	}

	return value
}

func representationName(metadata RemoteMetadata) string {
	if etag := strongETag(metadata.ETag); etag != "" {
		return etag
	}
	return metadata.LastModified
}

func displayValidator(value string) string {
	if value == "" {
		return "a response without the expected validator"
	}

	return value
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

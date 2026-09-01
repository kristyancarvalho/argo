package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kristyancarvalho/argo/internal/model"
)

const copyBufferSize = 32 * 1024

type Store interface {
	DownloadChunks(context.Context, model.DownloadID) ([]model.DownloadChunk, error)
	ReplaceDownloadChunks(context.Context, model.DownloadID, []model.DownloadChunk, time.Time) error
	ResetDownloadProgress(context.Context, model.DownloadID, time.Time) error
	UpdateChunkProgress(context.Context, model.DownloadID, int, int64, time.Time) error
	UpdateDownloadFilename(context.Context, model.DownloadID, string, time.Time) error
	UpdateDownloadProgress(context.Context, model.DownloadID, int64, time.Time) error
	UpdateRemoteMetadata(context.Context, model.DownloadID, int64, bool, string, string, time.Time) error
	UpdateDownloadStatus(context.Context, model.DownloadID, model.Status, time.Time, string) error
}

type Engine struct {
	httpClient *http.Client
	store      Store
	now        func() time.Time
	rateLimit  atomic.Int64
	planner    ChunkPlanner
	chunkCount int
	observer   func(model.DownloadID, ChunkProgress)
	parts      string
}

type Options struct {
	HTTPClient       *http.Client
	BytesPerSecond   int64
	MaximumChunks    int
	MinimumChunkSize int64
	ChunkProgress    func(model.DownloadID, ChunkProgress)
	PartsDirectory   string
}

func New(store Store) *Engine {
	return NewWithRateLimit(store, http.DefaultClient, 0)
}

func NewWithHTTPClient(store Store, httpClient *http.Client) *Engine {
	return NewWithRateLimit(store, httpClient, 0)
}

func NewWithRateLimit(store Store, httpClient *http.Client, bytesPerSecond int64) *Engine {
	engine, _ := NewWithOptions(store, Options{
		HTTPClient:     httpClient,
		BytesPerSecond: bytesPerSecond,
	})

	return engine
}

func NewWithOptions(store Store, options Options) (*Engine, error) {
	if options.HTTPClient == nil {
		options.HTTPClient = http.DefaultClient
	}
	if options.MaximumChunks == 0 {
		options.MaximumChunks = DefaultMaximumChunks
	}
	if options.MinimumChunkSize == 0 {
		options.MinimumChunkSize = DefaultMinimumChunkSize
	}
	if options.BytesPerSecond < 0 {
		return nil, fmt.Errorf("download rate limit must not be negative")
	}
	planner, err := NewChunkPlanner(options.MaximumChunks, options.MinimumChunkSize)
	if err != nil {
		return nil, err
	}
	parts := options.PartsDirectory
	if parts == "" {
		parts, err = DefaultPartsDirectory()
		if err != nil {
			return nil, err
		}
	}
	if !filepath.IsAbs(parts) {
		return nil, fmt.Errorf("partial directory must be absolute")
	}

	engine := &Engine{
		httpClient: options.HTTPClient,
		store:      store,
		now:        func() time.Time { return time.Now().UTC() },
		planner:    planner,
		chunkCount: options.MaximumChunks,
		observer:   options.ChunkProgress,
		parts:      filepath.Clean(parts),
	}
	engine.rateLimit.Store(options.BytesPerSecond)

	return engine, nil
}

func (engine *Engine) SetRateLimit(bytesPerSecond int64) error {
	if bytesPerSecond < 0 {
		return fmt.Errorf("download rate limit must not be negative")
	}
	engine.rateLimit.Store(bytesPerSecond)

	return nil
}

func (engine *Engine) Download(ctx context.Context, download model.Download) error {
	resolving := false
	switch download.Status {
	case model.StatusQueued:
		if err := engine.store.UpdateDownloadStatus(
			ctx,
			download.ID,
			model.StatusResolving,
			engine.now(),
			"",
		); err != nil {
			return err
		}
		resolving = true
	case model.StatusDownloading:
	case model.StatusResolving,
		model.StatusPaused,
		model.StatusCompleted,
		model.StatusFailed,
		model.StatusCanceled:
		return fmt.Errorf("download %s cannot start from status %s", download.ID, download.Status)
	}
	metadata, err := NewInspector(engine.httpClient).Inspect(ctx, download.URL)
	if err != nil {
		return engine.fail(ctx, download.ID, err)
	}
	if err := engine.prepareResumeState(ctx, &download, metadata); err != nil {
		return engine.fail(ctx, download.ID, err)
	}
	if err := engine.store.UpdateRemoteMetadata(
		ctx,
		download.ID,
		metadata.TotalSize,
		metadata.RangeSupported,
		metadata.ETag,
		metadata.LastModified,
		engine.now(),
	); err != nil {
		return engine.fail(ctx, download.ID, err)
	}
	if metadata.RangeSupported && metadata.TotalSize > 0 {
		chunks, err := engine.planner.Plan(metadata.TotalSize, engine.chunkCount)
		if err != nil {
			return engine.fail(ctx, download.ID, err)
		}
		if len(chunks) > 1 {
			return engine.downloadParallel(ctx, download, metadata, chunks, resolving)
		}
	}

	offset, finalPath, err := engine.preparePaths(download)
	if err != nil {
		return engine.fail(ctx, download.ID, err)
	}
	if offset != download.DownloadedBytes {
		if err := engine.store.UpdateDownloadProgress(ctx, download.ID, offset, engine.now()); err != nil {
			return engine.fail(ctx, download.ID, err)
		}
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, download.URL, nil)
	if err != nil {
		return engine.fail(ctx, download.ID, fmt.Errorf("create HTTP request: %w", err))
	}
	if offset > 0 {
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	response, err := engine.httpClient.Do(request)
	if err != nil {
		return engine.fail(ctx, download.ID, fmt.Errorf("perform HTTP request: %w", err))
	}
	defer func() {
		_ = response.Body.Close()
	}()

	responseOffset, totalSize, err := resolveResponse(response, offset)
	if err != nil {
		return engine.fail(ctx, download.ID, err)
	}
	if responseOffset != offset {
		offset = responseOffset
		if err := engine.store.UpdateDownloadProgress(ctx, download.ID, offset, engine.now()); err != nil {
			return engine.fail(ctx, download.ID, err)
		}
	}

	partial, err := openPartial(engine.partialPath(download.ID), offset)
	if err != nil {
		return engine.fail(ctx, download.ID, err)
	}
	partialOpen := true
	defer func() {
		if partialOpen {
			_ = partial.Close()
		}
	}()

	etag := response.Header.Get("ETag")
	if etag == "" {
		etag = metadata.ETag
	}
	lastModified := response.Header.Get("Last-Modified")
	if lastModified == "" {
		lastModified = metadata.LastModified
	}
	if err := engine.store.UpdateRemoteMetadata(
		ctx,
		download.ID,
		totalSize,
		metadata.RangeSupported || response.StatusCode == http.StatusPartialContent,
		etag,
		lastModified,
		engine.now(),
	); err != nil {
		return engine.fail(ctx, download.ID, err)
	}
	if resolving {
		if err := engine.store.UpdateDownloadStatus(
			ctx,
			download.ID,
			model.StatusDownloading,
			engine.now(),
			"",
		); err != nil {
			return engine.fail(ctx, download.ID, err)
		}
	}

	downloaded, err := engine.copy(
		ctx,
		download.ID,
		partial,
		response.Body,
		offset,
		newRateLimiter(engine.rateLimit.Load),
	)
	if err != nil {
		return engine.fail(ctx, download.ID, err)
	}
	if response.ContentLength >= 0 && downloaded-offset != response.ContentLength {
		return engine.fail(ctx, download.ID, io.ErrUnexpectedEOF)
	}
	if totalSize >= 0 && downloaded != totalSize {
		return engine.fail(ctx, download.ID, io.ErrUnexpectedEOF)
	}
	if err := partial.Sync(); err != nil {
		return engine.fail(ctx, download.ID, fmt.Errorf("sync partial file: %w", err))
	}
	if err := partial.Close(); err != nil {
		partialOpen = false
		return engine.fail(ctx, download.ID, fmt.Errorf("close partial file: %w", err))
	}
	partialOpen = false
	finalPath, err = engine.finalize(download, finalPath)
	if err != nil {
		return engine.fail(ctx, download.ID, err)
	}
	filename := filepath.Base(finalPath)
	if filename != download.Filename {
		if err := engine.store.UpdateDownloadFilename(ctx, download.ID, filename, engine.now()); err != nil {
			return engine.fail(ctx, download.ID, err)
		}
	}
	if err := engine.store.UpdateDownloadStatus(
		ctx,
		download.ID,
		model.StatusCompleted,
		engine.now(),
		"",
	); err != nil {
		return err
	}

	return nil
}

func (engine *Engine) preparePaths(download model.Download) (int64, string, error) {
	if err := os.MkdirAll(download.Destination, 0o755); err != nil {
		return 0, "", fmt.Errorf("create destination directory: %w", err)
	}
	finalPath := filepath.Join(download.Destination, download.Filename)

	if err := engine.preparePartial(download); err != nil {
		return 0, "", err
	}
	partial := engine.partialPath(download.ID)
	info, err := os.Stat(partial)
	if errors.Is(err, os.ErrNotExist) {
		return 0, finalPath, nil
	}
	if err != nil {
		return 0, "", fmt.Errorf("inspect partial file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return 0, "", fmt.Errorf("partial path is not a regular file: %s", partial)
	}
	if download.DownloadedBytes <= 0 || info.Size() < download.DownloadedBytes {
		return 0, finalPath, nil
	}
	if info.Size() > download.DownloadedBytes {
		if err := os.Truncate(partial, download.DownloadedBytes); err != nil {
			return 0, "", fmt.Errorf("truncate partial file: %w", err)
		}
	}

	return download.DownloadedBytes, finalPath, nil
}

func openPartial(path string, offset int64) (*os.File, error) {
	flags := os.O_CREATE | os.O_WRONLY
	if offset == 0 {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open partial file: %w", err)
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		closeErr := file.Close()
		return nil, errors.Join(fmt.Errorf("seek partial file: %w", err), closeErr)
	}

	return file, nil
}

func resolveResponse(response *http.Response, requestedOffset int64) (int64, int64, error) {
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return 0, 0, HTTPStatusError{StatusCode: response.StatusCode, Status: response.Status}
	}
	if response.StatusCode == http.StatusPartialContent {
		start, _, total, err := parseContentRange(response.Header.Get("Content-Range"))
		if err != nil {
			return 0, 0, err
		}
		if start != requestedOffset {
			return 0, 0, fmt.Errorf("range response starts at %d instead of %d", start, requestedOffset)
		}
		return requestedOffset, total, nil
	}
	if requestedOffset > 0 {
		return 0, response.ContentLength, nil
	}

	return 0, response.ContentLength, nil
}

func parseContentRange(value string) (int64, int64, int64, error) {
	if !strings.HasPrefix(value, "bytes ") {
		return 0, 0, 0, fmt.Errorf("invalid Content-Range %q", value)
	}
	parts := strings.Split(strings.TrimPrefix(value, "bytes "), "/")
	if len(parts) != 2 {
		return 0, 0, 0, fmt.Errorf("invalid Content-Range %q", value)
	}
	bounds := strings.Split(parts[0], "-")
	if len(bounds) != 2 {
		return 0, 0, 0, fmt.Errorf("invalid Content-Range %q", value)
	}
	start, err := strconv.ParseInt(bounds[0], 10, 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid Content-Range start %q: %w", bounds[0], err)
	}
	end, err := strconv.ParseInt(bounds[1], 10, 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid Content-Range end %q: %w", bounds[1], err)
	}
	total := int64(-1)
	if parts[1] != "*" {
		total, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("invalid Content-Range total %q: %w", parts[1], err)
		}
	}
	if start < 0 || end < start || (total >= 0 && end >= total) {
		return 0, 0, 0, fmt.Errorf("invalid Content-Range bounds %q", value)
	}

	return start, end, total, nil
}

func (engine *Engine) copy(
	ctx context.Context,
	id model.DownloadID,
	destination io.Writer,
	source io.Reader,
	downloaded int64,
	limiter *rateLimiter,
) (int64, error) {
	buffer := make([]byte, copyBufferSize)
	for {
		if err := ctx.Err(); err != nil {
			return downloaded, err
		}
		read, readError := source.Read(buffer)
		if read > 0 {
			if err := limiter.Wait(ctx, read); err != nil {
				return downloaded, err
			}
			written, writeError := destination.Write(buffer[:read])
			downloaded += int64(written)
			if writeError != nil {
				return downloaded, fmt.Errorf("write partial file: %w", writeError)
			}
			if written != read {
				return downloaded, io.ErrShortWrite
			}
			if err := engine.store.UpdateDownloadProgress(ctx, id, downloaded, engine.now()); err != nil {
				return downloaded, err
			}
		}
		if errors.Is(readError, io.EOF) {
			return downloaded, nil
		}
		if readError != nil {
			return downloaded, fmt.Errorf("read HTTP response: %w", readError)
		}
	}
}

func (engine *Engine) fail(ctx context.Context, id model.DownloadID, downloadError error) error {
	if ctx.Err() != nil {
		return downloadError
	}
	statusContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := engine.store.UpdateDownloadStatus(
		statusContext,
		id,
		model.StatusFailed,
		engine.now(),
		downloadError.Error(),
	); err != nil {
		return errors.Join(downloadError, fmt.Errorf("persist download failure: %w", err))
	}

	return downloadError
}

package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/kristyancarvalho/argo/internal/model"
)

type ChunkProgress struct {
	Index      int
	Start      int64
	End        int64
	Downloaded int64
}

type progressTracker struct {
	mutex      sync.Mutex
	total      int64
	byChunk    []int64
	downloadID model.DownloadID
	engine     *Engine
}

func (engine *Engine) downloadParallel(
	ctx context.Context,
	download model.Download,
	metadata RemoteMetadata,
	chunks []Chunk,
	resolving bool,
) error {
	partial, finalPath, err := prepareParallelFile(download, metadata.TotalSize)
	if err != nil {
		return engine.fail(ctx, download.ID, err)
	}
	partialOpen := true
	defer func() {
		if partialOpen {
			_ = partial.Close()
		}
	}()
	if download.DownloadedBytes != 0 {
		if err := engine.store.UpdateDownloadProgress(ctx, download.ID, 0, engine.now()); err != nil {
			return engine.fail(ctx, download.ID, err)
		}
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

	workerContext, cancelWorkers := context.WithCancel(ctx)
	defer cancelWorkers()
	results := make(chan error, len(chunks))
	limiter := newRateLimiter(engine.rateLimit)
	tracker := &progressTracker{
		byChunk:    make([]int64, len(chunks)),
		downloadID: download.ID,
		engine:     engine,
	}
	for _, chunk := range chunks {
		go func(chunk Chunk) {
			results <- engine.downloadChunk(
				workerContext,
				download,
				metadata.TotalSize,
				partial,
				chunk,
				limiter,
				tracker,
			)
		}(chunk)
	}

	workerErrors := make([]error, 0)
	for range chunks {
		workerError := <-results
		if workerError != nil {
			workerErrors = append(workerErrors, workerError)
			cancelWorkers()
		}
	}
	if len(workerErrors) > 0 {
		return engine.fail(ctx, download.ID, errors.Join(workerErrors...))
	}
	if tracker.Total() != metadata.TotalSize {
		return engine.fail(ctx, download.ID, io.ErrUnexpectedEOF)
	}
	if err := partial.Sync(); err != nil {
		return engine.fail(ctx, download.ID, fmt.Errorf("sync parallel partial file: %w", err))
	}
	if err := partial.Close(); err != nil {
		partialOpen = false
		return engine.fail(ctx, download.ID, fmt.Errorf("close parallel partial file: %w", err))
	}
	partialOpen = false
	if err := os.Rename(partialPath(download), finalPath); err != nil {
		return engine.fail(ctx, download.ID, fmt.Errorf("finalize parallel download: %w", err))
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

func (engine *Engine) downloadChunk(
	ctx context.Context,
	download model.Download,
	totalSize int64,
	destination *os.File,
	chunk Chunk,
	limiter *rateLimiter,
	tracker *progressTracker,
) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, download.URL, nil)
	if err != nil {
		return fmt.Errorf("create request for chunk %d: %w", chunk.Index, err)
	}
	request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", chunk.Start, chunk.End))
	response, err := engine.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("request chunk %d: %w", chunk.Index, err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode != http.StatusPartialContent {
		return HTTPStatusError{StatusCode: response.StatusCode, Status: response.Status}
	}
	contentRange := response.Header.Get("Content-Range")
	start, end, responseTotal, err := parseContentRange(contentRange)
	if err != nil || start != chunk.Start || end != chunk.End || responseTotal != totalSize {
		return RangeMismatchError{Chunk: chunk, ContentRange: contentRange}
	}

	buffer := make([]byte, copyBufferSize)
	var downloaded int64
	for {
		remaining := chunk.Size() - downloaded
		readLimit := int64(len(buffer))
		if remaining < readLimit {
			readLimit = remaining + 1
		}
		read, readError := response.Body.Read(buffer[:readLimit])
		if int64(read) > remaining {
			return RangeMismatchError{Chunk: chunk, ContentRange: contentRange}
		}
		if read > 0 {
			if err := limiter.Wait(ctx, read); err != nil {
				return err
			}
			written, writeError := destination.WriteAt(buffer[:read], chunk.Start+downloaded)
			if writeError != nil {
				return fmt.Errorf("write chunk %d: %w", chunk.Index, writeError)
			}
			if written != read {
				return io.ErrShortWrite
			}
			downloaded += int64(written)
			if err := tracker.Add(ctx, chunk, int64(written)); err != nil {
				return err
			}
		}
		if errors.Is(readError, io.EOF) {
			if downloaded != chunk.Size() {
				return io.ErrUnexpectedEOF
			}
			return nil
		}
		if readError != nil {
			return fmt.Errorf("read chunk %d: %w", chunk.Index, readError)
		}
	}
}

func (tracker *progressTracker) Add(ctx context.Context, chunk Chunk, byteCount int64) error {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	tracker.total += byteCount
	tracker.byChunk[chunk.Index] += byteCount
	if err := tracker.engine.store.UpdateDownloadProgress(
		ctx,
		tracker.downloadID,
		tracker.total,
		tracker.engine.now(),
	); err != nil {
		return err
	}
	if tracker.engine.observer != nil {
		tracker.engine.observer(tracker.downloadID, ChunkProgress{
			Index:      chunk.Index,
			Start:      chunk.Start,
			End:        chunk.End,
			Downloaded: tracker.byChunk[chunk.Index],
		})
	}

	return nil
}

func (tracker *progressTracker) Total() int64 {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()

	return tracker.total
}

func prepareParallelFile(download model.Download, totalSize int64) (*os.File, string, error) {
	if err := os.MkdirAll(download.Destination, 0o755); err != nil {
		return nil, "", fmt.Errorf("create destination directory: %w", err)
	}
	finalPath := filepath.Join(download.Destination, download.Filename)
	if _, err := os.Stat(finalPath); err == nil {
		return nil, "", DestinationExistsError{Path: finalPath}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, "", fmt.Errorf("inspect destination file: %w", err)
	}
	partial, err := os.OpenFile(partialPath(download), os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, "", fmt.Errorf("create parallel partial file: %w", err)
	}
	if err := partial.Truncate(totalSize); err != nil {
		closeError := partial.Close()
		return nil, "", errors.Join(fmt.Errorf("size parallel partial file: %w", err), closeError)
	}

	return partial, finalPath, nil
}

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
	partial, finalPath, originalSize, err := engine.prepareParallelFile(download, metadata.TotalSize)
	if err != nil {
		return engine.fail(ctx, download.ID, err)
	}
	partialOpen := true
	defer func() {
		if partialOpen {
			_ = partial.Close()
		}
	}()

	states, err := engine.store.DownloadChunks(ctx, download.ID)
	if err != nil {
		return engine.fail(ctx, download.ID, err)
	}
	if !chunkStatesMatch(states, chunks) {
		states = plannedChunkStates(download.ID, chunks)
	}
	adjustChunkStatesForSize(states, originalSize)
	if err := engine.store.ReplaceDownloadChunks(ctx, download.ID, states, engine.now()); err != nil {
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

	workerContext, cancelWorkers := context.WithCancel(ctx)
	defer cancelWorkers()
	pending := 0
	for _, state := range states {
		if state.DownloadedBytes < state.Size() {
			pending++
		}
	}
	results := make(chan error, pending)
	limiter := newRateLimiter(engine.rateLimit.Load)
	tracker := newProgressTracker(engine, download.ID, states)
	for _, chunk := range chunks {
		downloaded := states[chunk.Index].DownloadedBytes
		if downloaded == chunk.Size() {
			continue
		}
		go func(chunk Chunk, downloaded int64) {
			results <- engine.downloadChunk(
				workerContext,
				download,
				metadata.TotalSize,
				partial,
				chunk,
				downloaded,
				limiter,
				tracker,
			)
		}(chunk, downloaded)
	}

	workerErrors := make([]error, 0)
	for range pending {
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

func (engine *Engine) downloadChunk(
	ctx context.Context,
	download model.Download,
	totalSize int64,
	destination *os.File,
	chunk Chunk,
	downloaded int64,
	limiter *rateLimiter,
	tracker *progressTracker,
) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, download.URL, nil)
	if err != nil {
		return fmt.Errorf("create request for chunk %d: %w", chunk.Index, err)
	}
	requestStart := chunk.Start + downloaded
	request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", requestStart, chunk.End))
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
	if err != nil || start != requestStart || end != chunk.End || responseTotal != totalSize {
		return RangeMismatchError{Chunk: chunk, ContentRange: contentRange}
	}

	buffer := make([]byte, copyBufferSize)
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

func newProgressTracker(engine *Engine, downloadID model.DownloadID, chunks []model.DownloadChunk) *progressTracker {
	tracker := &progressTracker{
		byChunk:    make([]int64, len(chunks)),
		downloadID: downloadID,
		engine:     engine,
	}
	for _, chunk := range chunks {
		tracker.byChunk[chunk.Index] = chunk.DownloadedBytes
		tracker.total += chunk.DownloadedBytes
	}

	return tracker
}

func (tracker *progressTracker) Add(ctx context.Context, chunk Chunk, byteCount int64) error {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	tracker.total += byteCount
	tracker.byChunk[chunk.Index] += byteCount
	if err := tracker.engine.store.UpdateChunkProgress(
		ctx,
		tracker.downloadID,
		chunk.Index,
		tracker.byChunk[chunk.Index],
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

func plannedChunkStates(downloadID model.DownloadID, chunks []Chunk) []model.DownloadChunk {
	states := make([]model.DownloadChunk, len(chunks))
	for _, chunk := range chunks {
		states[chunk.Index] = model.DownloadChunk{
			DownloadID: downloadID,
			Index:      chunk.Index,
			Start:      chunk.Start,
			End:        chunk.End,
		}
	}

	return states
}

func chunkStatesMatch(states []model.DownloadChunk, chunks []Chunk) bool {
	if len(states) != len(chunks) {
		return false
	}
	for _, chunk := range chunks {
		state := states[chunk.Index]
		if state.Index != chunk.Index || state.Start != chunk.Start || state.End != chunk.End ||
			state.DownloadedBytes < 0 || state.DownloadedBytes > chunk.Size() {
			return false
		}
	}

	return true
}

func adjustChunkStatesForSize(states []model.DownloadChunk, fileSize int64) {
	for index := range states {
		available := fileSize - states[index].Start
		if available < 0 {
			available = 0
		}
		if available > states[index].Size() {
			available = states[index].Size()
		}
		if states[index].DownloadedBytes > available {
			states[index].DownloadedBytes = available
		}
	}
}

func (engine *Engine) prepareParallelFile(download model.Download, totalSize int64) (*os.File, string, int64, error) {
	if err := os.MkdirAll(download.Destination, 0o755); err != nil {
		return nil, "", 0, fmt.Errorf("create destination directory: %w", err)
	}
	finalPath := filepath.Join(download.Destination, download.Filename)
	if err := engine.preparePartial(download); err != nil {
		return nil, "", 0, err
	}
	partial, err := os.OpenFile(engine.partialPath(download.ID), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, "", 0, fmt.Errorf("create parallel partial file: %w", err)
	}
	info, err := partial.Stat()
	if err != nil {
		closeError := partial.Close()
		return nil, "", 0, errors.Join(fmt.Errorf("inspect parallel partial file: %w", err), closeError)
	}
	originalSize := info.Size()
	if err := partial.Truncate(totalSize); err != nil {
		closeError := partial.Close()
		return nil, "", 0, errors.Join(fmt.Errorf("size parallel partial file: %w", err), closeError)
	}

	return partial, finalPath, originalSize, nil
}

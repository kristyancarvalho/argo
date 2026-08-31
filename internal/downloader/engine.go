package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/kristyancarvalho/argo/internal/model"
)

const copyBufferSize = 32 * 1024

type Store interface {
	UpdateDownloadDetails(context.Context, model.DownloadID, string, int64, time.Time) error
	UpdateDownloadProgress(context.Context, model.DownloadID, int64, time.Time) error
	UpdateDownloadStatus(context.Context, model.DownloadID, model.Status, time.Time, string) error
}

type Engine struct {
	httpClient *http.Client
	store      Store
	now        func() time.Time
}

func New(store Store) *Engine {
	return NewWithHTTPClient(store, http.DefaultClient)
}

func NewWithHTTPClient(store Store, httpClient *http.Client) *Engine {
	return &Engine{
		httpClient: httpClient,
		store:      store,
		now:        func() time.Time { return time.Now().UTC() },
	}
}

func (engine *Engine) Download(ctx context.Context, download model.Download) error {
	if err := engine.store.UpdateDownloadStatus(
		ctx,
		download.ID,
		model.StatusResolving,
		engine.now(),
		"",
	); err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, download.URL, nil)
	if err != nil {
		return engine.fail(ctx, download.ID, fmt.Errorf("create HTTP request: %w", err))
	}
	response, err := engine.httpClient.Do(request)
	if err != nil {
		return engine.fail(ctx, download.ID, fmt.Errorf("perform HTTP request: %w", err))
	}
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return engine.fail(ctx, download.ID, HTTPStatusError{
			StatusCode: response.StatusCode,
			Status:     response.Status,
		})
	}

	if err := os.MkdirAll(download.Destination, 0o755); err != nil {
		return engine.fail(ctx, download.ID, fmt.Errorf("create destination directory: %w", err))
	}
	finalPath := filepath.Join(download.Destination, download.Filename)
	if _, err := os.Stat(finalPath); err == nil {
		return engine.fail(ctx, download.ID, DestinationExistsError{Path: finalPath})
	} else if !errors.Is(err, os.ErrNotExist) {
		return engine.fail(ctx, download.ID, fmt.Errorf("inspect destination file: %w", err))
	}
	partialPath := partialPath(download)
	partial, err := os.OpenFile(partialPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return engine.fail(ctx, download.ID, fmt.Errorf("create partial file: %w", err))
	}
	partialOpen := true
	defer func() {
		if partialOpen {
			_ = partial.Close()
		}
	}()

	if err := engine.store.UpdateDownloadDetails(
		ctx,
		download.ID,
		download.Filename,
		response.ContentLength,
		engine.now(),
	); err != nil {
		return engine.fail(ctx, download.ID, err)
	}
	if err := engine.store.UpdateDownloadStatus(
		ctx,
		download.ID,
		model.StatusDownloading,
		engine.now(),
		"",
	); err != nil {
		return engine.fail(ctx, download.ID, err)
	}

	downloaded, err := engine.copy(ctx, download.ID, partial, response.Body)
	if err != nil {
		return engine.fail(ctx, download.ID, err)
	}
	if response.ContentLength >= 0 && downloaded != response.ContentLength {
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
	if err := os.Rename(partialPath, finalPath); err != nil {
		return engine.fail(ctx, download.ID, fmt.Errorf("finalize download: %w", err))
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

func (engine *Engine) copy(
	ctx context.Context,
	id model.DownloadID,
	destination io.Writer,
	source io.Reader,
) (int64, error) {
	buffer := make([]byte, copyBufferSize)
	var downloaded int64
	for {
		if err := ctx.Err(); err != nil {
			return downloaded, err
		}
		read, readError := source.Read(buffer)
		if read > 0 {
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
	statusContext := context.WithoutCancel(ctx)
	statusContext, cancel := context.WithTimeout(statusContext, 5*time.Second)
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

func partialPath(download model.Download) string {
	return filepath.Join(download.Destination, ".argo-"+download.ID.String()+".part")
}

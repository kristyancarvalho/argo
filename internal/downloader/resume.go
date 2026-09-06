package downloader

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/kristyancarvalho/argo/internal/model"
	"golang.org/x/sys/unix"
)

const canceledResumeValidationTimeout = 5 * time.Second

func (engine *Engine) prepareResumeState(
	ctx context.Context,
	download *model.Download,
	metadata RemoteMetadata,
) error {
	_, strict := engine.strict.LoadAndDelete(download.ID)
	chunks, err := engine.store.DownloadChunks(ctx, download.ID)
	if err != nil {
		return err
	}
	if download.DownloadedBytes == 0 {
		return nil
	}
	valid := validatorsMatch(*download, metadata)
	if len(chunks) > 0 && !metadata.RangeSupported {
		valid = false
	}
	if valid {
		return nil
	}
	if strict {
		return ResumeUnavailableError{ID: download.ID.String(), Reason: "remote validators changed"}
	}
	if err := engine.store.ResetDownloadProgress(ctx, download.ID, engine.now()); err != nil {
		return err
	}
	if err := os.Remove(engine.partialPath(download.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale partial file: %w", err)
	}
	download.DownloadedBytes = 0

	return nil
}

func (engine *Engine) ValidateCanceledResume(ctx context.Context, download model.Download) error {
	validationContext, cancel := context.WithTimeout(ctx, canceledResumeValidationTimeout)
	defer cancel()
	if download.Status != model.StatusCanceled {
		return ResumeUnavailableError{ID: download.ID.String(), Reason: "download is not canceled"}
	}
	if download.DownloadedBytes <= 0 {
		return ResumeUnavailableError{ID: download.ID.String(), Reason: "no reusable partial data exists"}
	}
	if err := engine.preparePartial(download); err != nil {
		return ResumeUnavailableError{ID: download.ID.String(), Reason: err.Error()}
	}
	partial, err := engine.openPartialFile(download.ID, unix.O_RDONLY)
	if errors.Is(err, unix.ENOENT) {
		return ResumeUnavailableError{ID: download.ID.String(), Reason: "partial data is missing"}
	}
	if err != nil {
		return ResumeUnavailableError{ID: download.ID.String(), Reason: err.Error()}
	}
	defer func() {
		_ = partial.Close()
	}()
	info, err := partial.Stat()
	if err != nil {
		return ResumeUnavailableError{ID: download.ID.String(), Reason: err.Error()}
	}
	if info.Size() < download.DownloadedBytes {
		return ResumeUnavailableError{ID: download.ID.String(), Reason: "partial data is incomplete or invalid"}
	}
	metadata, err := NewInspector(engine.httpClient).Inspect(validationContext, download.URL)
	if err != nil {
		return ResumeUnavailableError{ID: download.ID.String(), Reason: err.Error()}
	}
	if !metadata.RangeSupported {
		return ResumeUnavailableError{ID: download.ID.String(), Reason: "remote server no longer supports byte ranges"}
	}
	if !validatorsMatch(download, metadata) {
		return ResumeUnavailableError{ID: download.ID.String(), Reason: "remote validators changed"}
	}
	engine.strict.Store(download.ID, struct{}{})

	return nil
}

func validatorsMatch(download model.Download, metadata RemoteMetadata) bool {
	if download.TotalSize >= 0 && metadata.TotalSize >= 0 && download.TotalSize != metadata.TotalSize {
		return false
	}
	validated := false
	if download.ETag != "" {
		if metadata.ETag != download.ETag {
			return false
		}
		validated = true
	}
	if download.LastModified != "" {
		if metadata.LastModified != download.LastModified {
			return false
		}
		validated = true
	}

	return validated
}

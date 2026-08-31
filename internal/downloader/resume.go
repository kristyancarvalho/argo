package downloader

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/kristyancarvalho/argo/internal/model"
)

func (engine *Engine) prepareResumeState(
	ctx context.Context,
	download *model.Download,
	metadata RemoteMetadata,
) error {
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
	if err := engine.store.ResetDownloadProgress(ctx, download.ID, engine.now()); err != nil {
		return err
	}
	if err := os.Remove(partialPath(*download)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale partial file: %w", err)
	}
	download.DownloadedBytes = 0

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

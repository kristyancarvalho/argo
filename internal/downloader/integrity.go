package downloader

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/kristyancarvalho/argo/internal/model"
	"golang.org/x/sys/unix"
)

type ChecksumMismatchError struct {
	Expected string
	Actual   string
}

func (err ChecksumMismatchError) Error() string {
	return fmt.Sprintf("checksum mismatch: expected %s, got %s", err.Expected, err.Actual)
}

func (engine *Engine) Verify(ctx context.Context, download model.Download) (string, error) {
	if download.Checksum == "" {
		return "", fmt.Errorf("download %s has no checksum metadata", download.ID)
	}
	if download.Status != model.StatusCompleted {
		return "", fmt.Errorf("download %s cannot be verified from status %s", download.ID, download.Status)
	}
	if download.Filename == "" || filepath.Base(download.Filename) != download.Filename {
		return "", fmt.Errorf("download %s has an unsafe filename", download.ID)
	}

	return verifyPath(ctx, filepath.Join(download.Destination, download.Filename), download.Checksum)
}

func verifyPath(ctx context.Context, path string, checksum string) (string, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", fmt.Errorf("open download for verification: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect download for verification: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("download is not a regular file")
	}

	return verifyChecksum(ctx, file, info.Size(), checksum)
}

func (engine *Engine) verifyPartial(ctx context.Context, download model.Download, partial *os.File) error {
	if download.Checksum == "" {
		return nil
	}
	if err := engine.store.UpdateDownloadStatus(ctx, download.ID, model.StatusVerifying, engine.now(), ""); err != nil {
		return err
	}
	info, err := partial.Stat()
	if err != nil {
		return fmt.Errorf("inspect partial file for verification: %w", err)
	}
	_, err = verifyChecksum(ctx, partial, info.Size(), download.Checksum)

	return err
}

func verifyChecksum(ctx context.Context, source io.ReaderAt, size int64, expected string) (string, error) {
	normalized, err := model.NormalizeChecksum(expected)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	reader := io.NewSectionReader(source, 0, size)
	buffer := make([]byte, copyBufferSize)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		read, readErr := reader.Read(buffer)
		if read > 0 {
			if _, err := hash.Write(buffer[:read]); err != nil {
				return "", fmt.Errorf("hash download: %w", err)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", fmt.Errorf("read download for verification: %w", readErr)
		}
	}
	actualDigest := hash.Sum(nil)
	actual := "sha256:" + hex.EncodeToString(actualDigest)
	expectedDigest, _ := hex.DecodeString(normalized[len("sha256:"):])
	if subtle.ConstantTimeCompare(expectedDigest, actualDigest) != 1 {
		return actual, ChecksumMismatchError{Expected: normalized, Actual: actual}
	}

	return actual, nil
}

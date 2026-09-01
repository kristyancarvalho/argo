package downloader

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/kristyancarvalho/argo/internal/model"
	"golang.org/x/sys/unix"
)

func DefaultPartsDirectory() (string, error) {
	if state := os.Getenv("XDG_STATE_HOME"); state != "" {
		if !filepath.IsAbs(state) {
			return "", fmt.Errorf("XDG_STATE_HOME must be absolute")
		}

		return filepath.Join(state, "argo", "parts"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory for partial storage: %w", err)
	}

	return filepath.Join(home, ".local", "state", "argo", "parts"), nil
}

func (engine *Engine) partialPath(identifier model.DownloadID) string {
	return filepath.Join(engine.parts, identifier.String()+".part")
}

func (engine *Engine) preparePartial(download model.Download) error {
	if err := os.MkdirAll(engine.parts, 0o700); err != nil {
		return fmt.Errorf("create partial directory: %w", err)
	}
	current := engine.partialPath(download.ID)
	if _, err := os.Stat(current); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect partial file: %w", err)
	}
	if !filepath.IsAbs(download.Destination) {
		return nil
	}
	legacy := filepath.Join(download.Destination, ".argo-"+download.ID.String()+".part")
	info, err := os.Lstat(legacy)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect legacy partial file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	if err := moveNoReplace(legacy, current); err != nil {
		return fmt.Errorf("migrate legacy partial file: %w", err)
	}

	return nil
}

func (engine *Engine) finalize(download model.Download, initial string) (string, error) {
	for index := 0; ; index++ {
		candidate := collisionPath(initial, index)
		err := moveNoReplace(engine.partialPath(download.ID), candidate)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("finalize download: %w", err)
		}

		return candidate, nil
	}
}

func collisionPath(path string, index int) string {
	if index == 0 {
		return path
	}
	extension := filepath.Ext(path)
	base := path[:len(path)-len(extension)]

	return fmt.Sprintf("%s (%d)%s", base, index, extension)
}

func moveNoReplace(source, destination string) error {
	err := unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE)
	if err == nil {
		return nil
	}
	if errors.Is(err, unix.EXDEV) {
		return copyNoReplace(source, destination)
	}
	if !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EINVAL) {
		return err
	}
	if err := os.Link(source, destination); err != nil {
		if errors.Is(err, unix.EXDEV) {
			return copyNoReplace(source, destination)
		}

		return err
	}

	return os.Remove(source)
}

func copyNoReplace(source, destination string) (result error) {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, input.Close())
	}()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			result = errors.Join(result, output.Close())
			if err := os.Remove(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
				result = errors.Join(result, err)
			}
		}
	}()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	complete = true

	return os.Remove(source)
}

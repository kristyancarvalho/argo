package downloader

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

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

func partialName(identifier model.DownloadID) string {
	return identifier.String() + ".part"
}

func (engine *Engine) RemovePartial(identifier model.DownloadID) error {
	if _, err := model.ParseDownloadID(identifier.String()); err != nil {
		return err
	}
	directory, err := secureDirectory(engine.parts)
	if err != nil {
		return err
	}
	defer func() {
		_ = directory.Close()
	}()
	if err := unix.Unlinkat(int(directory.Fd()), partialName(identifier), 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("remove partial file for download %s: %w", identifier, err)
	}

	return nil
}

func (engine *Engine) preparePartial(download model.Download) error {
	directory, err := secureDirectory(engine.parts)
	if err != nil {
		return err
	}
	defer func() {
		_ = directory.Close()
	}()
	var current unix.Stat_t
	err = unix.Fstatat(int(directory.Fd()), partialName(download.ID), &current, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		if err := validatePartialStat(&current); err != nil {
			return fmt.Errorf("inspect partial file: %w", err)
		}
		return nil
	} else if !errors.Is(err, unix.ENOENT) {
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
	legacyFile, err := os.OpenFile(legacy, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open legacy partial file: %w", err)
	}
	defer func() {
		_ = legacyFile.Close()
	}()
	if err := validatePartialFile(legacyFile); err != nil {
		return fmt.Errorf("validate legacy partial file: %w", err)
	}
	openedInfo, err := legacyFile.Stat()
	if err != nil {
		return fmt.Errorf("inspect legacy partial descriptor: %w", err)
	}
	if !os.SameFile(info, openedInfo) {
		return fmt.Errorf("legacy partial path changed during migration")
	}
	if err := publishPartial(directory, partialName(download.ID), legacyFile); err != nil {
		return fmt.Errorf("migrate legacy partial file: %w", err)
	}
	if err := removeIfSame(legacy, legacyFile); err != nil {
		return fmt.Errorf("remove migrated legacy partial file: %w", err)
	}

	return nil
}

func (engine *Engine) finalize(download model.Download, initial string, partial *os.File) (string, error) {
	directory, err := engine.validatePartialBinding(download.ID, partial)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = directory.Close()
	}()
	for index := 0; ; index++ {
		candidate := collisionPath(initial, index)
		err := linkOrCopyNoReplace(partial, candidate)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("finalize download: %w", err)
		}

		if err := unlinkIfSame(directory, partialName(download.ID), partial); err != nil {
			return "", fmt.Errorf("remove finalized partial file: %w", err)
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

func linkOrCopyNoReplace(source *os.File, destination string) error {
	err := unix.Linkat(int(source.Fd()), "", unix.AT_FDCWD, destination, unix.AT_EMPTY_PATH)
	if err == nil {
		return nil
	}
	if errors.Is(err, unix.EXDEV) {
		return copyNoReplace(source, destination)
	}

	return err
}

func copyNoReplace(input *os.File, destination string) (result error) {
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind partial file: %w", err)
	}
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

	return nil
}

func secureDirectory(path string) (*os.File, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return nil, fmt.Errorf("partial directory must be absolute")
	}
	current, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open filesystem root: %w", err)
	}
	components := strings.Split(strings.TrimPrefix(clean, string(filepath.Separator)), string(filepath.Separator))
	for index, component := range components {
		if component == "" {
			continue
		}
		next, openError := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(openError, unix.ENOENT) {
			if mkdirError := unix.Mkdirat(current, component, 0o700); mkdirError != nil && !errors.Is(mkdirError, unix.EEXIST) {
				_ = unix.Close(current)
				return nil, fmt.Errorf("create partial directory component %s: %w", component, mkdirError)
			}
			next, openError = unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if openError != nil {
			_ = unix.Close(current)
			return nil, fmt.Errorf("open partial directory component %s without symlinks: %w", component, openError)
		}
		var stat unix.Stat_t
		if err := unix.Fstat(next, &stat); err != nil {
			_ = unix.Close(next)
			_ = unix.Close(current)
			return nil, fmt.Errorf("inspect partial directory component %s: %w", component, err)
		}
		final := index == len(components)-1
		if err := validateDirectoryStat(&stat, final); err != nil {
			_ = unix.Close(next)
			_ = unix.Close(current)
			return nil, fmt.Errorf("unsafe partial directory component %s: %w", component, err)
		}
		_ = unix.Close(current)
		current = next
	}

	return os.NewFile(uintptr(current), clean), nil
}

func validateDirectoryStat(stat *unix.Stat_t, final bool) error {
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("not a directory")
	}
	uid := uint32(os.Geteuid())
	if stat.Uid != 0 && stat.Uid != uid {
		return fmt.Errorf("owned by user ID %d", stat.Uid)
	}
	permissions := stat.Mode & 0o7777
	if final {
		if stat.Uid != uid {
			return fmt.Errorf("final directory is not owned by the current user")
		}
		if permissions&0o022 != 0 {
			return fmt.Errorf("final directory permissions %04o allow group or other writes", permissions)
		}
		return nil
	}
	if permissions&0o022 != 0 && (stat.Uid != 0 || permissions&unix.S_ISVTX == 0) {
		return fmt.Errorf("permissions %04o allow unsafe replacement", permissions)
	}

	return nil
}

func (engine *Engine) openPartialFile(identifier model.DownloadID, flags int) (*os.File, error) {
	directory, err := secureDirectory(engine.parts)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = directory.Close()
	}()
	fd, err := unix.Openat(int(directory.Fd()), partialName(identifier), flags|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open partial file: %w", err)
	}
	file := os.NewFile(uintptr(fd), engine.partialPath(identifier))
	if err := validatePartialFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}

	return file, nil
}

func validatePartialFile(file *os.File) error {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return fmt.Errorf("inspect partial descriptor: %w", err)
	}

	return validatePartialStat(&stat)
}

func validatePartialStat(stat *unix.Stat_t) error {
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("partial path is not a regular file")
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("partial file is owned by user ID %d", stat.Uid)
	}
	if stat.Mode&0o077 != 0 {
		return fmt.Errorf("partial file permissions allow group or other access")
	}
	if stat.Nlink != 1 {
		return fmt.Errorf("partial file has %d filesystem links", stat.Nlink)
	}

	return nil
}

func (engine *Engine) validatePartialBinding(identifier model.DownloadID, file *os.File) (*os.File, error) {
	if err := validatePartialFile(file); err != nil {
		return nil, err
	}
	directory, err := secureDirectory(engine.parts)
	if err != nil {
		return nil, err
	}
	var pathStat unix.Stat_t
	if err := unix.Fstatat(int(directory.Fd()), partialName(identifier), &pathStat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		_ = directory.Close()
		return nil, fmt.Errorf("inspect partial path before finalization: %w", err)
	}
	if err := validatePartialStat(&pathStat); err != nil {
		_ = directory.Close()
		return nil, err
	}
	var fileStat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &fileStat); err != nil {
		_ = directory.Close()
		return nil, fmt.Errorf("inspect partial descriptor before finalization: %w", err)
	}
	if pathStat.Dev != fileStat.Dev || pathStat.Ino != fileStat.Ino {
		_ = directory.Close()
		return nil, fmt.Errorf("partial path changed during transfer")
	}

	return directory, nil
}

func publishPartial(directory *os.File, name string, source *os.File) error {
	err := unix.Linkat(int(source.Fd()), "", int(directory.Fd()), name, unix.AT_EMPTY_PATH)
	if err == nil {
		return nil
	}
	if !errors.Is(err, unix.EXDEV) {
		return err
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return err
	}
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	destination := os.NewFile(uintptr(fd), name)
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		_ = unix.Unlinkat(int(directory.Fd()), name, 0)
		return err
	}
	if err := destination.Sync(); err != nil {
		_ = destination.Close()
		_ = unix.Unlinkat(int(directory.Fd()), name, 0)
		return err
	}

	return destination.Close()
}

func removeIfSame(path string, file *os.File) error {
	var pathStat unix.Stat_t
	if err := unix.Lstat(path, &pathStat); err != nil {
		return err
	}
	var fileStat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &fileStat); err != nil {
		return err
	}
	if pathStat.Dev != fileStat.Dev || pathStat.Ino != fileStat.Ino {
		return fmt.Errorf("path changed during operation")
	}

	return os.Remove(path)
}

func unlinkIfSame(directory *os.File, name string, file *os.File) error {
	var pathStat unix.Stat_t
	if err := unix.Fstatat(int(directory.Fd()), name, &pathStat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	var fileStat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &fileStat); err != nil {
		return err
	}
	if pathStat.Dev != fileStat.Dev || pathStat.Ino != fileStat.Ino {
		return fmt.Errorf("partial path changed during finalization")
	}

	return unix.Unlinkat(int(directory.Fd()), name, 0)
}

package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

type InstanceLock struct {
	file *os.File
}

type InstanceLockedError struct {
	Path string
}

func (err InstanceLockedError) Error() string {
	return fmt.Sprintf("database %s is already owned by an active Argo daemon", err.Path)
}

func AcquireInstanceLock(databasePath string) (*InstanceLock, error) {
	if databasePath == "" || databasePath == ":memory:" {
		return nil, fmt.Errorf("persistent database path is required for daemon locking")
	}
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		return nil, fmt.Errorf("create database lock directory: %w", err)
	}
	path := databasePath + ".lock"
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open daemon instance lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	closeWithError := func(lockError error) (*InstanceLock, error) {
		if closeError := file.Close(); closeError != nil {
			return nil, errors.Join(lockError, closeError)
		}
		return nil, lockError
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return closeWithError(fmt.Errorf("inspect daemon instance lock: %w", err))
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return closeWithError(fmt.Errorf("daemon instance lock must be a regular file owned by the current user"))
	}
	if err := file.Chmod(0o600); err != nil {
		return closeWithError(fmt.Errorf("secure daemon instance lock: %w", err))
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return closeWithError(InstanceLockedError{Path: databasePath})
		}
		return closeWithError(fmt.Errorf("acquire daemon instance lock: %w", err))
	}

	return &InstanceLock{file: file}, nil
}

func (lock *InstanceLock) Close() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	if err := lock.file.Close(); err != nil {
		return fmt.Errorf("close daemon instance lock: %w", err)
	}
	lock.file = nil

	return nil
}

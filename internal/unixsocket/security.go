package unixsocket

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func Prepare(path string) error {
	return walk(path, true)
}

func Validate(path string) error {
	return walk(path, false)
}

func walk(path string, create bool) error {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return fmt.Errorf("socket directory must be absolute")
	}
	if clean == string(filepath.Separator) {
		var stat unix.Stat_t
		if err := unix.Stat(clean, &stat); err != nil {
			return fmt.Errorf("inspect socket directory: %w", err)
		}
		return validateDirectory(&stat, true)
	}
	current, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open filesystem root: %w", err)
	}
	components := strings.Split(strings.TrimPrefix(clean, string(filepath.Separator)), string(filepath.Separator))
	for index, component := range components {
		if component == "" {
			continue
		}
		next, openErr := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(openErr, unix.ENOENT) && create {
			if mkdirErr := unix.Mkdirat(current, component, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				_ = unix.Close(current)
				return fmt.Errorf("create socket directory component %s: %w", component, mkdirErr)
			}
			next, openErr = unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if openErr != nil {
			_ = unix.Close(current)
			return fmt.Errorf("open socket directory component %s without symlinks: %w", component, openErr)
		}
		var stat unix.Stat_t
		if err := unix.Fstat(next, &stat); err != nil {
			_ = unix.Close(next)
			_ = unix.Close(current)
			return fmt.Errorf("inspect socket directory component %s: %w", component, err)
		}
		final := index == len(components)-1
		if err := validateDirectory(&stat, final); err != nil {
			_ = unix.Close(next)
			_ = unix.Close(current)
			return fmt.Errorf("unsafe socket directory component %s: %w", component, err)
		}
		_ = unix.Close(current)
		current = next
	}
	if err := unix.Close(current); err != nil {
		return fmt.Errorf("close socket directory: %w", err)
	}

	return nil
}

func validateDirectory(stat *unix.Stat_t, final bool) error {
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("not a directory")
	}
	uid := uint32(os.Geteuid())
	permissions := stat.Mode & 0o7777
	if final {
		if stat.Uid != uid {
			return fmt.Errorf("owned by user ID %d instead of %d", stat.Uid, uid)
		}
		if permissions&0o077 != 0 {
			return fmt.Errorf("permissions %04o allow group or other access", permissions)
		}
		return nil
	}
	if stat.Uid != 0 && stat.Uid != uid {
		return fmt.Errorf("owned by user ID %d", stat.Uid)
	}
	if permissions&0o022 != 0 && (stat.Uid != 0 || permissions&unix.S_ISVTX == 0) {
		return fmt.Errorf("permissions %04o allow unsafe replacement", permissions)
	}

	return nil
}

func ValidateSocket(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("path is not a Unix socket")
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("socket is owned by user ID %d", stat.Uid)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("socket permissions %04o allow group or other access", info.Mode().Perm())
	}

	return nil
}

func ValidatePeer(connection net.Conn, expectedUID uint32) error {
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("connection is not a Unix socket")
	}
	raw, err := unixConnection.SyscallConn()
	if err != nil {
		return fmt.Errorf("access peer socket: %w", err)
	}
	var credentials *syscall.Ucred
	var credentialError error
	if err := raw.Control(func(descriptor uintptr) {
		credentials, credentialError = syscall.GetsockoptUcred(int(descriptor), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return fmt.Errorf("inspect peer socket: %w", err)
	}
	if credentialError != nil {
		return fmt.Errorf("read peer credentials: %w", credentialError)
	}
	if credentials == nil {
		return fmt.Errorf("peer credentials are unavailable")
	}
	if credentials.Uid != expectedUID {
		return fmt.Errorf("peer UID %d does not match expected UID %d", credentials.Uid, expectedUID)
	}

	return nil
}

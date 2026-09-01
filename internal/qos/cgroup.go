package qos

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func CurrentCgroupID() (uint64, error) {
	membership, err := os.Open("/proc/self/cgroup")
	if err != nil {
		return 0, fmt.Errorf("open process cgroup membership: %w", err)
	}
	defer func() {
		_ = membership.Close()
	}()

	return ResolveCgroupID(membership, "/sys/fs/cgroup")
}

func ResolveCgroupID(membership io.Reader, root string) (uint64, error) {
	scanner := bufio.NewScanner(membership)
	for scanner.Scan() {
		fields := strings.SplitN(scanner.Text(), ":", 3)
		if len(fields) != 3 || fields[0] != "0" || fields[1] != "" {
			continue
		}
		path := filepath.Join(root, filepath.Clean("/"+fields[2]))
		info, err := os.Stat(path)
		if err != nil {
			return 0, fmt.Errorf("inspect process cgroup: %w", err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Ino == 0 {
			return 0, fmt.Errorf("process cgroup has no numeric identifier")
		}

		return stat.Ino, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("read process cgroup membership: %w", err)
	}

	return 0, fmt.Errorf("unified process cgroup membership was not found")
}

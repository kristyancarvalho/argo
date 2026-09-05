package qos

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type CgroupSelector struct {
	Path  string `json:"path"`
	Level uint32 `json:"level"`
}

func CurrentCgroup() (CgroupSelector, error) {
	membership, err := os.Open("/proc/self/cgroup")
	if err != nil {
		return CgroupSelector{}, fmt.Errorf("open process cgroup membership: %w", err)
	}
	defer func() {
		_ = membership.Close()
	}()

	return ResolveCgroup(membership, "/sys/fs/cgroup")
}

func ResolveCgroup(membership io.Reader, root string) (CgroupSelector, error) {
	scanner := bufio.NewScanner(membership)
	for scanner.Scan() {
		fields := strings.SplitN(scanner.Text(), ":", 3)
		if len(fields) != 3 || fields[0] != "0" || fields[1] != "" {
			continue
		}
		path := strings.TrimPrefix(filepath.Clean("/"+fields[2]), "/")
		if path == "." || path == "" {
			return CgroupSelector{}, fmt.Errorf("process belongs to the cgroup root")
		}
		info, err := os.Stat(filepath.Join(root, path))
		if err != nil {
			return CgroupSelector{}, fmt.Errorf("inspect process cgroup: %w", err)
		}
		if !info.IsDir() {
			return CgroupSelector{}, fmt.Errorf("process cgroup is not a directory")
		}
		selector := CgroupSelector{
			Path:  filepath.ToSlash(path),
			Level: uint32(len(strings.Split(path, string(filepath.Separator)))),
		}
		if err := selector.Validate(); err != nil {
			return CgroupSelector{}, err
		}

		return selector, nil
	}
	if err := scanner.Err(); err != nil {
		return CgroupSelector{}, fmt.Errorf("read process cgroup membership: %w", err)
	}

	return CgroupSelector{}, fmt.Errorf("unified process cgroup membership was not found")
}

func (selector CgroupSelector) Validate() error {
	if selector.Path == "" || selector.Level == 0 {
		return fmt.Errorf("argo cgroup path and level are required")
	}
	if strings.HasPrefix(selector.Path, "/") || filepath.Clean(selector.Path) != selector.Path {
		return fmt.Errorf("invalid Argo cgroup path %q", selector.Path)
	}
	parts := strings.Split(selector.Path, "/")
	if len(parts) != int(selector.Level) {
		return fmt.Errorf("argo cgroup level %d does not match path %q", selector.Level, selector.Path)
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.IndexFunc(part, func(character rune) bool {
			return character < 0x20 || character == 0x7f
		}) >= 0 {
			return fmt.Errorf("invalid Argo cgroup path %q", selector.Path)
		}
	}

	return nil
}

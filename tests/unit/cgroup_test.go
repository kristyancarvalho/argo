package unit_test

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/kristyancarvalho/argo/internal/qos"
)

func TestResolveCgroupID(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "user.slice", "argo.service")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	identifier, err := qos.ResolveCgroupID(strings.NewReader("0::/user.slice/argo.service\n"), root)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	expected := info.Sys().(*syscall.Stat_t).Ino
	if identifier != expected {
		t.Fatalf("cgroup identifier is %d, expected %d", identifier, expected)
	}
}

func TestResolveCgroupIDRejectsLegacyMembership(t *testing.T) {
	if _, err := qos.ResolveCgroupID(strings.NewReader("2:cpu:/argo\n"), t.TempDir()); err == nil {
		t.Fatal("legacy cgroup membership was accepted")
	}
}

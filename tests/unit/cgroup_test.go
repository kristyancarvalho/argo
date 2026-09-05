package unit_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kristyancarvalho/argo/internal/qos"
)

func TestResolveCgroup(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "user.slice", "argo.service")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	selector, err := qos.ResolveCgroup(strings.NewReader("0::/user.slice/argo.service\n"), root)
	if err != nil {
		t.Fatal(err)
	}
	expected := (qos.CgroupSelector{Path: "user.slice/argo.service", Level: 2})
	if selector != expected {
		t.Fatalf("cgroup selector is %+v, expected %+v", selector, expected)
	}
}

func TestResolveCgroupRejectsLegacyMembership(t *testing.T) {
	if _, err := qos.ResolveCgroup(strings.NewReader("2:cpu:/argo\n"), t.TempDir()); err == nil {
		t.Fatal("legacy cgroup membership was accepted")
	}
}

func TestCgroupSelectorRejectsInvalidPathOrLevel(t *testing.T) {
	for _, selector := range []qos.CgroupSelector{
		{},
		{Path: "/argo.service", Level: 1},
		{Path: "user.slice/../argo.service", Level: 2},
		{Path: "argo.service", Level: 2},
		{Path: "argo\n.service", Level: 1},
	} {
		if err := selector.Validate(); err == nil {
			t.Fatalf("invalid selector succeeded: %+v", selector)
		}
	}
}

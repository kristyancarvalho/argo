package integration_test

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/kristyancarvalho/argo/internal/storage"
)

func TestDaemonInstanceLockExcludesSameDatabaseAcrossSocketPaths(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "state", "argo.db")
	first, err := storage.AcquireInstanceLock(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := storage.AcquireInstanceLock(databasePath)
	var locked storage.InstanceLockedError
	if !errors.As(err, &locked) || second != nil {
		t.Fatalf("duplicate database lock returned lock=%v error=%v", second, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := storage.AcquireInstanceLock(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonInstanceLockConcurrentStartupHasOneOwner(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "argo.db")
	start := make(chan struct{})
	release := make(chan struct{})
	type result struct {
		lock *storage.InstanceLock
		err  error
	}
	results := make(chan result, 8)
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			lock, err := storage.AcquireInstanceLock(databasePath)
			results <- result{lock: lock, err: err}
			if lock != nil {
				<-release
				_ = lock.Close()
			}
		}()
	}
	close(start)
	owners := 0
	locked := 0
	for range 8 {
		result := <-results
		if result.lock != nil && result.err == nil {
			owners++
			continue
		}
		var contention storage.InstanceLockedError
		if errors.As(result.err, &contention) {
			locked++
			continue
		}
		t.Fatalf("unexpected concurrent lock result: %+v", result)
	}
	close(release)
	workers.Wait()
	if owners != 1 || locked != 7 {
		t.Fatalf("concurrent startup owners=%d locked=%d", owners, locked)
	}
}

func TestDaemonInstanceLockRejectsSymlinkAndHardLink(t *testing.T) {
	directory := t.TempDir()
	victim := filepath.Join(directory, "victim")
	if err := os.WriteFile(victim, []byte("unchanged"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, create := range map[string]func(string) error{
		"symlink":  func(path string) error { return os.Symlink(victim, path) },
		"hardlink": func(path string) error { return os.Link(victim, path) },
	} {
		t.Run(name, func(t *testing.T) {
			databasePath := filepath.Join(directory, name)
			if err := create(databasePath + ".lock"); err != nil {
				t.Fatal(err)
			}
			if lock, err := storage.AcquireInstanceLock(databasePath); err == nil || lock != nil {
				t.Fatalf("unsafe lock path returned lock=%v error=%v", lock, err)
			}
			content, err := os.ReadFile(victim)
			if err != nil {
				t.Fatal(err)
			}
			if string(content) != "unchanged" {
				t.Fatalf("victim content changed to %q", content)
			}
			info, err := os.Stat(victim)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o644 {
				t.Fatalf("victim mode changed to %o", info.Mode().Perm())
			}
		})
	}
}

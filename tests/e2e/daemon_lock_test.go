package e2e_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/ipc"
)

func TestDuplicateDaemonCannotRecoverLiveTransfer(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "argod")
	build := exec.Command("go", "build", "-o", binary, "./cmd/argod")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build argod: %v: %s", err, output)
	}
	for _, chunks := range []int{1, 4} {
		t.Run(fmt.Sprintf("chunks-%d", chunks), func(t *testing.T) {
			testDuplicateDaemonTransfer(t, binary, chunks)
		})
	}
}

func testDuplicateDaemonTransfer(t *testing.T, binary string, chunks int) {
	t.Helper()
	payload := bytes.Repeat([]byte("argo-instance-lock"), 256*1024)
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
	})
	server, maximumRequests := duplicateDaemonServer(t, payload, release)
	root, err := os.MkdirTemp("/tmp", "argo-daemon-lock-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	data := filepath.Join(root, "data")
	state := filepath.Join(root, "state")
	home := filepath.Join(root, "home")
	primaryRuntime := filepath.Join(root, "runtime-primary")
	duplicateRuntime := filepath.Join(root, "runtime-duplicate")
	baseEnvironment := append(
		os.Environ(),
		"XDG_CONFIG_HOME="+filepath.Join(root, "config"),
		"XDG_DATA_HOME="+data,
		"XDG_STATE_HOME="+state,
		"HOME="+home,
	)
	primaryEnvironment := append(append([]string(nil), baseEnvironment...), "XDG_RUNTIME_DIR="+primaryRuntime)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	primary := exec.CommandContext(ctx, binary, "-max-chunks-per-download", strconv.Itoa(chunks))
	primary.Env = primaryEnvironment
	var primaryError bytes.Buffer
	primary.Stderr = &primaryError
	if err := primary.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if primary.ProcessState == nil {
			_ = primary.Process.Signal(os.Interrupt)
			_ = primary.Wait()
		}
	})
	client := ipc.NewClient(filepath.Join(primaryRuntime, "argo", "argod.sock"))
	waitForDaemonClient(t, ctx, client, primaryError.String)
	destination := filepath.Join(root, "downloads")
	added, err := client.Add(ctx, server.URL+"/large.bin", destination)
	if err != nil {
		t.Fatal(err)
	}
	before := waitForLiveDownload(t, ctx, client, added.ID)
	if chunks > 1 {
		deadline := time.Now().Add(3 * time.Second)
		for maximumRequests.Load() < 2 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if maximumRequests.Load() < 2 {
			t.Fatal("parallel fixture did not start multiple chunk requests")
		}
	}
	partial := filepath.Join(state, "argo", "parts", added.ID+".part")
	partialInfo, err := os.Stat(partial)
	if err != nil {
		t.Fatal(err)
	}
	duplicateEnvironment := append(append([]string(nil), baseEnvironment...), "XDG_RUNTIME_DIR="+duplicateRuntime)
	duplicate := exec.CommandContext(ctx, binary, "-max-chunks-per-download", strconv.Itoa(chunks))
	duplicate.Env = duplicateEnvironment
	output, duplicateError := duplicate.CombinedOutput()
	if duplicateError == nil || !strings.Contains(string(output), "active Argo daemon") {
		t.Fatalf("duplicate daemon returned %v: %s", duplicateError, output)
	}
	after, err := client.Show(ctx, added.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "downloading" || after.Error != before.Error || after.DownloadedBytes < before.DownloadedBytes {
		t.Fatalf("duplicate daemon mutated live state: before=%+v after=%+v", before, after)
	}
	afterPartial, err := os.Stat(partial)
	if err != nil {
		t.Fatal(err)
	}
	if afterPartial.Size() != partialInfo.Size() {
		t.Fatalf("duplicate daemon changed partial size from %d to %d", partialInfo.Size(), afterPartial.Size())
	}
	if _, err := os.Stat(filepath.Join(duplicateRuntime, "argo", "argod.sock")); !os.IsNotExist(err) {
		t.Fatalf("duplicate daemon created a second socket: %v", err)
	}
	releaseOnce.Do(func() { close(release) })
	waitForDownloadStatus(t, ctx, client, added.ID, "completed")
	content, err := os.ReadFile(filepath.Join(destination, "large.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, payload) {
		t.Fatal("primary daemon completed with corrupted content")
	}
	if err := primary.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := primary.Wait(); err != nil {
		t.Fatalf("primary daemon shutdown: %v: %s", err, primaryError.String())
	}
}

func duplicateDaemonServer(t *testing.T, payload []byte, release <-chan struct{}) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var active atomic.Int32
	var maximum atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		start, end, ranged, err := daemonTestRange(request.Header.Get("Range"), int64(len(payload)))
		if err != nil {
			http.Error(response, err.Error(), http.StatusRequestedRangeNotSatisfiable)
			return
		}
		response.Header().Set("ETag", `"instance-lock"`)
		response.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		if ranged {
			response.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
			response.WriteHeader(http.StatusPartialContent)
		}
		if start == 0 && end == 0 {
			_, _ = response.Write(payload[:1])
			return
		}
		current := active.Add(1)
		defer active.Add(-1)
		for observed := maximum.Load(); current > observed && !maximum.CompareAndSwap(observed, current); observed = maximum.Load() {
		}
		first := min(start+4096, end+1)
		if _, err := response.Write(payload[start:first]); err != nil {
			return
		}
		if flusher, ok := response.(http.Flusher); ok {
			flusher.Flush()
		}
		<-release
		_, _ = response.Write(payload[first : end+1])
	}))
	t.Cleanup(server.Close)

	return server, &maximum
}

func daemonTestRange(value string, total int64) (int64, int64, bool, error) {
	if value == "" {
		return 0, total - 1, false, nil
	}
	if !strings.HasPrefix(value, "bytes=") {
		return 0, 0, false, fmt.Errorf("invalid range")
	}
	bounds := strings.Split(strings.TrimPrefix(value, "bytes="), "-")
	if len(bounds) != 2 {
		return 0, 0, false, fmt.Errorf("invalid range")
	}
	start, err := strconv.ParseInt(bounds[0], 10, 64)
	if err != nil {
		return 0, 0, false, err
	}
	end := total - 1
	if bounds[1] != "" {
		end, err = strconv.ParseInt(bounds[1], 10, 64)
		if err != nil {
			return 0, 0, false, err
		}
	}
	if start < 0 || end < start || end >= total {
		return 0, 0, false, fmt.Errorf("range outside resource")
	}

	return start, end, true, nil
}

func waitForDaemonClient(t *testing.T, ctx context.Context, client *ipc.Client, diagnostic func() string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := client.Status(ctx); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon did not become ready: %s", diagnostic())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForLiveDownload(t *testing.T, ctx context.Context, client *ipc.Client, identifier string) ipc.Download {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		download, err := client.Show(ctx, identifier)
		if err != nil {
			t.Fatal(err)
		}
		if download.Status == "downloading" && download.DownloadedBytes > 0 {
			return download
		}
		if time.Now().After(deadline) {
			t.Fatalf("download did not become active: %+v", download)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForDownloadStatus(t *testing.T, ctx context.Context, client *ipc.Client, identifier, status string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		download, err := client.Show(ctx, identifier)
		if err != nil {
			t.Fatal(err)
		}
		if download.Status == status {
			return
		}
		if download.Status == "failed" || time.Now().After(deadline) {
			t.Fatalf("download did not reach %s: %+v", status, download)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

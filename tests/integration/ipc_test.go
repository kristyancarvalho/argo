package integration_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/ipc"
)

func TestIPCSocketStartupAndStatus(t *testing.T) {
	server, cancel, finished := startIPCServer(t)
	defer stopIPCServer(t, server, cancel, finished)

	info, err := os.Stat(server.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("%s is not a Unix socket", server.SocketPath())
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket permissions are %o, expected 600", info.Mode().Perm())
	}

	status, err := ipc.NewClient(server.SocketPath()).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "running" || status.PID != os.Getpid() || status.ProtocolVersion != ipc.ProtocolVersion {
		t.Fatalf("unexpected daemon status: %+v", status)
	}
	if status.StartedAt.IsZero() {
		t.Fatal("daemon status has no start time")
	}
}

func TestIPCRejectsMalformedRequestAndContinues(t *testing.T) {
	server, cancel, finished := startIPCServer(t)
	defer stopIPCServer(t, server, cancel, finished)

	connection, err := net.Dial("unix", server.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write([]byte("{invalid json}\n")); err != nil {
		t.Fatal(err)
	}
	message, err := bufio.NewReader(connection).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}

	var response ipc.Response
	if err := json.Unmarshal(message, &response); err != nil {
		t.Fatal(err)
	}
	if response.OK || response.Error == nil || response.Error.Code != "malformed_request" {
		t.Fatalf("unexpected malformed-request response: %+v", response)
	}
	if _, err := ipc.NewClient(server.SocketPath()).Status(context.Background()); err != nil {
		t.Fatalf("server did not continue after malformed request: %v", err)
	}
}

func TestIPCServerGracefulShutdown(t *testing.T) {
	server, cancel, finished := startIPCServer(t)
	cancel()

	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("IPC server did not stop after cancellation")
	}
	if _, err := os.Stat(server.SocketPath()); !os.IsNotExist(err) {
		t.Fatalf("socket still exists after shutdown: %v", err)
	}
}

func TestIPCRefusesToReplaceRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argod.sock")
	if err := os.WriteFile(path, []byte("owned data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ipc.Listen(path, ipc.NewStatusHandler()); err == nil {
		t.Fatal("Listen replaced a regular file")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "owned data" {
		t.Fatalf("regular file content changed to %q", content)
	}
}

func startIPCServer(t *testing.T) (*ipc.Server, context.CancelFunc, <-chan error) {
	t.Helper()
	server, err := ipc.Listen(filepath.Join(t.TempDir(), "argo", "argod.sock"), ipc.NewStatusHandler())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() {
		finished <- server.Serve(ctx)
	}()

	return server, cancel, finished
}

func stopIPCServer(t *testing.T, server *ipc.Server, cancel context.CancelFunc, finished <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(2 * time.Second):
		if err := server.Close(); err != nil {
			t.Error(err)
		}
		t.Error("IPC server did not stop")
	}
}

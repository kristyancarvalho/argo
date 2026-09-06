package integration_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/ipc"
)

type blockingIPCHandler struct {
	started  chan struct{}
	finished chan struct{}
}

type oversizedIPCHandler struct{}

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

func TestIPCIdleClientDoesNotBlockConcurrentStatus(t *testing.T) {
	server, cancel, finished := startIPCServer(t)
	defer stopIPCServer(t, server, cancel, finished)
	idle, err := net.Dial("unix", server.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = idle.Close() }()

	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	started := time.Now()
	if _, err := ipc.NewClient(server.SocketPath()).Status(ctx); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("status waited %s behind an idle client", elapsed)
	}
}

func TestIPCBlockedHandlerDoesNotBlockStatusAndObservesClientCancellation(t *testing.T) {
	handler := &blockingIPCHandler{started: make(chan struct{}), finished: make(chan struct{})}
	server, cancel, finished := startIPCServerWithHandler(t, handler)
	defer stopIPCServer(t, server, cancel, finished)
	client := ipc.NewClient(server.SocketPath())
	requestContext, cancelRequest := context.WithCancel(context.Background())
	requestFinished := make(chan error, 1)
	go func() {
		_, err := client.Resume(requestContext, "00000000000000000000000000000000")
		requestFinished <- err
	}()
	awaitSignal(t, handler.started)
	statusContext, stopStatus := context.WithTimeout(context.Background(), time.Second)
	defer stopStatus()
	if _, err := client.Status(statusContext); err != nil {
		t.Fatalf("status was blocked by another handler: %v", err)
	}
	cancelRequest()
	select {
	case err := <-requestFinished:
		if err == nil {
			t.Fatal("canceled request succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("client cancellation did not stop request")
	}
	awaitSignal(t, handler.finished)
}

func TestIPCRejectsOversizedMessageAndLimitsConcurrentConnections(t *testing.T) {
	server, cancel, finished := startIPCServer(t)
	defer stopIPCServer(t, server, cancel, finished)
	connection, err := net.Dial("unix", server.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	message := make([]byte, (1<<20)+1)
	for index := range message {
		message[index] = 'x'
	}
	message = append(message, '\n')
	if _, err := connection.Write(message); err != nil {
		t.Fatal(err)
	}
	var oversized ipc.Response
	if err := json.NewDecoder(connection).Decode(&oversized); err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	if oversized.Error == nil || oversized.Error.Code != "request_too_large" {
		t.Fatalf("oversized request returned %+v", oversized)
	}

	connections := make([]net.Conn, 0, 70)
	for range 70 {
		connection, err := net.Dial("unix", server.SocketPath())
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, connection)
	}
	time.Sleep(100 * time.Millisecond)
	deadline := time.Now().Add(500 * time.Millisecond)
	busy := 0
	for index := len(connections) - 1; index >= 0; index-- {
		connection := connections[index]
		_ = connection.SetReadDeadline(deadline)
		var response ipc.Response
		if err := json.NewDecoder(connection).Decode(&response); err == nil &&
			response.Error != nil && response.Error.Code == "server_busy" {
			busy++
		}
		_ = connection.Close()
	}
	if busy == 0 {
		t.Fatal("IPC server admitted every connection without enforcing its cap")
	}
}

func TestIPCReportsOversizedResponsePrecisely(t *testing.T) {
	server, cancel, finished := startIPCServerWithHandler(t, oversizedIPCHandler{})
	defer stopIPCServer(t, server, cancel, finished)
	var response string
	err := ipc.NewClient(server.SocketPath()).Call(context.Background(), ipc.OperationStatus, nil, &response)
	var remote ipc.RemoteError
	if !errors.As(err, &remote) {
		t.Fatalf("oversized response returned %T: %v", err, err)
	}
	if remote.Code != "response_too_large" {
		t.Fatalf("oversized response returned code %q", remote.Code)
	}
}

func TestIPCShutdownCancelsBlockedRequest(t *testing.T) {
	handler := &blockingIPCHandler{started: make(chan struct{}), finished: make(chan struct{})}
	server, cancel, finished := startIPCServerWithHandler(t, handler)
	client := ipc.NewClient(server.SocketPath())
	requestFinished := make(chan error, 1)
	go func() {
		_, err := client.Resume(context.Background(), "00000000000000000000000000000000")
		requestFinished <- err
	}()
	awaitSignal(t, handler.started)
	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		_ = server.Close()
		t.Fatal("IPC server did not stop with a blocked request")
	}
	awaitSignal(t, handler.finished)
	select {
	case <-requestFinished:
	case <-time.After(time.Second):
		t.Fatal("blocked client remained after server shutdown")
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
	return startIPCServerWithHandler(t, ipc.NewStatusHandler())
}

func startIPCServerWithHandler(
	t *testing.T,
	handler ipc.Handler,
) (*ipc.Server, context.CancelFunc, <-chan error) {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "argo-ipc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	server, err := ipc.Listen(filepath.Join(directory, "argod.sock"), handler)
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

func (handler *blockingIPCHandler) Handle(ctx context.Context, request ipc.Request) (any, error) {
	if request.Operation == ipc.OperationStatus {
		return ipc.NewStatusHandler().Handle(ctx, request)
	}
	select {
	case <-handler.started:
	default:
		close(handler.started)
	}
	<-ctx.Done()
	select {
	case <-handler.finished:
	default:
		close(handler.finished)
	}

	return nil, ctx.Err()
}

func (oversizedIPCHandler) Handle(context.Context, ipc.Request) (any, error) {
	return strings.Repeat("x", 2<<20), nil
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

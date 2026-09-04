package integration_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/qos"
	"github.com/kristyancarvalho/argo/internal/qosipc"
)

type staticQoSAuthorizer struct {
	err error
}

func (authorizer staticQoSAuthorizer) Authorize(*net.UnixConn) error {
	return authorizer.err
}

func TestQoSHelperValidApplyStatusAndRemove(t *testing.T) {
	backend := &recordingQoSBackend{}
	server, cancel, finished := startQoSServer(t, backend, staticQoSAuthorizer{})
	defer stopQoSServer(t, server, cancel, finished)
	client := qosipc.NewClient(server.SocketPath())
	desired, err := qos.CalculateDesiredState(qos.Intent{
		Policy:    qos.PolicyBalanced,
		Interface: "eth0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Apply(context.Background(), desired); err != nil {
		t.Fatal(err)
	}
	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Applied || status.State != desired || len(backend.applied) != 1 {
		t.Fatalf("unexpected applied status: %+v, backend %+v", status, backend.applied)
	}
	if err := client.Remove(context.Background(), "wlan0"); err == nil {
		t.Fatal("removing another interface succeeded")
	}
	if err := client.Remove(context.Background(), "eth0"); err != nil {
		t.Fatal(err)
	}
	status, err = client.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Applied || len(backend.removed) != 1 || backend.removed[0] != "eth0" {
		t.Fatalf("unexpected removed status: %+v, backend %+v", status, backend.removed)
	}
}

func TestQoSHelperRejectsInvalidAndUnsupportedRequests(t *testing.T) {
	backend := &recordingQoSBackend{}
	server, cancel, finished := startQoSServer(t, backend, staticQoSAuthorizer{})
	defer stopQoSServer(t, server, cancel, finished)
	tests := []struct {
		message string
		code    string
	}{
		{`{"version":1,"id":"one","operation":"execute","payload":{"command":"id"}}` + "\n", "unsupported_operation"},
		{`{"version":1,"id":"two","operation":"apply","payload":{"state":{"Enabled":false,"Policy":"off","Interface":""}}}` + "\n", "invalid_request"},
		{`{"version":1,"id":"three","operation":"remove","payload":{"interface":"eth0","command":"id"}}` + "\n", "invalid_request"},
		{"{malformed}\n", "malformed_request"},
	}
	for _, test := range tests {
		response := sendQoSRequest(t, server.SocketPath(), test.message)
		if response.OK || response.Error == nil || response.Error.Code != test.code {
			t.Fatalf("request %q returned %+v", test.message, response)
		}
	}
	if len(backend.applied) != 0 || len(backend.removed) != 0 {
		t.Fatalf("invalid requests reached backend: %+v %+v", backend.applied, backend.removed)
	}
}

func TestQoSHelperRejectsUnauthorizedPeerBeforePayload(t *testing.T) {
	backend := &recordingQoSBackend{}
	server, cancel, finished := startQoSServer(
		t,
		backend,
		staticQoSAuthorizer{err: errors.New("denied")},
	)
	defer stopQoSServer(t, server, cancel, finished)
	connection, err := net.Dial("unix", server.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = connection.Close()
	}()
	response := readQoSResponse(t, connection)
	if response.OK || response.Error == nil || response.Error.Code != "unauthorized" || response.ID != "" {
		t.Fatalf("unexpected unauthorized response: %+v", response)
	}
	if len(backend.applied) != 0 || len(backend.removed) != 0 {
		t.Fatal("unauthorized request reached backend")
	}
}

func startQoSServer(
	t *testing.T,
	backend qos.Backend,
	authorizer qosipc.Authorizer,
) (*qosipc.Server, context.CancelFunc, <-chan error) {
	t.Helper()
	controller, err := qos.NewController(backend)
	if err != nil {
		t.Fatal(err)
	}
	service, err := qosipc.NewService(controller)
	if err != nil {
		t.Fatal(err)
	}
	server, err := qosipc.Listen(filepath.Join(t.TempDir(), "argo-qosd.sock"), service, authorizer)
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

func stopQoSServer(
	t *testing.T,
	server *qosipc.Server,
	cancel context.CancelFunc,
	finished <-chan error,
) {
	t.Helper()
	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(2 * time.Second):
		_ = server.Close()
		t.Error("QoS helper server did not stop")
	}
}

func sendQoSRequest(t *testing.T, socketPath, message string) qosipc.Response {
	t.Helper()
	connection, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = connection.Close()
	}()
	if _, err := connection.Write([]byte(message)); err != nil {
		t.Fatal(err)
	}
	return readQoSResponse(t, connection)
}

func readQoSResponse(t *testing.T, connection net.Conn) qosipc.Response {
	t.Helper()
	encoded, err := bufio.NewReader(connection).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response qosipc.Response
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatal(err)
	}

	return response
}

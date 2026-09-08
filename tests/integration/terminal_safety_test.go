package integration_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kristyancarvalho/argo/internal/daemon"
	"github.com/kristyancarvalho/argo/internal/ipc"
	"github.com/kristyancarvalho/argo/internal/model"
)

func TestDaemonNeutralizesControlBearingRemoteFilename(t *testing.T) {
	store := openTestStore(t)
	engine := newControlledDownloadEngine(store)
	service, err := daemon.NewService(context.Background(), store, engine)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSchedulerService(t, service)
	payload, err := json.Marshal(ipc.AddRequest{
		URL: "https://example.test/%1b%5d0%3btitle%07RED%0aFAKE%0d%09%E2%80%AE.bin", Destination: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Handle(context.Background(), ipc.Request{Operation: ipc.OperationAdd, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	response := result.(ipc.AddResponse)
	if strings.ContainsAny(response.Filename, "\x1b\a\n\r\t") || strings.ContainsRune(response.Filename, '\u202e') ||
		response.Filename != "_]0;title_RED_FAKE___.bin" {
		t.Fatalf("unsafe admitted filename is %q", response.Filename)
	}
	identifier, err := model.ParseDownloadID(response.ID)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := store.Download(context.Background(), identifier)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Filename != response.Filename {
		t.Fatalf("persisted filename is %q, response is %q", persisted.Filename, response.Filename)
	}
}

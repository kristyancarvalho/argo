package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/daemon"
	"github.com/kristyancarvalho/argo/internal/ipc"
	"github.com/kristyancarvalho/argo/internal/model"
	"github.com/kristyancarvalho/argo/internal/storage"
)

type controlledDownloadEngine struct {
	store         *storage.Store
	mutex         sync.Mutex
	gates         map[string]chan struct{}
	started       chan model.Download
	active        int
	maximumActive int
	rateLimit     int64
}

func (engine *controlledDownloadEngine) SetRateLimit(bytesPerSecond int64) error {
	engine.mutex.Lock()
	defer engine.mutex.Unlock()
	engine.rateLimit = bytesPerSecond

	return nil
}

func (engine *controlledDownloadEngine) ValidateCanceledResume(context.Context, model.Download) error {
	return nil
}

func (engine *controlledDownloadEngine) currentRateLimit() int64 {
	engine.mutex.Lock()
	defer engine.mutex.Unlock()

	return engine.rateLimit
}

func TestConcurrentSchedulerPreservesQueueOrderAndStartsNext(t *testing.T) {
	store := openTestStore(t)
	engine := newControlledDownloadEngine(store, "first", "second", "third")
	service := newSchedulerService(t, store, engine, 1)
	defer closeSchedulerService(t, service)

	first := addScheduledDownload(t, service, "first")
	second := addScheduledDownload(t, service, "second")
	third := addScheduledDownload(t, service, "third")
	assertStartedDownload(t, engine, first)
	assertNoStartedDownload(t, engine)
	engine.release("first")
	assertStartedDownload(t, engine, second)
	engine.release("second")
	assertStartedDownload(t, engine, third)
	engine.release("third")
	waitForDownload(t, store, third, func(download model.Download) bool {
		return download.Status == model.StatusCompleted
	})
}

func TestConcurrentSchedulerEnforcesGlobalCap(t *testing.T) {
	store := openTestStore(t)
	engine := newControlledDownloadEngine(store, "one", "two", "three", "four")
	service := newSchedulerService(t, store, engine, 2)
	defer closeSchedulerService(t, service)

	identifiers := []model.DownloadID{
		addScheduledDownload(t, service, "one"),
		addScheduledDownload(t, service, "two"),
		addScheduledDownload(t, service, "three"),
		addScheduledDownload(t, service, "four"),
	}
	started := []model.DownloadID{
		waitStartedDownload(t, engine).ID,
		waitStartedDownload(t, engine).ID,
	}
	assertNoStartedDownload(t, engine)
	if engine.maximum() != 2 {
		t.Fatalf("maximum active downloads is %d, expected 2", engine.maximum())
	}
	engine.release(filenameForID(t, store, started[0]))
	next := waitStartedDownload(t, engine).ID
	if next != identifiers[2] {
		t.Fatalf("next download is %s, expected %s", next, identifiers[2])
	}
	for _, name := range []string{"two", "three", "four", "one"} {
		engine.releaseIfPending(name)
	}
}

func TestConcurrentSchedulerPauseReleasesSlotAndResumeReentersQueue(t *testing.T) {
	store := openTestStore(t)
	engine := newControlledDownloadEngine(store, "paused", "waiting")
	service := newSchedulerService(t, store, engine, 1)
	defer closeSchedulerService(t, service)

	paused := addScheduledDownload(t, service, "paused")
	assertStartedDownload(t, engine, paused)
	waiting := addScheduledDownload(t, service, "waiting")
	actionScheduledDownload(t, service, ipc.OperationPause, paused)
	assertStartedDownload(t, engine, waiting)
	actionScheduledDownload(t, service, ipc.OperationResume, paused)
	assertNoStartedDownload(t, engine)
	engine.release("waiting")
	assertStartedDownload(t, engine, paused)
	engine.release("paused")
	waitForDownload(t, store, paused, func(download model.Download) bool {
		return download.Status == model.StatusCompleted
	})
}

func TestConcurrentSchedulerOrdersUpdatedPrioritiesStably(t *testing.T) {
	store := openTestStore(t)
	engine := newControlledDownloadEngine(store, "blocker", "low", "normal", "high-one", "high-two")
	service := newSchedulerService(t, store, engine, 1)
	defer closeSchedulerService(t, service)

	blocker := addScheduledDownload(t, service, "blocker")
	assertStartedDownload(t, engine, blocker)
	low := addScheduledDownload(t, service, "low")
	normal := addScheduledDownload(t, service, "normal")
	highOne := addScheduledDownload(t, service, "high-one")
	highTwo := addScheduledDownload(t, service, "high-two")
	setScheduledPriority(t, service, low, string(model.PriorityLow))
	setScheduledPriority(t, service, highOne, string(model.PriorityHigh))
	setScheduledPriority(t, service, highTwo, string(model.PriorityHigh))
	setScheduledPriority(t, service, blocker, string(model.PriorityHigh))
	persisted, err := store.Download(context.Background(), blocker)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Priority != model.PriorityHigh {
		t.Fatalf("active priority is %s, expected high", persisted.Priority)
	}

	engine.release("blocker")
	for _, expected := range []struct {
		identifier model.DownloadID
		name       string
	}{
		{highOne, "high-one"},
		{highTwo, "high-two"},
		{normal, "normal"},
		{low, "low"},
	} {
		assertStartedDownload(t, engine, expected.identifier)
		engine.release(expected.name)
	}
}

func TestConcurrentSchedulerRejectsInvalidPriority(t *testing.T) {
	store := openTestStore(t)
	engine := newControlledDownloadEngine(store, "invalid")
	service := newSchedulerService(t, store, engine, 1)
	defer closeSchedulerService(t, service)
	identifier := addScheduledDownload(t, service, "invalid")
	assertStartedDownload(t, engine, identifier)

	payload, err := json.Marshal(ipc.PriorityRequest{ID: identifier.String(), Priority: "urgent"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Handle(context.Background(), ipc.Request{
		Operation: ipc.OperationPriority,
		Payload:   payload,
	})
	var priorityError model.InvalidPriorityError
	if !errors.As(err, &priorityError) {
		t.Fatalf("invalid priority returned %T, expected InvalidPriorityError", err)
	}
	engine.release("invalid")
}

func TestConcurrentSchedulerUsesConfiguredDefaultPriority(t *testing.T) {
	store := openTestStore(t)
	engine := newControlledDownloadEngine(store, "configured")
	service, err := daemon.NewServiceWithOptions(context.Background(), store, engine, daemon.ServiceOptions{
		MaximumConcurrentDownloads: 1,
		DefaultPriority:            model.PriorityHigh,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeSchedulerService(t, service)
	identifier := addScheduledDownload(t, service, "configured")
	assertStartedDownload(t, engine, identifier)
	download, err := store.Download(context.Background(), identifier)
	if err != nil {
		t.Fatal(err)
	}
	if download.Priority != model.PriorityHigh {
		t.Fatalf("download priority is %s, expected high", download.Priority)
	}
	engine.release("configured")
}

func TestServiceCloseStopsActiveWorkers(t *testing.T) {
	store := openTestStore(t)
	engine := newControlledDownloadEngine(store, "active")
	service := newSchedulerService(t, store, engine, 1)
	identifier := addScheduledDownload(t, service, "active")
	assertStartedDownload(t, engine, identifier)
	closed := make(chan error, 1)
	go func() {
		closed <- service.Close()
	}()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("service close did not stop active workers")
	}
	if engine.activeCount() != 0 {
		t.Fatalf("active worker count is %d after service close", engine.activeCount())
	}
}

func (engine *controlledDownloadEngine) Download(ctx context.Context, download model.Download) error {
	if download.Status == model.StatusQueued {
		if err := engine.store.UpdateDownloadStatus(
			context.Background(),
			download.ID,
			model.StatusDownloading,
			time.Now().UTC(),
			"",
		); err != nil {
			return err
		}
	}
	engine.mutex.Lock()
	engine.active++
	if engine.active > engine.maximumActive {
		engine.maximumActive = engine.active
	}
	gate := engine.gates[download.Filename]
	engine.mutex.Unlock()
	engine.started <- download

	select {
	case <-ctx.Done():
		engine.decrementActive()
		return ctx.Err()
	case <-gate:
		engine.decrementActive()
		return engine.store.UpdateDownloadStatus(
			context.Background(),
			download.ID,
			model.StatusCompleted,
			time.Now().UTC(),
			"",
		)
	}
}

func setScheduledPriority(
	t *testing.T,
	service *daemon.Service,
	identifier model.DownloadID,
	priority string,
) {
	t.Helper()
	payload, err := json.Marshal(ipc.PriorityRequest{ID: identifier.String(), Priority: priority})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Handle(context.Background(), ipc.Request{
		Operation: ipc.OperationPriority,
		Payload:   payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	response, valid := result.(ipc.PriorityResponse)
	if !valid || response.ID != identifier.String() || response.Priority != priority {
		t.Fatalf("unexpected priority response: %+v", result)
	}
}

func newControlledDownloadEngine(store *storage.Store, names ...string) *controlledDownloadEngine {
	gates := make(map[string]chan struct{}, len(names))
	for _, name := range names {
		gates[name] = make(chan struct{})
	}

	return &controlledDownloadEngine{
		store:   store,
		gates:   gates,
		started: make(chan model.Download, len(names)+1),
	}
}

func newSchedulerService(
	t *testing.T,
	store *storage.Store,
	engine *controlledDownloadEngine,
	maximum int,
) *daemon.Service {
	t.Helper()
	service, err := daemon.NewServiceWithOptions(context.Background(), store, engine, daemon.ServiceOptions{
		MaximumConcurrentDownloads: maximum,
	})
	if err != nil {
		t.Fatal(err)
	}

	return service
}

func addScheduledDownload(t *testing.T, service *daemon.Service, name string) model.DownloadID {
	t.Helper()
	payload, err := json.Marshal(ipc.AddRequest{
		URL:         "https://example.test/" + name,
		Destination: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Handle(context.Background(), ipc.Request{
		Operation: ipc.OperationAdd,
		Payload:   payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	response, valid := result.(ipc.AddResponse)
	if !valid {
		t.Fatalf("add response has type %T", result)
	}
	identifier, err := model.ParseDownloadID(response.ID)
	if err != nil {
		t.Fatal(err)
	}

	return identifier
}

func actionScheduledDownload(
	t *testing.T,
	service *daemon.Service,
	operation ipc.Operation,
	identifier model.DownloadID,
) {
	t.Helper()
	payload, err := json.Marshal(ipc.DownloadActionRequest{ID: identifier.String()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Handle(context.Background(), ipc.Request{
		Operation: operation,
		Payload:   payload,
	}); err != nil {
		t.Fatal(err)
	}
}

func assertStartedDownload(t *testing.T, engine *controlledDownloadEngine, expected model.DownloadID) {
	t.Helper()
	started := waitStartedDownload(t, engine)
	if started.ID != expected {
		t.Fatalf("started download is %s, expected %s", started.ID, expected)
	}
}

func waitStartedDownload(t *testing.T, engine *controlledDownloadEngine) model.Download {
	t.Helper()
	select {
	case download := <-engine.started:
		return download
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for scheduled download")
		return model.Download{}
	}
}

func assertNoStartedDownload(t *testing.T, engine *controlledDownloadEngine) {
	t.Helper()
	select {
	case download := <-engine.started:
		t.Fatalf("unexpected download started: %s", download.ID)
	case <-time.After(50 * time.Millisecond):
	}
}

func (engine *controlledDownloadEngine) release(name string) {
	engine.mutex.Lock()
	defer engine.mutex.Unlock()
	close(engine.gates[name])
}

func (engine *controlledDownloadEngine) releaseIfPending(name string) {
	engine.mutex.Lock()
	defer engine.mutex.Unlock()
	select {
	case <-engine.gates[name]:
	default:
		close(engine.gates[name])
	}
}

func (engine *controlledDownloadEngine) decrementActive() {
	engine.mutex.Lock()
	defer engine.mutex.Unlock()
	engine.active--
}

func (engine *controlledDownloadEngine) maximum() int {
	engine.mutex.Lock()
	defer engine.mutex.Unlock()

	return engine.maximumActive
}

func (engine *controlledDownloadEngine) activeCount() int {
	engine.mutex.Lock()
	defer engine.mutex.Unlock()

	return engine.active
}

func filenameForID(t *testing.T, store *storage.Store, identifier model.DownloadID) string {
	t.Helper()
	download, err := store.Download(context.Background(), identifier)
	if err != nil {
		t.Fatal(err)
	}

	return download.Filename
}

func closeSchedulerService(t *testing.T, service *daemon.Service) {
	t.Helper()
	if err := service.Close(); err != nil && !errors.Is(err, context.Canceled) {
		t.Error(err)
	}
}

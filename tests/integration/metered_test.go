package integration_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kristyancarvalho/argo/internal/daemon"
	"github.com/kristyancarvalho/argo/internal/ipc"
	"github.com/kristyancarvalho/argo/internal/model"
	"github.com/kristyancarvalho/argo/internal/network"
	"github.com/kristyancarvalho/argo/internal/storage"
)

type controlledNetworkObserver struct {
	updates chan controlledNetworkUpdate
}

type controlledNetworkUpdate struct {
	snapshot network.Snapshot
	applied  chan error
}

func TestMeteredPolicyPausesAndExplicitlyResumesDownloads(t *testing.T) {
	store := openTestStore(t)
	engine := newControlledDownloadEngine(store, "metered")
	observer := newControlledNetworkObserver()
	service := newMeteredService(t, store, engine, observer, true, true)
	defer closeSchedulerService(t, service)
	observer.send(t, network.Snapshot{Connected: true, Metered: network.MeteredNo})

	identifier := addScheduledDownload(t, service, "metered")
	assertStartedDownload(t, engine, identifier)
	observer.send(t, network.Snapshot{Metered: network.MeteredYes})
	waitForDownload(t, store, identifier, func(download model.Download) bool {
		return download.Status == model.StatusPaused
	})
	observer.send(t, network.Snapshot{Connected: true, Metered: network.MeteredNo})
	assertStartedDownload(t, engine, identifier)
	engine.release("metered")
	waitForDownload(t, store, identifier, func(download model.Download) bool {
		return download.Status == model.StatusCompleted
	})
}

func TestMeteredPolicyLeavesPausedDownloadsWithoutResumePolicy(t *testing.T) {
	store := openTestStore(t)
	engine := newControlledDownloadEngine(store, "no-resume")
	observer := newControlledNetworkObserver()
	service := newMeteredService(t, store, engine, observer, true, false)
	defer closeSchedulerService(t, service)
	observer.send(t, network.Snapshot{Connected: true, Metered: network.MeteredNo})

	identifier := addScheduledDownload(t, service, "no-resume")
	assertStartedDownload(t, engine, identifier)
	observer.send(t, network.Snapshot{Metered: network.MeteredGuessYes})
	waitForDownload(t, store, identifier, func(download model.Download) bool {
		return download.Status == model.StatusPaused
	})
	observer.send(t, network.Snapshot{Connected: true, Metered: network.MeteredGuessNo})
	assertNoStartedDownload(t, engine)
	download, err := store.Download(context.Background(), identifier)
	if err != nil {
		t.Fatal(err)
	}
	if download.Status != model.StatusPaused {
		t.Fatalf("download status is %s, expected paused", download.Status)
	}
}

func TestMeteredPolicyDisabledDoesNotPause(t *testing.T) {
	store := openTestStore(t)
	engine := newControlledDownloadEngine(store, "disabled")
	observer := newControlledNetworkObserver()
	service := newMeteredService(t, store, engine, observer, false, false)
	defer closeSchedulerService(t, service)
	observer.send(t, network.Snapshot{Connected: true, Metered: network.MeteredNo})

	identifier := addScheduledDownload(t, service, "disabled")
	assertStartedDownload(t, engine, identifier)
	observer.send(t, network.Snapshot{Metered: network.MeteredYes})
	assertNoStartedDownload(t, engine)
	download, err := store.Download(context.Background(), identifier)
	if err != nil {
		t.Fatal(err)
	}
	if download.Status != model.StatusDownloading {
		t.Fatalf("download status is %s, expected downloading", download.Status)
	}
	engine.release("disabled")
}

func TestMeteredPolicyDoesNotResumeUserPausedDownload(t *testing.T) {
	store := openTestStore(t)
	engine := newControlledDownloadEngine(store, "manual")
	observer := newControlledNetworkObserver()
	service := newMeteredService(t, store, engine, observer, true, true)
	defer closeSchedulerService(t, service)
	observer.send(t, network.Snapshot{Connected: true, Metered: network.MeteredNo})

	identifier := addScheduledDownload(t, service, "manual")
	assertStartedDownload(t, engine, identifier)
	actionScheduledDownload(t, service, ipc.OperationPause, identifier)
	waitForDownload(t, store, identifier, func(download model.Download) bool {
		return download.Status == model.StatusPaused
	})
	observer.send(t, network.Snapshot{Metered: network.MeteredYes})
	observer.send(t, network.Snapshot{Connected: true, Metered: network.MeteredNo})
	assertNoStartedDownload(t, engine)
}

func TestMeteredPolicyBlocksNewAdmissionsUntilUnmetered(t *testing.T) {
	store := openTestStore(t)
	engine := newControlledDownloadEngine(store, "admission")
	observer := newControlledNetworkObserver()
	service := newMeteredService(t, store, engine, observer, true, true)
	defer closeSchedulerService(t, service)
	observer.send(t, network.Snapshot{Connected: true, Metered: network.MeteredYes})

	identifier := addScheduledDownload(t, service, "admission")
	assertNoStartedDownload(t, engine)
	waitForDownload(t, store, identifier, func(download model.Download) bool {
		return download.Status == model.StatusPaused
	})
	observer.send(t, network.Snapshot{Connected: true, Metered: network.MeteredNo})
	assertStartedDownload(t, engine, identifier)
	engine.release("admission")
}

func TestMeteredPolicyBlocksResumeAndRetry(t *testing.T) {
	store := openTestStore(t)
	engine := newControlledDownloadEngine(store, "resume", "retry")
	observer := newControlledNetworkObserver()
	service := newMeteredService(t, store, engine, observer, true, true)
	defer closeSchedulerService(t, service)
	observer.send(t, network.Snapshot{Connected: true, Metered: network.MeteredNo})

	resumeID := addScheduledDownload(t, service, "resume")
	assertStartedDownload(t, engine, resumeID)
	actionScheduledDownload(t, service, ipc.OperationCancel, resumeID)
	waitForDownload(t, store, resumeID, func(download model.Download) bool {
		return download.Status == model.StatusCanceled
	})
	retryID := addScheduledDownload(t, service, "retry")
	assertStartedDownload(t, engine, retryID)
	engine.release("retry")
	waitForDownload(t, store, retryID, func(download model.Download) bool {
		return download.Status == model.StatusCompleted
	})
	observer.send(t, network.Snapshot{Connected: true, Metered: network.MeteredYes})

	actionScheduledDownload(t, service, ipc.OperationResume, resumeID)
	retried := retryScheduledDownload(t, service, retryID)
	assertNoStartedDownload(t, engine)
	for _, identifier := range []model.DownloadID{resumeID, retried} {
		waitForDownload(t, store, identifier, func(download model.Download) bool {
			return download.Status == model.StatusPaused
		})
	}
	observer.send(t, network.Snapshot{Connected: true, Metered: network.MeteredNo})
	first := waitStartedDownload(t, engine)
	engine.releaseIfPending(first.Filename)
	second := waitStartedDownload(t, engine)
	if first.ID != resumeID && second.ID != resumeID {
		t.Fatalf("resumed download %s did not restart", resumeID)
	}
	engine.releaseIfPending(second.Filename)
}

func TestMeteredPolicyPausesQueuedWorkBehindActiveSlot(t *testing.T) {
	store := openTestStore(t)
	engine := newControlledDownloadEngine(store, "active", "waiting")
	observer := newControlledNetworkObserver()
	service := newMeteredService(t, store, engine, observer, true, true)
	defer closeSchedulerService(t, service)
	observer.send(t, network.Snapshot{Connected: true, Metered: network.MeteredNo})

	active := addScheduledDownload(t, service, "active")
	assertStartedDownload(t, engine, active)
	waiting := addScheduledDownload(t, service, "waiting")
	observer.send(t, network.Snapshot{Connected: true, Metered: network.MeteredYes})
	for _, identifier := range []model.DownloadID{active, waiting} {
		waitForDownload(t, store, identifier, func(download model.Download) bool {
			return download.Status == model.StatusPaused
		})
	}
	assertNoStartedDownload(t, engine)
	observer.send(t, network.Snapshot{Connected: true, Metered: network.MeteredNo})
	for range 2 {
		started := waitStartedDownload(t, engine)
		engine.releaseIfPending(started.Filename)
	}
}

func TestMeteredPolicyGatesRecoveredDownloadsAtStartup(t *testing.T) {
	store := openTestStore(t)
	firstEngine := newControlledDownloadEngine(store, "recovered")
	firstService := newSchedulerService(t, store, firstEngine, 1)
	identifier := addScheduledDownload(t, firstService, "recovered")
	assertStartedDownload(t, firstEngine, identifier)
	closeSchedulerService(t, firstService)

	observer := newControlledNetworkObserver()
	applied := make(chan error, 1)
	observer.updates <- controlledNetworkUpdate{
		snapshot: network.Snapshot{Connected: true, Metered: network.MeteredYes},
		applied:  applied,
	}
	secondEngine := newControlledDownloadEngine(store, "recovered")
	service := newMeteredService(t, store, secondEngine, observer, true, true)
	defer closeSchedulerService(t, service)
	if err := <-applied; err != nil {
		t.Fatal(err)
	}
	assertNoStartedDownload(t, secondEngine)
	waitForDownload(t, store, identifier, func(download model.Download) bool {
		return download.Status == model.StatusPaused
	})
}

func retryScheduledDownload(t *testing.T, service *daemon.Service, identifier model.DownloadID) model.DownloadID {
	t.Helper()
	payload, err := json.Marshal(ipc.DownloadActionRequest{ID: identifier.String()})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Handle(context.Background(), ipc.Request{Operation: ipc.OperationRetry, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	response, valid := result.(ipc.AddResponse)
	if !valid {
		t.Fatalf("retry response has type %T", result)
	}
	retried, err := model.ParseDownloadID(response.ID)
	if err != nil {
		t.Fatal(err)
	}

	return retried
}

func (observer *controlledNetworkObserver) Observe(
	ctx context.Context,
	emit func(network.Snapshot) error,
) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case update := <-observer.updates:
			if err := emit(update.snapshot); err != nil {
				update.applied <- err
				return err
			}
			update.applied <- nil
		}
	}
}

func newControlledNetworkObserver() *controlledNetworkObserver {
	return &controlledNetworkObserver{updates: make(chan controlledNetworkUpdate, 4)}
}

func (observer *controlledNetworkObserver) send(t *testing.T, snapshot network.Snapshot) {
	t.Helper()
	update := controlledNetworkUpdate{snapshot: snapshot, applied: make(chan error, 1)}
	observer.updates <- update
	if err := <-update.applied; err != nil {
		t.Fatal(err)
	}
}

func newMeteredService(
	t *testing.T,
	store *storage.Store,
	engine *controlledDownloadEngine,
	observer *controlledNetworkObserver,
	pause bool,
	resume bool,
) *daemon.Service {
	t.Helper()
	service, err := daemon.NewServiceWithOptions(context.Background(), store, engine, daemon.ServiceOptions{
		MaximumConcurrentDownloads: 1,
		NetworkObserver:            observer,
		PauseOnMetered:             pause,
		ResumeAfterMetered:         resume,
	})
	if err != nil {
		t.Fatal(err)
	}

	return service
}

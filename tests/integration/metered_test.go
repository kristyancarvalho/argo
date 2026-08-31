package integration_test

import (
	"context"
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

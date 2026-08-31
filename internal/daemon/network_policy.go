package daemon

import (
	"errors"
	"time"

	"github.com/kristyancarvalho/argo/internal/model"
	"github.com/kristyancarvalho/argo/internal/network"
)

func (service *Service) runNetworkObserver() {
	defer service.waitGroup.Done()
	_ = service.networkObserver.Observe(service.ctx, service.applyNetworkState)
}

func (service *Service) applyNetworkState(snapshot network.Snapshot) error {
	if !service.pauseOnMetered {
		return nil
	}
	if snapshot.Metered.IsMetered() {
		return service.pauseForMeteredNetwork()
	}
	if snapshot.Metered == network.MeteredNo || snapshot.Metered == network.MeteredGuessNo {
		return service.resumeAfterMeteredNetwork()
	}

	return nil
}

func (service *Service) pauseForMeteredNetwork() error {
	downloads, err := service.store.Downloads(service.ctx)
	if err != nil {
		return err
	}
	for _, download := range downloads {
		switch download.Status {
		case model.StatusQueued, model.StatusResolving, model.StatusDownloading:
		case model.StatusPaused, model.StatusCompleted, model.StatusFailed, model.StatusCanceled:
			continue
		}
		err := service.store.UpdateDownloadStatus(
			service.ctx,
			download.ID,
			model.StatusPaused,
			time.Now().UTC(),
			"",
		)
		if err != nil {
			var transitionError model.InvalidTransitionError
			if errors.As(err, &transitionError) {
				continue
			}
			return err
		}
		service.rememberMeteredPause(download.ID)
		service.cancelActive(download.ID)
	}

	return nil
}

func (service *Service) resumeAfterMeteredNetwork() error {
	if !service.resumeAfterMetered {
		return nil
	}
	identifiers := service.meteredPauseIDs()
	for _, identifier := range identifiers {
		download, err := service.store.Download(service.ctx, identifier)
		if err != nil {
			return err
		}
		if download.Status != model.StatusPaused {
			service.forgetMeteredPause(identifier)
			continue
		}
		if err := service.store.UpdateDownloadStatus(
			service.ctx,
			identifier,
			model.StatusDownloading,
			time.Now().UTC(),
			"",
		); err != nil {
			return err
		}
		if err := service.enqueue(service.ctx, identifier, download.Priority); err != nil {
			return err
		}
		service.forgetMeteredPause(identifier)
	}

	return nil
}

func (service *Service) rememberMeteredPause(identifier model.DownloadID) {
	service.meteredMutex.Lock()
	defer service.meteredMutex.Unlock()
	service.meteredPaused[identifier] = struct{}{}
}

func (service *Service) forgetMeteredPause(identifier model.DownloadID) {
	service.meteredMutex.Lock()
	defer service.meteredMutex.Unlock()
	delete(service.meteredPaused, identifier)
}

func (service *Service) meteredPauseIDs() []model.DownloadID {
	service.meteredMutex.Lock()
	defer service.meteredMutex.Unlock()
	identifiers := make([]model.DownloadID, 0, len(service.meteredPaused))
	for identifier := range service.meteredPaused {
		identifiers = append(identifiers, identifier)
	}

	return identifiers
}

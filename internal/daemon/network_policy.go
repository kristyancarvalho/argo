package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kristyancarvalho/argo/internal/model"
	"github.com/kristyancarvalho/argo/internal/network"
)

const (
	networkRetryInitial = 100 * time.Millisecond
	networkRetryMaximum = 5 * time.Second
)

func (service *Service) runNetworkObserver() {
	defer service.waitGroup.Done()
	defer service.markNetworkReady()
	delay := networkRetryInitial
	for {
		observed := false
		err := service.networkObserver.Observe(service.ctx, func(snapshot network.Snapshot) error {
			observed = true
			return service.applyNetworkState(snapshot)
		})
		if service.ctx.Err() != nil {
			return
		}
		if err == nil {
			err = fmt.Errorf("network observation ended")
		}
		service.invalidateNetworkState(err)
		if reconnector, ok := service.networkObserver.(NetworkReconnector); ok {
			if reconnectError := reconnector.Reconnect(service.ctx); reconnectError != nil {
				service.invalidateNetworkState(errors.Join(err, reconnectError))
			}
		}
		if observed {
			delay = networkRetryInitial
		} else if delay < networkRetryMaximum {
			delay *= 2
			if delay > networkRetryMaximum {
				delay = networkRetryMaximum
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-service.ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

func (service *Service) applyNetworkState(snapshot network.Snapshot) error {
	service.networkPolicyMutex.Lock()
	defer service.networkPolicyMutex.Unlock()

	service.networkMutex.Lock()
	transitioned := !service.networkAvailable ||
		service.networkSnapshot.Connected != snapshot.Connected ||
		service.networkSnapshot.Interface != snapshot.Interface
	service.networkSnapshot = snapshot
	service.networkAvailable = true
	service.networkError = ""
	service.networkMutex.Unlock()
	service.markNetworkReady()
	if transitioned {
		_ = service.reconcileTrafficPolicy(service.ctx)
	}
	pauseOnMetered, _ := service.meteredPolicy()
	if !pauseOnMetered {
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

func (service *Service) invalidateNetworkState(observationError error) {
	service.networkPolicyMutex.Lock()
	defer service.networkPolicyMutex.Unlock()
	service.networkMutex.Lock()
	service.networkSnapshot = network.Snapshot{}
	service.networkAvailable = false
	service.networkError = observationError.Error()
	service.networkMutex.Unlock()
	service.markNetworkReady()
	_ = service.reconcileTrafficPolicy(service.ctx)
}

func (service *Service) markNetworkReady() {
	service.networkReadyOnce.Do(func() {
		close(service.networkReady)
	})
}

func (service *Service) reconcileMeteredAdmission(ctx context.Context) error {
	service.networkPolicyMutex.Lock()
	defer service.networkPolicyMutex.Unlock()
	if !service.transfersBlockedByMeteredNetwork() {
		return nil
	}

	return service.pauseForMeteredNetwork()
}

func (service *Service) pauseAdmissionOnMetered(
	ctx context.Context,
	identifier model.DownloadID,
) (bool, error) {
	service.networkPolicyMutex.Lock()
	defer service.networkPolicyMutex.Unlock()
	if !service.transfersBlockedByMeteredNetwork() {
		return false, nil
	}
	download, err := service.store.Download(ctx, identifier)
	if err != nil {
		return false, err
	}
	switch download.Status {
	case model.StatusQueued, model.StatusResolving, model.StatusDownloading, model.StatusVerifying:
		if err := service.store.UpdateDownloadStatus(
			ctx,
			identifier,
			model.StatusPaused,
			time.Now().UTC(),
			"",
		); err != nil {
			return false, err
		}
	case model.StatusPaused:
	case model.StatusCompleted, model.StatusFailed, model.StatusCanceled:
		return false, nil
	}
	service.rememberMeteredPause(identifier)
	service.cancelActive(identifier)

	return true, nil
}

func (service *Service) transfersBlockedByMeteredNetwork() bool {
	pauseOnMetered, _ := service.meteredPolicy()
	if !pauseOnMetered {
		return false
	}
	service.networkMutex.RLock()
	defer service.networkMutex.RUnlock()

	return service.networkAvailable && service.networkSnapshot.Metered.IsMetered()
}

func (service *Service) pauseForMeteredNetwork() error {
	downloads, err := service.store.Downloads(service.ctx)
	if err != nil {
		return err
	}
	for _, download := range downloads {
		switch download.Status {
		case model.StatusQueued, model.StatusResolving, model.StatusDownloading, model.StatusVerifying:
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
	_, resumeAfterMetered := service.meteredPolicy()
	if !resumeAfterMetered {
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

func (service *Service) meteredPolicy() (bool, bool) {
	service.profileMutex.RLock()
	defer service.profileMutex.RUnlock()

	return service.pauseOnMetered, service.resumeAfterMetered
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

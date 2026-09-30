package daemon

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kristyancarvalho/argo/internal/ipc"
	"github.com/kristyancarvalho/argo/internal/model"
	"github.com/kristyancarvalho/argo/internal/qos"
	"github.com/kristyancarvalho/argo/internal/telemetry"
)

func (service *Service) setTrafficPolicy(
	ctx context.Context,
	payload json.RawMessage,
) (ipc.PolicyResponse, error) {
	var request ipc.PolicyRequest
	if err := decodePayload(payload, &request); err != nil {
		return ipc.PolicyResponse{}, InvalidDownloadActionError{Action: "policy", Reason: err.Error()}
	}
	policy, err := qos.ParsePolicy(request.Policy)
	if err != nil {
		return ipc.PolicyResponse{}, InvalidDownloadActionError{Action: "policy", Reason: err.Error()}
	}
	if policy == qos.PolicyLatency && service.currentLatencyPolicy(qos.PolicyLatency) == nil {
		return ipc.PolicyResponse{}, PolicyUnavailableError{Reason: "latency policy is not configured"}
	}
	backgroundPolicy := service.currentLatencyPolicy(qos.PolicyBackground)
	if policy == qos.PolicyBackground && backgroundPolicy == nil {
		return ipc.PolicyResponse{}, PolicyUnavailableError{Reason: "background policy is not configured"}
	}
	if policy == qos.PolicyBackground {
		backgroundPolicy.Reset()
	}
	service.profileMutex.Lock()
	previous := service.trafficPolicy
	service.trafficPolicy = policy
	service.profileMutex.Unlock()
	if err := service.reconcileTrafficPolicy(ctx); err != nil {
		service.profileMutex.Lock()
		service.trafficPolicy = previous
		service.profileMutex.Unlock()
		_ = service.reconcileTrafficPolicy(context.WithoutCancel(ctx))
		service.setTrafficError(err)
		return ipc.PolicyResponse{}, PolicyUnavailableError{Reason: err.Error()}
	}
	applied := false
	if service.trafficController != nil {
		_, applied = service.trafficController.Current()
	}

	return ipc.PolicyResponse{Policy: string(policy), Applied: applied}, nil
}

func (service *Service) reconcileTrafficPolicy(ctx context.Context) (resultErr error) {
	defer func() {
		service.setTrafficError(resultErr)
	}()
	service.profileMutex.RLock()
	policy := service.trafficPolicy
	latencyPolicy := service.latencyPolicy
	backgroundPolicy := service.backgroundPolicy
	service.profileMutex.RUnlock()
	downloads, err := service.store.Downloads(ctx)
	if err != nil {
		return err
	}
	active := 0
	for _, download := range downloads {
		switch download.Status {
		case model.StatusQueued, model.StatusResolving, model.StatusDownloading, model.StatusVerifying:
			active++
		case model.StatusPaused, model.StatusCompleted, model.StatusFailed, model.StatusCanceled:
		}
	}
	service.networkMutex.RLock()
	snapshot := service.networkSnapshot
	available := service.networkAvailable
	service.networkMutex.RUnlock()
	desired := qos.DesiredState{Policy: qos.PolicyOff}
	if available && snapshot.Connected && snapshot.Interface != "" {
		environment := qos.PolicyEnvironment{
			Interface:             snapshot.Interface,
			LinkRateBitsPerSecond: service.trafficLinkRate,
			Cgroup:                service.trafficCgroup,
			ActiveDownloads:       active,
		}
		adaptivePolicy := latencyPolicy
		if policy == qos.PolicyBackground {
			adaptivePolicy = backgroundPolicy
		}
		if (policy == qos.PolicyLatency || policy == qos.PolicyBackground) && adaptivePolicy != nil {
			desired, err = qos.MapAdaptivePolicyFor(
				policy,
				environment,
				adaptivePolicy.Current().RateBitsPerSecond,
			)
		} else {
			desired, err = qos.MapPolicy(policy, environment)
		}
		if err != nil {
			return err
		}
	}
	if service.trafficController == nil {
		if !desired.Enabled {
			return nil
		}
		return fmt.Errorf("QoS helper is unavailable")
	}

	return service.trafficController.Reconcile(ctx, desired)
}

func (service *Service) setTrafficError(err error) {
	service.trafficErrorMutex.Lock()
	defer service.trafficErrorMutex.Unlock()
	service.trafficError = ""
	if err != nil {
		service.trafficError = err.Error()
	}
}

func (service *Service) runTelemetryObserver() {
	defer service.waitGroup.Done()
	_ = service.telemetryObserver.Observe(service.ctx, func(snapshot telemetry.Snapshot) error {
		service.profileMutex.RLock()
		activePolicy := service.trafficPolicy
		service.profileMutex.RUnlock()
		latencyPolicy := service.currentLatencyPolicy(activePolicy)
		if latencyPolicy == nil && activePolicy != qos.PolicyBackground {
			latencyPolicy = service.currentLatencyPolicy(qos.PolicyLatency)
		}
		if latencyPolicy == nil {
			return nil
		}
		if activePolicy == qos.PolicyBackground && snapshot.ActiveTransfers == 0 {
			latencyPolicy.ObserveBaseline(snapshot)
			latencyPolicy.Reset()
			return nil
		}
		latencyPolicy.Observe(snapshot)
		active := activePolicy == qos.PolicyLatency || activePolicy == qos.PolicyBackground
		if active {
			_ = service.reconcileTrafficPolicy(service.ctx)
		}

		return nil
	})
}

func (service *Service) currentLatencyPolicy(policy qos.Policy) *qos.LatencyPolicy {
	service.profileMutex.RLock()
	defer service.profileMutex.RUnlock()
	if policy == qos.PolicyBackground {
		return service.backgroundPolicy
	}
	return service.latencyPolicy
}

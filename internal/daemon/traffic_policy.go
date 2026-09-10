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
		return ipc.PolicyResponse{}, err
	}
	if policy == qos.PolicyLatency && service.currentLatencyPolicy() == nil {
		return ipc.PolicyResponse{}, fmt.Errorf("latency policy is not configured")
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
		return ipc.PolicyResponse{}, err
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
		if policy == qos.PolicyLatency && latencyPolicy != nil {
			desired, err = qos.MapAdaptivePolicy(
				environment,
				latencyPolicy.Current().RateBitsPerSecond,
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
		latencyPolicy := service.currentLatencyPolicy()
		if latencyPolicy == nil {
			return nil
		}
		latencyPolicy.Observe(snapshot)
		service.profileMutex.RLock()
		active := service.trafficPolicy == qos.PolicyLatency
		service.profileMutex.RUnlock()
		if active {
			_ = service.reconcileTrafficPolicy(service.ctx)
		}

		return nil
	})
}

func (service *Service) currentLatencyPolicy() *qos.LatencyPolicy {
	service.profileMutex.RLock()
	defer service.profileMutex.RUnlock()

	return service.latencyPolicy
}

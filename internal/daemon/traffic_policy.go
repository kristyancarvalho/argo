package daemon

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kristyancarvalho/argo/internal/ipc"
	"github.com/kristyancarvalho/argo/internal/model"
	"github.com/kristyancarvalho/argo/internal/qos"
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
	if policy == qos.PolicyLatency {
		return ipc.PolicyResponse{}, fmt.Errorf("latency policy is not available in this release")
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
		return ipc.PolicyResponse{}, err
	}
	applied := false
	if service.trafficController != nil {
		_, applied = service.trafficController.Current()
	}

	return ipc.PolicyResponse{Policy: string(policy), Applied: applied}, nil
}

func (service *Service) reconcileTrafficPolicy(ctx context.Context) error {
	service.profileMutex.RLock()
	policy := service.trafficPolicy
	service.profileMutex.RUnlock()
	downloads, err := service.store.Downloads(ctx)
	if err != nil {
		return err
	}
	active := 0
	for _, download := range downloads {
		switch download.Status {
		case model.StatusQueued, model.StatusResolving, model.StatusDownloading:
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
		desired, err = qos.MapPolicy(policy, qos.PolicyEnvironment{
			Interface:             snapshot.Interface,
			LinkRateBitsPerSecond: service.trafficLinkRate,
			CgroupID:              service.trafficCgroupID,
			ActiveDownloads:       active,
		})
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

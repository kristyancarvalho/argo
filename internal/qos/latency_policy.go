package qos

import (
	"fmt"
	"sync"
	"time"

	"github.com/kristyancarvalho/argo/internal/telemetry"
)

type LatencyPolicy struct {
	mutex      sync.Mutex
	baseline   *telemetry.BaselineEstimator
	controller *AdaptiveController
}

func NewLatencyPolicy(
	baseline *telemetry.BaselineEstimator,
	controller *AdaptiveController,
) (*LatencyPolicy, error) {
	if baseline == nil || controller == nil {
		return nil, fmt.Errorf("latency policy requires baseline and adaptive controllers")
	}

	return &LatencyPolicy{baseline: baseline, controller: controller}, nil
}

func (policy *LatencyPolicy) Observe(snapshot telemetry.Snapshot) AdaptiveState {
	policy.mutex.Lock()
	defer policy.mutex.Unlock()
	policy.baseline.Observe(snapshot)
	now := snapshot.LatencySampledAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	baseline, available := policy.baseline.Current(now)
	return policy.controller.Update(AdaptiveSignal{
		Latency:   snapshot.Latency,
		Baseline:  baseline,
		Available: snapshot.LatencyAvailable && available,
	})
}

func (policy *LatencyPolicy) Current() AdaptiveState {
	policy.mutex.Lock()
	defer policy.mutex.Unlock()

	return policy.controller.Current()
}

func MapAdaptivePolicy(environment PolicyEnvironment, rate uint64) (DesiredState, error) {
	if environment.ActiveDownloads == 0 {
		return DesiredState{Policy: PolicyOff}, nil
	}
	if environment.ActiveDownloads < 0 {
		return DesiredState{}, fmt.Errorf("active download count must not be negative")
	}
	if err := validateInterface(environment.Interface); err != nil {
		return DesiredState{}, err
	}
	if environment.LinkRateBitsPerSecond < 2 || environment.CgroupID == 0 {
		return DesiredState{}, fmt.Errorf("policy requires configured link rate and Argo cgroup")
	}
	state := DesiredState{
		Enabled:               true,
		Policy:                PolicyLatency,
		Interface:             environment.Interface,
		LinkRateBitsPerSecond: environment.LinkRateBitsPerSecond,
		ArgoRateBitsPerSecond: rate,
		CgroupID:              environment.CgroupID,
	}
	if err := state.Validate(); err != nil {
		return DesiredState{}, err
	}

	return state, nil
}

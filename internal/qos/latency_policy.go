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
	lastSample telemetry.Snapshot
	lastState  AdaptiveState
}

type LatencyDiagnostics struct {
	State             AdaptiveState
	MeasuredLatency   time.Duration
	LatencyAvailable  bool
	BaselineLatency   time.Duration
	BaselineAvailable bool
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
	policy.lastSample = snapshot
	now := snapshot.LatencySampledAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	baseline, available := policy.baseline.Current(now)
	policy.lastState = policy.controller.Update(AdaptiveSignal{
		Latency:   snapshot.Latency,
		Baseline:  baseline,
		Available: snapshot.LatencyAvailable && available,
	})

	return policy.lastState
}

func (policy *LatencyPolicy) Diagnostics(now time.Time) LatencyDiagnostics {
	policy.mutex.Lock()
	defer policy.mutex.Unlock()
	baseline, baselineAvailable := policy.baseline.Current(now)
	state := policy.lastState
	if state.RateBitsPerSecond == 0 {
		state = policy.controller.Current()
	}

	return LatencyDiagnostics{
		State:             state,
		MeasuredLatency:   policy.lastSample.Latency,
		LatencyAvailable:  policy.lastSample.LatencyAvailable,
		BaselineLatency:   baseline,
		BaselineAvailable: baselineAvailable,
	}
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
	if environment.LinkRateBitsPerSecond < 2 || environment.Cgroup.Validate() != nil {
		return DesiredState{}, fmt.Errorf("policy requires configured link rate and Argo cgroup")
	}
	state := DesiredState{
		Enabled:               true,
		Policy:                PolicyLatency,
		Interface:             environment.Interface,
		LinkRateBitsPerSecond: environment.LinkRateBitsPerSecond,
		ArgoRateBitsPerSecond: rate,
		Cgroup:                environment.Cgroup,
	}
	if err := state.Validate(); err != nil {
		return DesiredState{}, err
	}

	return state, nil
}

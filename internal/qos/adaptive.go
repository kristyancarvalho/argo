package qos

import (
	"fmt"
	"sync"
	"time"
)

type AdaptiveOptions struct {
	MinimumRateBitsPerSecond  uint64
	MaximumRateBitsPerSecond  uint64
	InitialRateBitsPerSecond  uint64
	IncreaseStepBitsPerSecond uint64
	DecreaseStepBitsPerSecond uint64
	AcceptableLatencyIncrease time.Duration
	RequiredSamples           int
}

type AdaptiveSignal struct {
	Latency   time.Duration
	Baseline  time.Duration
	Available bool
}

type AdaptiveReason string

const (
	AdaptiveStable           AdaptiveReason = "stable"
	AdaptiveLatencyRising    AdaptiveReason = "latency-rising"
	AdaptiveLatencyFalling   AdaptiveReason = "latency-falling"
	AdaptiveTelemetryMissing AdaptiveReason = "telemetry-missing"
)

type AdaptiveState struct {
	RateBitsPerSecond uint64
	Changed           bool
	Reason            AdaptiveReason
}

type AdaptiveController struct {
	mutex          sync.Mutex
	options        AdaptiveOptions
	rate           uint64
	risingSamples  int
	fallingSamples int
	missingSamples int
}

func NewAdaptiveController(options AdaptiveOptions) (*AdaptiveController, error) {
	if options.MinimumRateBitsPerSecond == 0 ||
		options.MaximumRateBitsPerSecond <= options.MinimumRateBitsPerSecond {
		return nil, fmt.Errorf("adaptive rate bounds must be positive and increasing")
	}
	if options.InitialRateBitsPerSecond < options.MinimumRateBitsPerSecond ||
		options.InitialRateBitsPerSecond > options.MaximumRateBitsPerSecond {
		return nil, fmt.Errorf("adaptive initial rate must be within configured bounds")
	}
	if options.IncreaseStepBitsPerSecond == 0 || options.DecreaseStepBitsPerSecond == 0 {
		return nil, fmt.Errorf("adaptive rate steps must be positive")
	}
	if options.AcceptableLatencyIncrease <= 0 {
		return nil, fmt.Errorf("acceptable latency increase must be positive")
	}
	if options.RequiredSamples < 2 {
		return nil, fmt.Errorf("adaptive controller requires at least two consecutive samples")
	}

	return &AdaptiveController{options: options, rate: options.InitialRateBitsPerSecond}, nil
}

func (controller *AdaptiveController) Update(signal AdaptiveSignal) AdaptiveState {
	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	if !signal.Available || signal.Latency <= 0 || signal.Baseline <= 0 {
		controller.risingSamples = 0
		controller.fallingSamples = 0
		controller.missingSamples++
		if controller.missingSamples < controller.options.RequiredSamples {
			return controller.state(false, AdaptiveTelemetryMissing)
		}
		controller.missingSamples = 0
		return controller.decrease(AdaptiveTelemetryMissing)
	}
	controller.missingSamples = 0
	delta := signal.Latency - signal.Baseline
	if delta > controller.options.AcceptableLatencyIncrease {
		controller.risingSamples++
		controller.fallingSamples = 0
		if controller.risingSamples < controller.options.RequiredSamples {
			return controller.state(false, AdaptiveStable)
		}
		controller.risingSamples = 0
		return controller.decrease(AdaptiveLatencyRising)
	}
	if delta < controller.options.AcceptableLatencyIncrease/2 {
		controller.fallingSamples++
		controller.risingSamples = 0
		if controller.fallingSamples < controller.options.RequiredSamples {
			return controller.state(false, AdaptiveStable)
		}
		controller.fallingSamples = 0
		return controller.increase()
	}
	controller.risingSamples = 0
	controller.fallingSamples = 0

	return controller.state(false, AdaptiveStable)
}

func (controller *AdaptiveController) Current() AdaptiveState {
	controller.mutex.Lock()
	defer controller.mutex.Unlock()

	return controller.state(false, AdaptiveStable)
}

func (controller *AdaptiveController) Reset() AdaptiveState {
	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	changed := controller.rate != controller.options.InitialRateBitsPerSecond
	controller.rate = controller.options.InitialRateBitsPerSecond
	controller.risingSamples = 0
	controller.fallingSamples = 0
	controller.missingSamples = 0

	return controller.state(changed, AdaptiveStable)
}

func (controller *AdaptiveController) increase() AdaptiveState {
	remaining := controller.options.MaximumRateBitsPerSecond - controller.rate
	if remaining == 0 {
		return controller.state(false, AdaptiveLatencyFalling)
	}
	step := controller.options.IncreaseStepBitsPerSecond
	if step > remaining {
		step = remaining
	}
	controller.rate += step

	return controller.state(true, AdaptiveLatencyFalling)
}

func (controller *AdaptiveController) decrease(reason AdaptiveReason) AdaptiveState {
	remaining := controller.rate - controller.options.MinimumRateBitsPerSecond
	if remaining == 0 {
		return controller.state(false, reason)
	}
	step := controller.options.DecreaseStepBitsPerSecond
	if step > remaining {
		step = remaining
	}
	controller.rate -= step

	return controller.state(true, reason)
}

func (controller *AdaptiveController) state(changed bool, reason AdaptiveReason) AdaptiveState {
	return AdaptiveState{RateBitsPerSecond: controller.rate, Changed: changed, Reason: reason}
}

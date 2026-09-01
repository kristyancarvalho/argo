package unit_test

import (
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/qos"
)

func TestAdaptiveControllerRespondsGraduallyToRisingLatency(t *testing.T) {
	controller := newTestAdaptiveController(t)
	signal := qos.AdaptiveSignal{
		Latency:   50 * time.Millisecond,
		Baseline:  20 * time.Millisecond,
		Available: true,
	}
	for range 2 {
		state := controller.Update(signal)
		if state.Changed || state.RateBitsPerSecond != 60_000_000 {
			t.Fatalf("controller changed before confirmation: %+v", state)
		}
	}
	state := controller.Update(signal)
	if !state.Changed || state.RateBitsPerSecond != 50_000_000 ||
		state.Reason != qos.AdaptiveLatencyRising {
		t.Fatalf("unexpected rising-latency state: %+v", state)
	}
}

func TestAdaptiveControllerRespondsGraduallyToFallingLatency(t *testing.T) {
	controller := newTestAdaptiveController(t)
	signal := qos.AdaptiveSignal{
		Latency:   22 * time.Millisecond,
		Baseline:  20 * time.Millisecond,
		Available: true,
	}
	for range 2 {
		if state := controller.Update(signal); state.Changed {
			t.Fatalf("controller changed before confirmation: %+v", state)
		}
	}
	state := controller.Update(signal)
	if !state.Changed || state.RateBitsPerSecond != 65_000_000 ||
		state.Reason != qos.AdaptiveLatencyFalling {
		t.Fatalf("unexpected falling-latency state: %+v", state)
	}
}

func TestAdaptiveControllerFallsBackSafelyWithoutTelemetry(t *testing.T) {
	controller := newTestAdaptiveController(t)
	for range 2 {
		state := controller.Update(qos.AdaptiveSignal{})
		if state.Changed || state.Reason != qos.AdaptiveTelemetryMissing {
			t.Fatalf("unexpected early missing state: %+v", state)
		}
	}
	state := controller.Update(qos.AdaptiveSignal{})
	if !state.Changed || state.RateBitsPerSecond != 50_000_000 ||
		state.Reason != qos.AdaptiveTelemetryMissing {
		t.Fatalf("unexpected missing-telemetry fallback: %+v", state)
	}
}

func TestAdaptiveControllerEnforcesRateBounds(t *testing.T) {
	controller := newTestAdaptiveController(t)
	high := qos.AdaptiveSignal{Latency: 50 * time.Millisecond, Baseline: 20 * time.Millisecond, Available: true}
	low := qos.AdaptiveSignal{Latency: 20 * time.Millisecond, Baseline: 20 * time.Millisecond, Available: true}
	for range 30 {
		controller.Update(high)
	}
	if state := controller.Current(); state.RateBitsPerSecond != 20_000_000 {
		t.Fatalf("minimum bound is %+v", state)
	}
	for range 60 {
		controller.Update(low)
	}
	if state := controller.Current(); state.RateBitsPerSecond != 80_000_000 {
		t.Fatalf("maximum bound is %+v", state)
	}
}

func TestAdaptiveControllerResistsOscillation(t *testing.T) {
	controller := newTestAdaptiveController(t)
	high := qos.AdaptiveSignal{Latency: 50 * time.Millisecond, Baseline: 20 * time.Millisecond, Available: true}
	low := qos.AdaptiveSignal{Latency: 20 * time.Millisecond, Baseline: 20 * time.Millisecond, Available: true}
	for range 20 {
		controller.Update(high)
		controller.Update(low)
	}
	if state := controller.Current(); state.RateBitsPerSecond != 60_000_000 {
		t.Fatalf("alternating samples changed controller state: %+v", state)
	}
	middle := qos.AdaptiveSignal{Latency: 27 * time.Millisecond, Baseline: 20 * time.Millisecond, Available: true}
	for range 10 {
		if state := controller.Update(middle); state.Changed {
			t.Fatalf("dead-band sample changed controller state: %+v", state)
		}
	}
}

func TestAdaptiveControllerResetAndValidation(t *testing.T) {
	controller := newTestAdaptiveController(t)
	high := qos.AdaptiveSignal{Latency: 50 * time.Millisecond, Baseline: 20 * time.Millisecond, Available: true}
	for range 3 {
		controller.Update(high)
	}
	if state := controller.Reset(); !state.Changed || state.RateBitsPerSecond != 60_000_000 {
		t.Fatalf("unexpected reset state: %+v", state)
	}
	invalid := []qos.AdaptiveOptions{
		{},
		{MinimumRateBitsPerSecond: 10, MaximumRateBitsPerSecond: 10},
		{MinimumRateBitsPerSecond: 10, MaximumRateBitsPerSecond: 20, InitialRateBitsPerSecond: 5},
		{MinimumRateBitsPerSecond: 10, MaximumRateBitsPerSecond: 20, InitialRateBitsPerSecond: 10},
	}
	for _, options := range invalid {
		if _, err := qos.NewAdaptiveController(options); err == nil {
			t.Fatalf("invalid options %+v were accepted", options)
		}
	}
}

func newTestAdaptiveController(t *testing.T) *qos.AdaptiveController {
	t.Helper()
	controller, err := qos.NewAdaptiveController(qos.AdaptiveOptions{
		MinimumRateBitsPerSecond:  20_000_000,
		MaximumRateBitsPerSecond:  80_000_000,
		InitialRateBitsPerSecond:  60_000_000,
		IncreaseStepBitsPerSecond: 5_000_000,
		DecreaseStepBitsPerSecond: 10_000_000,
		AcceptableLatencyIncrease: 10 * time.Millisecond,
		RequiredSamples:           3,
	})
	if err != nil {
		t.Fatal(err)
	}

	return controller
}

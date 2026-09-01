package unit_test

import (
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/telemetry"
)

func TestLatencyBaselineRequiresStableSamples(t *testing.T) {
	estimator := newTestBaselineEstimator(t, telemetry.BaselineOptions{
		MinimumSamples:   3,
		WindowSize:       5,
		MaximumDeviation: 2 * time.Millisecond,
		MaximumAge:       time.Minute,
	})
	started := time.Now().UTC()
	estimator.Observe(latencySnapshot(20*time.Millisecond, started, 0))
	estimator.Observe(latencySnapshot(21*time.Millisecond, started.Add(time.Second), 0))
	if baseline, available := estimator.Current(started.Add(time.Second)); available || baseline != 0 {
		t.Fatalf("baseline trusted fewer than the required samples: %s, %t", baseline, available)
	}
	estimator.Observe(latencySnapshot(19*time.Millisecond, started.Add(2*time.Second), 0))
	baseline, available := estimator.Current(started.Add(2 * time.Second))
	if !available || baseline != 20*time.Millisecond {
		t.Fatalf("stable baseline is %s, available %t", baseline, available)
	}
}

func TestLatencyBaselineIgnoresLoadedSamplesAndOutliers(t *testing.T) {
	estimator := newTestBaselineEstimator(t, telemetry.BaselineOptions{
		MinimumSamples:   3,
		WindowSize:       5,
		MaximumDeviation: 2 * time.Millisecond,
		MaximumAge:       time.Minute,
	})
	started := time.Now().UTC()
	loaded := latencySnapshot(20*time.Millisecond, started, 0)
	loaded.ActiveTransfers = 1
	estimator.Observe(loaded)
	for index, latency := range []time.Duration{
		20 * time.Millisecond,
		100 * time.Millisecond,
		21 * time.Millisecond,
		19 * time.Millisecond,
	} {
		estimator.Observe(latencySnapshot(latency, started.Add(time.Duration(index+1)*time.Second), 0))
	}
	baseline, available := estimator.Current(started.Add(5 * time.Second))
	if !available || baseline != 20*time.Millisecond {
		t.Fatalf("outlier changed baseline to %s, available %t", baseline, available)
	}
}

func TestLatencyBaselineExpiresAndRequiresFreshSamples(t *testing.T) {
	estimator := newTestBaselineEstimator(t, telemetry.BaselineOptions{
		MinimumSamples:   3,
		WindowSize:       3,
		MaximumDeviation: time.Millisecond,
		MaximumAge:       10 * time.Second,
	})
	started := time.Now().UTC()
	for index := range 3 {
		estimator.Observe(latencySnapshot(20*time.Millisecond, started.Add(time.Duration(index)*time.Second), 0))
	}
	if _, available := estimator.Current(started.Add(13 * time.Second)); available {
		t.Fatal("stale baseline remained available")
	}
	estimator.Observe(latencySnapshot(20*time.Millisecond, started.Add(14*time.Second), 0))
	if _, available := estimator.Current(started.Add(14 * time.Second)); available {
		t.Fatal("one fresh sample renewed a stale baseline")
	}
	for _, offset := range []time.Duration{15 * time.Second, 16 * time.Second} {
		estimator.Observe(latencySnapshot(20*time.Millisecond, started.Add(offset), 0))
	}
	if baseline, available := estimator.Current(started.Add(16 * time.Second)); !available || baseline != 20*time.Millisecond {
		t.Fatalf("fresh baseline is %s, available %t", baseline, available)
	}
}

func TestManualLatencyBaselineOverridesSamplesAndExpiration(t *testing.T) {
	estimator := newTestBaselineEstimator(t, telemetry.BaselineOptions{Manual: 35 * time.Millisecond})
	started := time.Now().UTC()
	estimator.Observe(latencySnapshot(10*time.Millisecond, started, 0))
	estimator.Reset()
	baseline, available := estimator.Current(started.Add(24 * time.Hour))
	if !available || baseline != 35*time.Millisecond {
		t.Fatalf("manual baseline is %s, available %t", baseline, available)
	}
}

func TestLatencyBaselineRejectsInvalidOptions(t *testing.T) {
	tests := []telemetry.BaselineOptions{
		{MinimumSamples: 1},
		{MinimumSamples: 3, WindowSize: 2},
		{MaximumDeviation: -time.Second},
		{MaximumAge: -time.Second},
		{Manual: -time.Second},
	}
	for _, options := range tests {
		if _, err := telemetry.NewBaselineEstimator(options); err == nil {
			t.Fatalf("options %+v were accepted", options)
		}
	}
}

func newTestBaselineEstimator(
	t *testing.T,
	options telemetry.BaselineOptions,
) *telemetry.BaselineEstimator {
	t.Helper()
	estimator, err := telemetry.NewBaselineEstimator(options)
	if err != nil {
		t.Fatal(err)
	}

	return estimator
}

func latencySnapshot(latency time.Duration, at time.Time, throughput int64) telemetry.Snapshot {
	return telemetry.Snapshot{
		AggregateBytesPerSecond: throughput,
		Latency:                 latency,
		LatencyAvailable:        true,
		LatencySampledAt:        at,
	}
}

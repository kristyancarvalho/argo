package telemetry

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

const (
	defaultMinimumBaselineSamples = 5
	defaultBaselineWindowSize     = 9
	defaultMaximumDeviation       = 5 * time.Millisecond
	defaultMaximumBaselineAge     = 5 * time.Minute
)

type BaselineOptions struct {
	MinimumSamples   int
	WindowSize       int
	MaximumDeviation time.Duration
	MaximumAge       time.Duration
	Manual           time.Duration
}

type latencyObservation struct {
	latency time.Duration
	at      time.Time
}

type BaselineEstimator struct {
	mutex        sync.Mutex
	options      BaselineOptions
	observations []latencyObservation
	baseline     time.Duration
	updatedAt    time.Time
	lastSampleAt time.Time
}

func NewBaselineEstimator(options BaselineOptions) (*BaselineEstimator, error) {
	if options.MinimumSamples == 0 {
		options.MinimumSamples = defaultMinimumBaselineSamples
	}
	if options.WindowSize == 0 {
		options.WindowSize = defaultBaselineWindowSize
	}
	if options.MaximumDeviation == 0 {
		options.MaximumDeviation = defaultMaximumDeviation
	}
	if options.MaximumAge == 0 {
		options.MaximumAge = defaultMaximumBaselineAge
	}
	if options.MinimumSamples < 2 {
		return nil, fmt.Errorf("latency baseline requires at least two samples")
	}
	if options.WindowSize < options.MinimumSamples {
		return nil, fmt.Errorf("latency baseline window must contain the minimum sample count")
	}
	if options.MaximumDeviation <= 0 || options.MaximumAge <= 0 {
		return nil, fmt.Errorf("latency baseline deviation and age must be positive")
	}
	if options.Manual < 0 {
		return nil, fmt.Errorf("manual latency baseline must not be negative")
	}

	return &BaselineEstimator{options: options}, nil
}

func (estimator *BaselineEstimator) Observe(snapshot Snapshot) {
	if estimator.options.Manual > 0 || !snapshot.LatencyAvailable || snapshot.Latency <= 0 ||
		snapshot.LatencySampledAt.IsZero() || snapshot.ActiveTransfers != 0 ||
		snapshot.AggregateBytesPerSecond != 0 {
		return
	}
	estimator.mutex.Lock()
	defer estimator.mutex.Unlock()
	if !estimator.lastSampleAt.IsZero() && !snapshot.LatencySampledAt.After(estimator.lastSampleAt) {
		return
	}
	estimator.lastSampleAt = snapshot.LatencySampledAt
	cutoff := snapshot.LatencySampledAt.Add(-estimator.options.MaximumAge)
	retained := estimator.observations[:0]
	for _, observation := range estimator.observations {
		if !observation.at.Before(cutoff) {
			retained = append(retained, observation)
		}
	}
	estimator.observations = retained
	estimator.observations = append(estimator.observations, latencyObservation{
		latency: snapshot.Latency,
		at:      snapshot.LatencySampledAt,
	})
	if len(estimator.observations) > estimator.options.WindowSize {
		estimator.observations = estimator.observations[len(estimator.observations)-estimator.options.WindowSize:]
	}
	if len(estimator.observations) < estimator.options.MinimumSamples {
		return
	}
	latencies := make([]time.Duration, 0, len(estimator.observations))
	for _, observation := range estimator.observations {
		latencies = append(latencies, observation.latency)
	}
	center := median(latencies)
	stable := make([]latencyObservation, 0, len(estimator.observations))
	for _, observation := range estimator.observations {
		deviation := observation.latency - center
		if deviation < 0 {
			deviation = -deviation
		}
		if deviation <= estimator.options.MaximumDeviation {
			stable = append(stable, observation)
		}
	}
	if len(stable) < estimator.options.MinimumSamples {
		return
	}
	latencies = latencies[:0]
	var newest time.Time
	for _, observation := range stable {
		latencies = append(latencies, observation.latency)
		if observation.at.After(newest) {
			newest = observation.at
		}
	}
	estimator.baseline = median(latencies)
	estimator.updatedAt = newest
}

func (estimator *BaselineEstimator) Current(now time.Time) (time.Duration, bool) {
	if estimator.options.Manual > 0 {
		return estimator.options.Manual, true
	}
	estimator.mutex.Lock()
	defer estimator.mutex.Unlock()
	if estimator.baseline <= 0 || now.Before(estimator.updatedAt) ||
		now.Sub(estimator.updatedAt) > estimator.options.MaximumAge {
		return 0, false
	}

	return estimator.baseline, true
}

func (estimator *BaselineEstimator) Reset() {
	estimator.mutex.Lock()
	defer estimator.mutex.Unlock()
	estimator.observations = nil
	estimator.baseline = 0
	estimator.updatedAt = time.Time{}
	estimator.lastSampleAt = time.Time{}
}

func median(values []time.Duration) time.Duration {
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(left, right int) bool {
		return sorted[left] < sorted[right]
	})
	middle := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[middle]
	}

	return sorted[middle-1]/2 + sorted[middle]/2 +
		(sorted[middle-1]%2+sorted[middle]%2)/2
}

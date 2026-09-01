package telemetry

import (
	"math"
	"sync"
	"time"

	"github.com/kristyancarvalho/argo/internal/model"
)

type TransferObservation struct {
	ID              model.DownloadID
	DownloadedBytes int64
	ObservedAt      time.Time
}

type TransferSample struct {
	ID             model.DownloadID
	BytesPerSecond int64
	SampledAt      time.Time
}

type Snapshot struct {
	Transfers               map[model.DownloadID]TransferSample
	ActiveTransfers         int
	AggregateBytesPerSecond int64
	Latency                 time.Duration
	LatencyAvailable        bool
	LatencySampledAt        time.Time
	SampledAt               time.Time
}

type previousObservation struct {
	bytes int64
	at    time.Time
}

type Sampler struct {
	mutex                 sync.Mutex
	previous              map[model.DownloadID]previousObservation
	maximumBytesPerSecond int64
}

func NewSampler(maximumBytesPerSecond int64) *Sampler {
	return &Sampler{
		previous:              make(map[model.DownloadID]previousObservation),
		maximumBytesPerSecond: maximumBytesPerSecond,
	}
}

func (sampler *Sampler) Sample(observations []TransferObservation) Snapshot {
	sampler.mutex.Lock()
	defer sampler.mutex.Unlock()
	transfers := make(map[model.DownloadID]TransferSample)
	seen := make(map[model.DownloadID]struct{}, len(observations))
	var sampledAt time.Time
	for _, observation := range observations {
		if observation.ID == "" || observation.DownloadedBytes < 0 || observation.ObservedAt.IsZero() {
			continue
		}
		if _, duplicate := seen[observation.ID]; duplicate {
			continue
		}
		seen[observation.ID] = struct{}{}
		if observation.ObservedAt.After(sampledAt) {
			sampledAt = observation.ObservedAt
		}
		previous, exists := sampler.previous[observation.ID]
		if exists && !observation.ObservedAt.After(previous.at) {
			continue
		}
		sampler.previous[observation.ID] = previousObservation{
			bytes: observation.DownloadedBytes,
			at:    observation.ObservedAt,
		}
		if !exists || observation.DownloadedBytes < previous.bytes {
			continue
		}
		delta := observation.DownloadedBytes - previous.bytes
		elapsed := observation.ObservedAt.Sub(previous.at)
		rate := bytesPerSecond(delta, elapsed)
		if rate < 0 || sampler.maximumBytesPerSecond > 0 && rate > sampler.maximumBytesPerSecond {
			continue
		}
		transfers[observation.ID] = TransferSample{
			ID:             observation.ID,
			BytesPerSecond: rate,
			SampledAt:      observation.ObservedAt,
		}
	}
	for identifier := range sampler.previous {
		if _, exists := seen[identifier]; !exists {
			delete(sampler.previous, identifier)
		}
	}
	aggregate := int64(0)
	for _, sample := range transfers {
		if sample.BytesPerSecond > math.MaxInt64-aggregate {
			aggregate = math.MaxInt64
			break
		}
		aggregate += sample.BytesPerSecond
	}

	return Snapshot{
		Transfers:               transfers,
		ActiveTransfers:         len(seen),
		AggregateBytesPerSecond: aggregate,
		SampledAt:               sampledAt,
	}
}

func (sampler *Sampler) Reset() {
	sampler.mutex.Lock()
	defer sampler.mutex.Unlock()
	clear(sampler.previous)
}

func bytesPerSecond(bytes int64, elapsed time.Duration) int64 {
	if bytes < 0 || elapsed <= 0 {
		return -1
	}
	seconds := elapsed.Seconds()
	if seconds == 0 || float64(bytes) > float64(math.MaxInt64)*seconds {
		return math.MaxInt64
	}

	return int64(float64(bytes) / seconds)
}

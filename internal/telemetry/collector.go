package telemetry

import (
	"context"
	"time"
)

type LatencyProbe interface {
	Measure(context.Context) (time.Duration, error)
}

type Collector struct {
	sampler *Sampler
	probe   LatencyProbe
}

func NewCollector(sampler *Sampler, probe LatencyProbe) *Collector {
	if sampler == nil {
		sampler = NewSampler(0)
	}

	return &Collector{sampler: sampler, probe: probe}
}

func (collector *Collector) Sample(
	ctx context.Context,
	observations []TransferObservation,
) Snapshot {
	snapshot := collector.sampler.Sample(observations)
	if collector.probe == nil {
		return snapshot
	}
	latency, err := collector.probe.Measure(ctx)
	if err != nil || latency < 0 {
		return snapshot
	}
	snapshot.Latency = latency
	snapshot.LatencyAvailable = true

	return snapshot
}

func (collector *Collector) Reset() {
	collector.sampler.Reset()
}

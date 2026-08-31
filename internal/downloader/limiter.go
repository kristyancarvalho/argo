package downloader

import (
	"context"
	"time"
)

type rateLimiter struct {
	bytesPerSecond int64
	startedAt      time.Time
	totalBytes     int64
}

func newRateLimiter(bytesPerSecond int64) *rateLimiter {
	return &rateLimiter{
		bytesPerSecond: bytesPerSecond,
		startedAt:      time.Now(),
	}
}

func (limiter *rateLimiter) Wait(ctx context.Context, byteCount int) error {
	if limiter.bytesPerSecond <= 0 || byteCount <= 0 {
		return nil
	}
	limiter.totalBytes += int64(byteCount)
	expected := time.Duration(
		float64(limiter.totalBytes) / float64(limiter.bytesPerSecond) * float64(time.Second),
	)
	delay := time.Until(limiter.startedAt.Add(expected))
	if delay <= 0 {
		return nil
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

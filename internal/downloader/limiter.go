package downloader

import (
	"context"
	"sync"
	"time"
)

type rateLimiter struct {
	mutex       sync.Mutex
	rate        func() int64
	currentRate int64
	startedAt   time.Time
	totalBytes  int64
}

func newRateLimiter(rate func() int64) *rateLimiter {
	return &rateLimiter{
		rate:      rate,
		startedAt: time.Now(),
	}
}

func (limiter *rateLimiter) Wait(ctx context.Context, byteCount int) error {
	limiter.mutex.Lock()
	defer limiter.mutex.Unlock()
	bytesPerSecond := limiter.rate()
	if bytesPerSecond != limiter.currentRate {
		limiter.currentRate = bytesPerSecond
		limiter.startedAt = time.Now()
		limiter.totalBytes = 0
	}
	if bytesPerSecond <= 0 || byteCount <= 0 {
		return nil
	}
	limiter.totalBytes += int64(byteCount)
	expected := time.Duration(
		float64(limiter.totalBytes) / float64(bytesPerSecond) * float64(time.Second),
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

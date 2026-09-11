package downloader

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultRetryAttempts = 4
	defaultRetryBase     = 200 * time.Millisecond
	defaultRetryMaximum  = 30 * time.Second
)

type RetryOptions struct {
	Attempts int
	Base     time.Duration
	Maximum  time.Duration
	Now      func() time.Time
	Jitter   func(time.Duration) time.Duration
	Wait     func(context.Context, time.Duration) error
}

type retryPolicy struct {
	attempts int
	base     time.Duration
	maximum  time.Duration
	now      func() time.Time
	jitter   func(time.Duration) time.Duration
	wait     func(context.Context, time.Duration) error
}

func newRetryPolicy(options RetryOptions) (retryPolicy, error) {
	if options.Attempts < 0 || options.Base < 0 || options.Maximum < 0 {
		return retryPolicy{}, errors.New("retry configuration must not be negative")
	}
	if options.Attempts == 0 {
		options.Attempts = defaultRetryAttempts
	}
	if options.Base == 0 {
		options.Base = defaultRetryBase
	}
	if options.Maximum == 0 {
		options.Maximum = defaultRetryMaximum
	}
	if options.Maximum < options.Base {
		return retryPolicy{}, errors.New("retry maximum delay must not be less than base delay")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Jitter == nil {
		options.Jitter = retryJitter
	}
	if options.Wait == nil {
		options.Wait = waitForRetry
	}
	return retryPolicy{
		attempts: options.Attempts,
		base:     options.Base,
		maximum:  options.Maximum,
		now:      options.Now,
		jitter:   options.Jitter,
		wait:     options.Wait,
	}, nil
}

func (policy retryPolicy) do(ctx context.Context, build func() (*http.Request, error), client *http.Client) (*http.Response, error) {
	var lastError error
	for attempt := 0; attempt < policy.attempts; attempt++ {
		request, err := build()
		if err != nil {
			return nil, err
		}
		response, err := client.Do(request)
		if err == nil && !retryableStatus(response.StatusCode) {
			return response, nil
		}
		if err == nil {
			lastError = HTTPStatusError{StatusCode: response.StatusCode, Status: response.Status}
			if attempt+1 == policy.attempts {
				return response, nil
			}
			delay := policy.delay(attempt, response.Header.Get("Retry-After"))
			closeRetryResponse(response)
			if err := policy.wait(ctx, delay); err != nil {
				return nil, err
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			continue
		}
		lastError = err
		if !retryableHTTPError(ctx, err) || attempt+1 == policy.attempts {
			return nil, err
		}
		if err := policy.wait(ctx, policy.delay(attempt, "")); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return nil, lastError
}

func (policy retryPolicy) delay(attempt int, retryAfter string) time.Duration {
	if delay, ok := parseRetryAfter(retryAfter, policy.now()); ok {
		return min(delay, policy.maximum)
	}
	delay := policy.base
	for range attempt {
		if delay >= policy.maximum/2 {
			delay = policy.maximum
			break
		}
		delay *= 2
	}
	return min(policy.jitter(delay), policy.maximum)
}

func retryableStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout,
		http.StatusTooEarly,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func retryableHTTPError(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ETIMEDOUT) || errors.Is(err, syscall.ENETDOWN) || errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) {
		return dnsError.IsTimeout || dnsError.IsTemporary
	}
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}

func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
		if seconds > int64((1<<63-1)/time.Second) {
			return time.Duration(1<<63 - 1), true
		}
		return time.Duration(seconds) * time.Second, true
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	return max(when.Sub(now), 0), true
}

func closeRetryResponse(response *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
	_ = response.Body.Close()
}

func retryJitter(delay time.Duration) time.Duration {
	if delay <= 1 {
		return delay
	}
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return delay
	}
	span := delay / 5
	if span == 0 {
		return delay
	}
	offset := time.Duration(binary.LittleEndian.Uint64(bytes[:]) % uint64(span*2+1))
	return delay - span + offset
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

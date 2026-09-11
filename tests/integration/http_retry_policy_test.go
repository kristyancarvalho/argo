package integration_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/downloader"
)

func TestHTTPRetryPolicyDoesNotRetryPermanentStatus(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				http.Error(response, http.StatusText(status), status)
			}))
			t.Cleanup(server.Close)

			store := openTestStore(t)
			download := persistedDownload(t, store, server.URL+"/permanent.bin", t.TempDir(), "permanent.bin")
			engine := retryTestEngine(t, store, downloader.RetryOptions{})
			var statusError downloader.HTTPStatusError
			if err := engine.Download(context.Background(), download); !errors.As(err, &statusError) {
				t.Fatalf("download returned %v, expected HTTPStatusError", err)
			}
			if requests.Load() != 1 {
				t.Fatalf("server received %d requests, expected one", requests.Load())
			}
		})
	}
}

func TestHTTPRetryPolicyRecoversFromTransientStatuses(t *testing.T) {
	payload := makePayload(8192)
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if requests.Add(1) <= 2 {
					http.Error(response, http.StatusText(status), status)
					return
				}
				serveRetryPayload(response, request, payload)
			}))
			t.Cleanup(server.Close)

			store := openTestStore(t)
			download := persistedDownload(t, store, server.URL+"/recover.bin", t.TempDir(), "recover.bin")
			engine := retryTestEngine(t, store, immediateRetryOptions())
			if err := engine.Download(context.Background(), download); err != nil {
				t.Fatal(err)
			}
			assertCompletedDownload(t, store, download, payload)
			if requests.Load() < 4 {
				t.Fatalf("server received %d requests, expected retries followed by transfer", requests.Load())
			}
		})
	}
}

func TestHTTPRetryPolicyRespectsRetryAfter(t *testing.T) {
	payload := makePayload(2048)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if requests.Add(1) == 1 {
			response.Header().Set("Retry-After", "3")
			http.Error(response, "slow down", http.StatusTooManyRequests)
			return
		}
		serveRetryPayload(response, request, payload)
	}))
	t.Cleanup(server.Close)

	var mutex sync.Mutex
	delays := make([]time.Duration, 0)
	options := immediateRetryOptions()
	options.Wait = func(_ context.Context, delay time.Duration) error {
		mutex.Lock()
		delays = append(delays, delay)
		mutex.Unlock()
		return nil
	}
	store := openTestStore(t)
	download := persistedDownload(t, store, server.URL+"/retry-after.bin", t.TempDir(), "retry-after.bin")
	if err := retryTestEngine(t, store, options).Download(context.Background(), download); err != nil {
		t.Fatal(err)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if len(delays) != 1 || delays[0] != 3*time.Second {
		t.Fatalf("retry delays are %v, expected [3s]", delays)
	}
}

func TestHTTPRetryPolicyUsesBoundedExponentialBackoff(t *testing.T) {
	payload := makePayload(2048)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if requests.Add(1) <= 3 {
			http.Error(response, "unavailable", http.StatusServiceUnavailable)
			return
		}
		serveRetryPayload(response, request, payload)
	}))
	t.Cleanup(server.Close)

	delays := make([]time.Duration, 0)
	options := downloader.RetryOptions{
		Attempts: 4,
		Base:     time.Millisecond,
		Maximum:  3 * time.Millisecond,
		Jitter:   func(delay time.Duration) time.Duration { return delay },
		Wait: func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			return nil
		},
	}
	store := openTestStore(t)
	download := persistedDownload(t, store, server.URL+"/backoff.bin", t.TempDir(), "backoff.bin")
	if err := retryTestEngine(t, store, options).Download(context.Background(), download); err != nil {
		t.Fatal(err)
	}
	expected := []time.Duration{time.Millisecond, 2 * time.Millisecond, 3 * time.Millisecond}
	if fmt.Sprint(delays) != fmt.Sprint(expected) {
		t.Fatalf("retry delays are %v, expected %v", delays, expected)
	}
}

func TestHTTPRetryPolicyRecoversFromTransportTimeout(t *testing.T) {
	payload := makePayload(4096)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		serveRetryPayload(response, request, payload)
	}))
	t.Cleanup(server.Close)

	var requests atomic.Int32
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if requests.Add(1) == 1 {
			return nil, retryTimeoutError{}
		}
		return http.DefaultTransport.RoundTrip(request)
	})
	options := immediateRetryOptions()
	store := openTestStore(t)
	download := persistedDownload(t, store, server.URL+"/timeout.bin", t.TempDir(), "timeout.bin")
	engine, err := downloader.NewWithOptions(store, downloader.Options{
		HTTPClient:       &http.Client{Transport: transport},
		MaximumChunks:    1,
		MinimumChunkSize: 1024,
		PartsDirectory:   t.TempDir(),
		Retry:            options,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Download(context.Background(), download); err != nil {
		t.Fatal(err)
	}
	assertCompletedDownload(t, store, download, payload)
}

func TestHTTPRetryPolicyContinuesTruncatedBodyFromExactOffset(t *testing.T) {
	payload := makePayload(64 * 1024)
	half := len(payload) / 2
	var dataRequests atomic.Int32
	var resumedRange string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("ETag", `"retry-v1"`)
		if request.Header.Get("Range") == "bytes=0-0" {
			response.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", len(payload)))
			response.WriteHeader(http.StatusPartialContent)
			_, _ = response.Write(payload[:1])
			return
		}
		if dataRequests.Add(1) == 1 {
			response.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			response.WriteHeader(http.StatusOK)
			_, _ = response.Write(payload[:half])
			return
		}
		resumedRange = request.Header.Get("Range")
		start, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(resumedRange, "bytes="), "-"))
		if err != nil {
			http.Error(response, "bad range", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		response.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(payload)-1, len(payload)))
		response.WriteHeader(http.StatusPartialContent)
		_, _ = response.Write(payload[start:])
	}))
	t.Cleanup(server.Close)

	store := openTestStore(t)
	download := persistedDownload(t, store, server.URL+"/truncated.bin", t.TempDir(), "truncated.bin")
	engine := retryTestEngine(t, store, immediateRetryOptions())
	if err := engine.Download(context.Background(), download); err != nil {
		t.Fatal(err)
	}
	assertCompletedDownload(t, store, download, payload)
	if resumedRange != fmt.Sprintf("bytes=%d-", half) {
		t.Fatalf("resume range is %q, expected bytes=%d-", resumedRange, half)
	}
}

func TestHTTPRetryPolicyRecoversParallelRangesFromExactOffsets(t *testing.T) {
	payload := makePayload(4096)
	seen := make(map[int64]bool)
	var mutex sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("ETag", `"parallel-retry-v1"`)
		if request.Header.Get("Range") == "bytes=0-0" {
			response.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", len(payload)))
			response.WriteHeader(http.StatusPartialContent)
			_, _ = response.Write(payload[:1])
			return
		}
		start, end, err := requestedRange(request.Header.Get("Range"), int64(len(payload)))
		if err != nil {
			http.Error(response, err.Error(), http.StatusRequestedRangeNotSatisfiable)
			return
		}
		response.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		response.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		response.WriteHeader(http.StatusPartialContent)
		mutex.Lock()
		truncate := !seen[end]
		seen[end] = true
		mutex.Unlock()
		if truncate {
			midpoint := start + (end-start+1)/2
			_, _ = response.Write(payload[start:midpoint])
			return
		}
		_, _ = response.Write(payload[start : end+1])
	}))
	t.Cleanup(server.Close)

	store := openTestStore(t)
	download := persistedDownload(t, store, server.URL+"/parallel-retry.bin", t.TempDir(), "parallel-retry.bin")
	engine, err := downloader.NewWithOptions(store, downloader.Options{
		HTTPClient:       http.DefaultClient,
		MaximumChunks:    4,
		MinimumChunkSize: 1,
		PartsDirectory:   t.TempDir(),
		Retry:            immediateRetryOptions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Download(context.Background(), download); err != nil {
		t.Fatal(err)
	}
	assertCompletedDownload(t, store, download, payload)
	mutex.Lock()
	defer mutex.Unlock()
	if len(seen) != 4 {
		t.Fatalf("retried %d ranges, expected four", len(seen))
	}
}

func TestHTTPRetryPolicyCancellationInterruptsBackoff(t *testing.T) {
	requestSeen := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		requestSeen <- struct{}{}
		http.Error(response, "unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	waiting := make(chan struct{}, 1)
	options := immediateRetryOptions()
	options.Wait = func(ctx context.Context, _ time.Duration) error {
		waiting <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}
	store := openTestStore(t)
	download := persistedDownload(t, store, server.URL+"/cancel.bin", t.TempDir(), "cancel.bin")
	engine := retryTestEngine(t, store, options)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- engine.Download(ctx, download)
	}()
	<-requestSeen
	<-waiting
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("download returned %v, expected context cancellation", err)
	}
}

func retryTestEngine(t *testing.T, store downloader.Store, retry downloader.RetryOptions) *downloader.Engine {
	t.Helper()
	engine, err := downloader.NewWithOptions(store, downloader.Options{
		HTTPClient:       http.DefaultClient,
		MaximumChunks:    1,
		MinimumChunkSize: 1024,
		PartsDirectory:   t.TempDir(),
		Retry:            retry,
	})
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func immediateRetryOptions() downloader.RetryOptions {
	return downloader.RetryOptions{
		Attempts: 4,
		Base:     time.Millisecond,
		Maximum:  10 * time.Second,
		Jitter:   func(delay time.Duration) time.Duration { return delay },
		Wait: func(ctx context.Context, _ time.Duration) error {
			return ctx.Err()
		},
	}
}

func serveRetryPayload(response http.ResponseWriter, request *http.Request, payload []byte) {
	response.Header().Set("ETag", `"retry-v1"`)
	if request.Header.Get("Range") == "bytes=0-0" {
		response.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", len(payload)))
		response.WriteHeader(http.StatusPartialContent)
		_, _ = response.Write(payload[:1])
		return
	}
	response.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	_, _ = response.Write(payload)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type retryTimeoutError struct{}

func (retryTimeoutError) Error() string   { return "temporary timeout" }
func (retryTimeoutError) Timeout() bool   { return true }
func (retryTimeoutError) Temporary() bool { return true }

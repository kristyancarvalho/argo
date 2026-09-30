package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kristyancarvalho/argo/internal/downloader"
	"github.com/kristyancarvalho/argo/internal/storage"
)

const (
	httpTransportBenchmarkEnvironment   = "ARGO_HTTP_TRANSPORT_BENCHMARK"
	httpTransportNamespaceEnvironment   = "ARGO_HTTP_TRANSPORT_NETNS"
	httpTransportCPUProfileEnvironment  = "ARGO_HTTP_TRANSPORT_CPU_PROFILE"
	httpTransportHeapProfileEnvironment = "ARGO_HTTP_TRANSPORT_HEAP_PROFILE"
	httpTransportLinkEnvironment        = "ARGO_HTTP_TRANSPORT_LINK"
	httpTransportProtocolEnvironment    = "ARGO_HTTP_TRANSPORT_PROTOCOL"
	httpTransportPayloadSize            = 2 << 20
	httpTransportIterations             = 3
)

type httpTransportLink struct {
	name           string
	delay          time.Duration
	loss           string
	connectionRate int64
}

type httpTransportSample struct {
	elapsed     time.Duration
	connections int64
	requests    int64
}

type httpTransportServer struct {
	server      *httptest.Server
	connections atomic.Int64
	requests    atomic.Int64
	protocol    atomic.Int64
}

type pacedResponseWriter struct {
	http.ResponseWriter
	rate    int64
	started time.Time
	written int64
}

func TestHTTPTransportBenchmark(t *testing.T) {
	if os.Getenv(httpTransportBenchmarkEnvironment) == "" {
		t.Skip("set ARGO_HTTP_TRANSPORT_BENCHMARK=1 to run the isolated transport benchmark")
	}
	if os.Getenv(httpTransportNamespaceEnvironment) == "" {
		runHTTPTransportNamespace(t)
		return
	}
	runHTTPTransportBenchmark(t)
}

func TestHTTPTransportBenchmarkUsesVerifiedTLS(t *testing.T) {
	fixture := newHTTPTransportServer(t, []byte("verified"), true, 0)
	defer fixture.server.Close()
	client, transport := newHTTPTransportClient(t, fixture, "default")
	defer transport.CloseIdleConnections()
	if transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("benchmark client disabled TLS verification")
	}
	response, err := client.Get(fixture.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	badClient, badTransport := newHTTPTransportClient(t, fixture, "default")
	defer badTransport.CloseIdleConnections()
	badTransport.TLSClientConfig.RootCAs = x509.NewCertPool()
	badResponse, err := badClient.Get(fixture.server.URL)
	if badResponse != nil {
		_ = badResponse.Body.Close()
	}
	if err == nil {
		t.Fatal("benchmark client accepted an untrusted certificate")
	}
}

func runHTTPTransportNamespace(t *testing.T) {
	for _, executable := range []string{"unshare", "mount", "ip", "tc", "curl"} {
		if _, err := exec.LookPath(executable); err != nil {
			t.Skipf("%s is unavailable", executable)
		}
	}
	command := exec.Command(
		"unshare", "-Urnm", "sh", "-c",
		"mount -t tmpfs -o mode=1777 tmpfs /tmp && exec \"$@\"",
		"sh", os.Args[0], "-test.run=^TestHTTPTransportBenchmark$", "-test.v",
	)
	command.Env = append(os.Environ(), httpTransportNamespaceEnvironment+"=1", "TMPDIR=/tmp", "GOTMPDIR=/tmp")
	output, err := command.CombinedOutput()
	if err != nil {
		if strings.Contains(string(output), "Operation not permitted") {
			t.Skipf("unprivileged network namespaces are unavailable: %s", output)
		}
		t.Fatalf("isolated HTTP transport benchmark: %v: %s", err, output)
	}
	t.Log(strings.TrimSpace(string(output)))
}

func runHTTPTransportBenchmark(t *testing.T) {
	startHTTPTransportProfiles(t)
	runBenchmarkCommand(t, "ip", "link", "set", "lo", "up")
	payload := makePayload(httpTransportPayloadSize)
	links := []httpTransportLink{
		{name: "local"},
		{name: "rtt40", delay: 20 * time.Millisecond},
		{name: "rtt40-loss0.5", delay: 20 * time.Millisecond, loss: "0.5%"},
		{name: "per-connection-8Mbit", connectionRate: 1_000_000},
	}
	for _, link := range links {
		if selected := os.Getenv(httpTransportLinkEnvironment); selected != "" && selected != link.name {
			continue
		}
		configureHTTPTransportLink(t, link)
		for _, protocol := range []string{"http1", "http2"} {
			if selected := os.Getenv(httpTransportProtocolEnvironment); selected != "" && selected != protocol {
				continue
			}
			fixture := newHTTPTransportServer(t, payload, protocol == "http2", link.connectionRate)
			for _, tuning := range []string{"default", "idle8"} {
				for _, chunks := range []int{1, 4} {
					samples := make([]httpTransportSample, 0, httpTransportIterations)
					for range httpTransportIterations {
						samples = append(samples, measureArgoHTTPTransport(t, fixture, payload, protocol, tuning, chunks))
					}
					reportHTTPTransportSamples(t, link.name, protocol, tuning, chunks, samples, int64(len(payload)))
				}
			}
			curlSamples := make([]httpTransportSample, 0, httpTransportIterations)
			for range httpTransportIterations {
				curlSamples = append(curlSamples, measureCurlHTTPTransport(t, fixture, payload, protocol))
			}
			reportHTTPTransportSamples(t, link.name, protocol, "curl", 1, curlSamples, int64(len(payload)))
			fixture.server.Close()
		}
	}
}

func startHTTPTransportProfiles(t *testing.T) {
	t.Helper()
	if path := os.Getenv(httpTransportCPUProfileEnvironment); path != "" {
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := pprof.StartCPUProfile(file); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() {
			pprof.StopCPUProfile()
			if err := file.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	if path := os.Getenv(httpTransportHeapProfileEnvironment); path != "" {
		t.Cleanup(func() {
			file, err := os.Create(path)
			if err != nil {
				t.Error(err)
				return
			}
			runtime.GC()
			if err := pprof.WriteHeapProfile(file); err != nil {
				t.Error(err)
			}
			if err := file.Close(); err != nil {
				t.Error(err)
			}
		})
	}
}

func newHTTPTransportServer(t *testing.T, payload []byte, http2 bool, connectionRate int64) *httpTransportServer {
	t.Helper()
	fixture := &httpTransportServer{}
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fixture.requests.Add(1)
		fixture.protocol.Store(int64(request.ProtoMajor))
		writer.Header().Set("ETag", `"http-transport-benchmark"`)
		if connectionRate > 0 {
			writer = &pacedResponseWriter{ResponseWriter: writer, rate: connectionRate, started: time.Now()}
		}
		http.ServeContent(writer, request, "payload.bin", time.Time{}, bytes.NewReader(payload))
	})
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = http2
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			fixture.connections.Add(1)
		}
	}
	server.StartTLS()
	fixture.server = server

	return fixture
}

func (writer *pacedResponseWriter) Write(payload []byte) (int, error) {
	written, err := writer.ResponseWriter.Write(payload)
	writer.written += int64(written)
	expected := time.Duration(float64(writer.written) / float64(writer.rate) * float64(time.Second))
	if wait := expected - time.Since(writer.started); wait > 0 {
		time.Sleep(wait)
	}

	return written, err
}

func measureArgoHTTPTransport(
	t *testing.T,
	fixture *httpTransportServer,
	payload []byte,
	protocol string,
	tuning string,
	chunks int,
) httpTransportSample {
	t.Helper()
	client, transport := newHTTPTransportClient(t, fixture, tuning)
	defer transport.CloseIdleConnections()
	root := t.TempDir()
	store, err := storage.Open(context.Background(), filepath.Join(root, "argo.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	beforeConnections := fixture.connections.Load()
	beforeRequests := fixture.requests.Load()
	started := time.Now()
	for index := range 2 {
		destination := filepath.Join(root, strconv.Itoa(index))
		download := persistedChecksumDownload(
			t,
			store,
			fixture.server.URL+"/payload.bin",
			destination,
			"payload.bin",
			checksumFor(payload),
		)
		engine, err := downloader.NewWithOptions(store, downloader.Options{
			HTTPClient: client, PartsDirectory: filepath.Join(root, "parts"),
			MaximumChunks: chunks, MinimumChunkSize: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := engine.Download(context.Background(), download); err != nil {
			t.Fatal(err)
		}
		assertHTTPTransportChecksum(t, filepath.Join(destination, "payload.bin"), payload)
	}
	elapsed := time.Since(started)
	if major := fixture.protocol.Load(); major != protocolMajor(protocol) {
		t.Fatalf("%s fixture observed HTTP/%d", protocol, major)
	}

	return httpTransportSample{
		elapsed: elapsed, connections: fixture.connections.Load() - beforeConnections,
		requests: fixture.requests.Load() - beforeRequests,
	}
}

func newHTTPTransportClient(t *testing.T, fixture *httpTransportServer, tuning string) (*http.Client, *http.Transport) {
	t.Helper()
	client := fixture.server.Client()
	transport, valid := client.Transport.(*http.Transport)
	if !valid {
		t.Fatalf("unexpected transport type %T", client.Transport)
	}
	transport = transport.Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.InsecureSkipVerify = false
	transport.TLSClientConfig.RootCAs = x509.NewCertPool()
	transport.TLSClientConfig.RootCAs.AddCert(fixture.server.Certificate())
	if names := fixture.server.Certificate().DNSNames; len(names) > 0 {
		transport.TLSClientConfig.ServerName = names[0]
	}
	if tuning == "idle8" {
		transport.MaxIdleConns = 32
		transport.MaxIdleConnsPerHost = 8
	}
	client.Transport = transport

	return client, transport
}

func measureCurlHTTPTransport(t *testing.T, fixture *httpTransportServer, payload []byte, protocol string) httpTransportSample {
	t.Helper()
	root := t.TempDir()
	certificate := fixture.server.Certificate()
	certificatePath := filepath.Join(root, "server.pem")
	encoded := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
	if err := os.WriteFile(certificatePath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(certificate.Raw)
	if err != nil {
		t.Fatal(err)
	}
	host := "example.com"
	if len(parsed.DNSNames) > 0 {
		host = parsed.DNSNames[0]
	}
	_, port, err := net.SplitHostPort(fixture.server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "payload.bin")
	beforeConnections := fixture.connections.Load()
	beforeRequests := fixture.requests.Load()
	started := time.Now()
	command := exec.Command(
		"curl", "--silent", "--show-error", "--fail", "--noproxy", "*",
		curlProtocolArgument(protocol), "--cacert", certificatePath, "--resolve", host+":"+port+":127.0.0.1",
		"--output", destination, "https://"+host+":"+port+"/payload.bin",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("curl %s failed: %v: %s", protocol, err, output)
	}
	elapsed := time.Since(started)
	assertHTTPTransportChecksum(t, destination, payload)
	if major := fixture.protocol.Load(); major != protocolMajor(protocol) {
		t.Fatalf("curl %s fixture observed HTTP/%d", protocol, major)
	}

	return httpTransportSample{
		elapsed: elapsed, connections: fixture.connections.Load() - beforeConnections,
		requests: fixture.requests.Load() - beforeRequests,
	}
}

func configureHTTPTransportLink(t *testing.T, link httpTransportLink) {
	t.Helper()
	arguments := []string{"qdisc", "replace", "dev", "lo", "root", "netem", "limit", "10000"}
	if link.delay > 0 {
		arguments = append(arguments, "delay", link.delay.String())
	}
	if link.loss != "" {
		arguments = append(arguments, "loss", link.loss)
	}
	runBenchmarkCommand(t, "tc", arguments...)
}

func reportHTTPTransportSamples(
	t *testing.T,
	link string,
	protocol string,
	tuning string,
	chunks int,
	samples []httpTransportSample,
	payloadSize int64,
) {
	t.Helper()
	elapsed := make([]time.Duration, 0, len(samples))
	connections := make([]int64, 0, len(samples))
	requests := make([]int64, 0, len(samples))
	for _, sample := range samples {
		elapsed = append(elapsed, sample.elapsed)
		connections = append(connections, sample.connections)
		requests = append(requests, sample.requests)
	}
	sort.Slice(elapsed, func(left, right int) bool { return elapsed[left] < elapsed[right] })
	sort.Slice(connections, func(left, right int) bool { return connections[left] < connections[right] })
	sort.Slice(requests, func(left, right int) bool { return requests[left] < requests[right] })
	median := elapsed[len(elapsed)/2]
	megabytesPerSecond := float64(payloadSize) / 1_000_000 / median.Seconds()
	if tuning != "curl" {
		megabytesPerSecond *= 2
	}
	t.Logf(
		"RESULT link=%s protocol=%s client=%s chunks=%d median=%s min=%s max=%s throughput=%.3fMB/s connections=%d requests=%d",
		link, protocol, tuning, chunks, median, elapsed[0], elapsed[len(elapsed)-1], megabytesPerSecond,
		connections[len(connections)/2], requests[len(requests)/2],
	)
}

func assertHTTPTransportChecksum(t *testing.T, path string, payload []byte) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256(payload)
	actual := sha256.Sum256(content)
	if actual != expected {
		t.Fatalf("checksum mismatch: got %s, want %s", hex.EncodeToString(actual[:]), hex.EncodeToString(expected[:]))
	}
}

func protocolMajor(protocol string) int64 {
	if protocol == "http2" {
		return 2
	}
	return 1
}

func curlProtocolArgument(protocol string) string {
	if protocol == "http2" {
		return "--http2"
	}
	return "--http1.1"
}

func runBenchmarkCommand(t *testing.T, name string, arguments ...string) {
	t.Helper()
	if output, err := exec.Command(name, arguments...).CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v: %s", name, strings.Join(arguments, " "), err, output)
	}
}

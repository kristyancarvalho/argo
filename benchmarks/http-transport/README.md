# HTTP transport benchmark

This benchmark isolates Argo's HTTP transport behavior from the host network. It runs the integration test binary in a disposable user, mount, and network namespace, mounts a private tmpfs for SQLite and transfer files, and applies `tc netem` only to the private loopback device. No public server or host qdisc is used.

Run the campaign with:

```sh
ARGO_HTTP_TRANSPORT_BENCHMARK=1 \
  go test ./tests/integration -run '^TestHTTPTransportBenchmark$' -count=1 -v
```

Optional `ARGO_HTTP_TRANSPORT_CPU_PROFILE` and `ARGO_HTTP_TRANSPORT_HEAP_PROFILE` paths capture profiles from inside the benchmark child. `unshare`, `mount`, `ip`, `tc`, and `curl` are required. The test skips in the normal suite and when unprivileged namespaces are unavailable.

Set `ARGO_HTTP_TRANSPORT_LINK` to `local`, `rtt40`, `rtt40-loss0.5`, or `per-connection-8Mbit`, and `ARGO_HTTP_TRANSPORT_PROTOCOL` to `http1` or `http2`, to repeat a focused subset.

## Method

The fixture serves a deterministic 2 MiB payload over locally trusted TLS with correct validators and Range responses. HTTP/1.1 and HTTP/2 use the same Go server and payload. Each Argo sample measures two consecutive downloads through a reused client, real SQLite, partial-file synchronization, publication, and in-band SHA-256 verification. One- and four-range cases set the planner explicitly. Every output is hashed again after completion.

Four link conditions are measured three times each:

- local namespace loopback;
- 20 ms netem delay in each direction, approximately 40 ms RTT;
- the same delay with 0.5% packet loss;
- a deterministic 8 Mbit/s application-level limit per response connection.

`default` is the Go transport behavior used by Argo at the time of measurement. `idle8` changes only `MaxIdleConns` to 32 and `MaxIdleConnsPerHost` to 8. It does not add active requests. Connection counts cover both consecutive downloads. curl is a one-stream transfer-ceiling reference with certificate and hostname validation, but excludes Argo's SQLite, final synchronization, publication, and in-band checksum work, so it is not a feature-equivalent comparison.

The captured campaign used Linux 7.2.4, Go 1.27.0, curl 8.22.0 with nghttp2, iproute2 7.2.0, and an Intel i5-11300H. The repository filesystem was Btrfs, but the private benchmark data filesystem was tmpfs to reduce unrelated durable-storage variance. See [results-2026-09-11.tsv](results-2026-09-11.tsv) for all three timing samples represented as sorted minimum, median, and maximum values.

## Findings and decision

Increasing the idle pool is justified only for repeated four-range HTTP/1.1 work. In the finalized full strictly verified TLS campaign, the controlled 40 ms case changed from 4.13 to 5.44 MB/s; two focused repeats measured 4.47 to 5.33 MB/s and 4.51 to 5.38 MB/s. Connections across two downloads consistently fell from six to four. Issue #269 tracks the bounded production change and its revalidation.

Loss results were deliberately not used to claim a throughput gain. Three strictly verified 0.5% loss campaigns produced 5.70 to 5.14 MB/s, 3.85 to 4.91 MB/s, and 4.12 to 5.14 MB/s. The connection reduction persisted, but the short throughput samples were too variable and included one reversal.

The setting produced no stable one-range or HTTP/2 gain. HTTP/2 used one connection in every Argo case and its short lossy samples varied materially, so no HTTP/2-specific tuning is justified. Four ranges approached four times one-stream throughput only under the deliberately independent per-response throttle; this demonstrates a server-dependent opportunity, not a general claim that more ranges are faster.

The strict-TLS profiled 138.9-second campaign accumulated 8.16 seconds of CPU samples (5.87%). SHA-256 and syscalls were the largest flat consumers at 26.1% and 19.9% of sampled CPU respectively. After each sample database was closed, the terminal heap profile reported 3.16 MiB in use, primarily profiler, runtime, and HTTP/2 fixture allocations. The experiment is predominantly network delay and server pacing, with no evidence supporting buffer pools, disabled checksums, weaker durability, more active connections, forced protocol selection, or relaxed TLS validation.

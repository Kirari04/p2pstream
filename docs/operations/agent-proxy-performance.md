# Agent proxy performance

Agent forwarding reuses origin connections, shares resource admission across
parallel tunnels, and expands receive credit only for sustained transfers.
Automatic admission avoids the former low fixed request and connection ceilings.
Explicit operator limits, parser/body limits and real memory/descriptor pressure
still apply.

## Defaults and upgrade behavior

- Four authenticated TLS/TCP tunnels per agent distribute public and remote
  management traffic. Each origin has a reusable transport per lane, so one
  HTTP/2 connection cannot pin all work to a single TCP tunnel. All lanes share
  stream admission, update drain, health identity and revocation. A dropped lane
  reconnects independently; a request whose connection was lost still follows
  the configured replay rules.
- Streams begin with at most 512 KiB receive credit. Smoothed consumption
  and the recent minimum session RTT guide growth toward eight bandwidth-delay
  products, using at least 2 ms for relay scheduling. Two supporting samples
  are required, with at most one doubling per decision and a configurable
  1 GiB ceiling. Slow streams do not grow just because they have transferred
  many bytes. Unknown RTT holds the initial window; denied growth backs off
  for one second. Shared Ping probes retain a bounded 30-second history, so
  queued responses do not immediately inflate the target. A real increase in
  path latency can take up to 30 seconds to replace the earlier minimum.
  Every increase reserves its receive chunks and metadata before publishing
  credit. Ordinary reads return credit at a quarter-window threshold; explicit
  growth publishes new credit immediately. Initial and additional reservations
  remain until peer FIN or forced stream cleanup. Receive storage uses lazy
  64 KiB chunks and releases drained bulk storage, keeping a cache of up to 64
  chunks (4 MiB per stream) for reuse. The cache is populated lazily rather
  than preallocated, and this retention ceiling does not change the resource
  reservation bound. Reclaiming storage does not revoke advertised credit or
  release its reservation.
- Linux public, direct-origin and agent-origin TCP sockets retain kernel
  autotuning. Setting even a moderately sized `SO_RCVBUF` or `SO_SNDBUF`
  disables autotuning and can severely limit a connection over a WAN. The
  default agent `TUNNEL_UPSTREAM_SOCKET_BUFFER_BYTES=0` leaves both untouched;
  an explicit positive value still requests fixed buffers. Non-Linux builds
  retain the bounded 128 KiB-per-direction fallback.
- Automatic Linux sockets carry a 512 KiB lifetime admission allowance for
  connection overhead. This allowance is not a hard bound on kernel queues and
  does not pre-reserve the `tcp_rmem` and `tcp_wmem` maxima: Linux grows queues
  on demand while the adaptive controller requires host `MemTotal` and
  `MemAvailable` on every sample and adds every finite cgroup constraint, a
  finite Go memory limit when configured, and required process file-descriptor
  usage versus `RLIMIT_NOFILE`. It samples every 100 ms by default. This is a
  fail-closed admission signal, not an instantaneous hard cap on kernel queue
  growth between samples. A missing, stale or invalid resource sample rejects
  new work. Explicit positive socket sizes retain their full bounded charge,
  including Linux's possible doubling of both requested buffers. Half-close and
  idle pooling retain the applicable charge until physical close.
- Public request, resolved-client, direct-peer and direct-origin connection
  guards default to zero (automatic or disabled policy). Public sockets,
  logical HTTP requests and direct-origin sockets still reserve resources.
  HTTP/2 uploads also reserve their reachable receive credit, capped at 1 MiB
  or the smaller declared body length; this does not limit total upload size.
  HTTP/2 requests and receive-window growth consume memory, without consuming
  fictitious file descriptors or an artificial fraction of physical stream slots.
  Under actual resource exhaustion, direct-origin admission can close idle
  keep-alive sockets before reserving a replacement. Active requests continue;
  a failed replacement dial releases its reservation for the next request.
  Early public socket and request admission misses schedule one bounded
  asynchronous idle-pool sweep, then retry the ledger once without waiting for
  socket or protocol close. A 100 ms cooldown prevents request waves from
  causing cleanup stampedes. Each sweep touches at most one agent shard and one
  direct transport, preserves active requests and control work, and may need a
  later request after asynchronous close returns its reservation.
  Admission does not temporarily exceed the memory/descriptor budget to keep
  idle sockets warm.
- A public TCP accept still starts with a conservative 1.125 MiB resource
  promise for the 64 KiB header policy. Internally this is an 896 KiB physical
  connection owner plus a 256 KiB TLS-handshake phase. The handshake promise is
  released when the connection first becomes active. The 128 KiB parser,
  512 KiB automatic-kernel allowance and 256 KiB steady userspace allowance
  remain owned until physical close. Direct-peer fairness initially charges the
  same lifetime plus handshake bytes, then returns the temporary handshake
  credit when the connection becomes active. Each admitted request adds a 32
  KiB context plus twice the decoded header, path and host bytes, including
  bounded metadata. Upload
  credit remains capped at 1 MiB or the smaller declared body. A further 32 KiB
  body-copy working promise is acquired only after access, redirect and policy
  decisions, before cache, static or upstream work. These values are admission
  envelopes, not measured heap or RSS subtraction.
- A server-side agent stream owns the exact receive-window accounting (about
  640.625 KiB at the 512 KiB initial window) plus 256 KiB of userspace and no
  fictitious file descriptor. The agent side adds the automatic 512 KiB kernel
  allowance and two real dial descriptors. These stream promises remain
  subject to the same 100 ms measured-pressure, fail-closed controller.
- Pool entries follow the shared stream budget; the separate 256-target ceiling
  is gone. Warm hits avoid constructing a disposable HTTP transport. Remote
  management requests now reuse pinned TLS connections, with invalidation on
  trust/agent changes. Mutations can hand off only after a local admission
  failure before any headers or body bytes were sent.
- A busy agent at its negotiated stream limit waits inside the reusable HTTP
  transport, allowing returning connections to serve queued requests. Opening
  bursts queue fairly for up to ten seconds or the earlier request deadline.
  Health admission uses 250 ms and treats local saturation as a skipped probe.

Existing explicit environment values remain limits. Remove old overrides to use
automatic admission. Upgrade the server and agents together: both fixed and
adaptive modes require explicit capacity headers. An explicit fixed
`TUNNEL_MAX_CONCURRENT_REQUESTS` remains supported for controlled deployments;
mixed releases must not be treated as a promise of retired legacy-header
negotiation. Additional lanes start only after the primary handshake
acknowledges support.
No throughput improvement bypasses agent destination policy or TLS trust.

## TCP bandwidth regression and coverage

The published **v0.1.53-staging.91** is affected: public sockets were fixed at
64 KiB per direction, while direct and agent origins defaulted to 128 KiB.
The fixes described here restore TCP autotuning. Upgrade the server and agents
together to a release containing these changes, and remove explicit
socket/window overrides to use the new defaults. Increasing only
`net.core.rmem_max`/`wmem_max` did not remove the affected release's
application-imposed public cap.

`scripts/test-proxy-wan.sh` runs in the required CI Verify job. It creates
three disposable Linux network namespaces (server, router, client) with 80 ms
RTT and 100 Mbit/s capacity. Delay is on the forwarding router, avoiding TCP
Small Queues artifacts from applying netem at the sending endpoint. It tests
HTTP/1.1 and HTTP/2 through the actual public HTTPS listener and routing stack,
with direct origins and a real agent. A second topology puts the origin across
the WAN to catch origin socket regressions. Transfers verify complete payload
hashes and use the median of three runs against an unproxied control; admission,
TLS, response status and negotiated HTTP versions are exercised too. HTTP/2
uploads cover the public receive direction. The harness needs Linux `ip`, `tc`,
`unshare`, `nsenter` and a curl build with HTTP/2. It uses unprivileged namespaces,
or passwordless sudo on CI hosts that restrict user namespaces, and changes no
host addresses, routes, qdiscs or sysctls.

The new download test against the released source measured **1.17–1.18 MB/s**
against a **9.72 MB/s** HTTPS control and failed its 65% relative-throughput
floor. The fix measured **9.72–9.74 MB/s**, matching the control. This test catches
the public socket regression that the older transport-only benchmark missed.

A separate three-VM Multipass lab uses public HTTPS, four real TLS/Yamux lanes,
WireGuard (MTU 1420) and nginx origins. With 80 ms RTT on both client and VPN
links and unchanged guest OS buffer limits, three 128 MiB downloads measured:

| Path | Median MB/s |
| --- | ---: |
| nginx HTTPS control, client link only | 36.52 |
| Direct proxy through WireGuard | 23.25 |
| Agent proxy through WireGuard | 16.82 |
| Four agent downloads, 64 MiB each, aggregate | 39.79 |

These include connection setup and TCP startup; shorter transfers differ
substantially. They demonstrate removal of the fixed 1.4–1.5 MB/s ceiling, not
NIC line-rate under every topology. VPN loss, congestion, OS autotuning maxima,
TLS/relay work and explicit operator settings can still limit throughput.
That run used the previous 64 MiB ceiling and lifetime-byte growth policy.
The implementation below replaces that growth policy; initial credit remains
512 KiB and growth can be denied when memory is unavailable.

Reproduce the CI regression without a development server:

```sh
scripts/test-proxy-wan.sh
```

## Kernel-memory admission regression

`scripts/test-proxy-memory.sh` runs a finite slow-reader burst in a disposable
Docker cgroup with a 512 MiB memory limit and no swap or host network access.
It retains touched application ballast, admits 24 real public TCP sockets, and
fills their automatically tuned queues while clients stop reading. The test
uses the production resource sampler and admission ledger, records independent
host/cgroup constraints and kernel `memory.stat` socket bytes, holds the queues
across multiple 100 ms samples, and requires memory-driven admission rejection
without OOM events. Physical close must return reservations and restore
admission. It runs in CI alongside the WAN throughput regression.

```sh
scripts/test-proxy-memory.sh
```

This covers one constrained-memory burst. Admission sampling cannot bound all
growth of already-active kernel queues between samples or after admission
stops; deployment memory limits and the operating system remain relevant.
Larger TCP maxima, more simultaneous transfers, and longer sampling intervals
need workload-specific validation.

## High-bandwidth profiling

The 100 Mbit/s CI regression is not a gigabit capacity qualification. Further
profiling on 2026-09-15 used the same three 2-vCPU, 2-GiB Ubuntu 24.04 VMs,
512 MiB downloads, four authenticated TLS/Yamux lanes and WireGuard. An nginx
relay fetched the same agent origin over the same VPN path as the direct proxy.
Rates below are decimal MB/s and include transfer startup. This was a shared
development host, not a dedicated performance runner.

The following bullets are historical profiles of earlier window and
growth implementations; they are retained for context and are not cache64
measurements.

- With 80 ms VPN RTT, the original 6 MiB receive / 4 MiB send autotuning
  maxima limited fresh agent downloads to about 20–25 MB/s. Raising both
  maxima to 32 MiB in the lab produced about 77–86 MB/s. An independent
  reverse-direction iperf3 test increased from 35.8 to 128.6 MB/s for one
  TCP flow. These were lab settings, not new application defaults.
- On the path without added delay, allowing tunnel growth to 64 MiB produced
  about 96–108 MB/s and a 421 MiB post-GC server heap after repeated downloads.
  Roughly 414 MiB was retained by Yamux's `bytes.Buffer` receive storage.
  A 2 MiB tunnel ceiling delivered about 101–109 MB/s with a 21 MiB heap in
  the corresponding profile. Heap is distinct from RSS, kernel socket memory,
  and the application's resource reservations.
- Smaller windows are not appropriate everywhere. With the larger Linux
  maxima and 80 ms VPN RTT, an 8 MiB tunnel ceiling delivered about 48–53 MB/s,
  compared with about 77–86 MB/s at 64 MiB. That baseline replenished credit after
  at least half its window becomes available, so a high-throughput path needs
  headroom beyond a single bandwidth-delay product.
- The profiled baseline grew credit with total consumed bytes, so even slow,
  long-lived connections eventually reached the maximum. Its `bytes.Buffer`
  capacity and transient reallocation could exceed logical receive credit.
  This motivated the measured growth and bounded chunk storage now implemented.

The final comparison used the lazy 64-chunk cache (64 KiB chunks,
4 MiB retention ceiling per stream). It was not preallocated and did not alter
the resource reservation bound. These are three cold-service alternating pairs
on the same development lab, with 512 MiB HTTP/2 transfers; the parallel case
used four 256 MiB transfers. They are lab comparisons, not a physical 10 Gbit/s
qualification. The baseline was commit `463958f`, with the prior 64 MiB
lifetime-byte growth policy; both builds retained Linux TCP autotuning, with
32 MiB receive/send maxima in these runs. Each pair restarted the services
and cleared the lab's saved TCP connection metrics.

| Lab case | Baseline median MB/s (range) | Final median MB/s (range) |
| --- | ---: | ---: |
| LAN, one transfer | 112.4 (112.1–113.0) | 109.4 (101.5–110.0) |
| VPN, 80 ms RTT, one transfer | 56.9 (56.7–78.8) | 81.1 (55.7–83.4) |
| VPN, 80 ms RTT, four transfers | 91.2 (90.3–103.5) | 90.9 (90.7–106.2) |

The VPN single-transfer series is bimodal in both modes, so the medians are
descriptive rather than a capacity claim. An intermediate 512 KiB chunk cache
had about a 15% lower parallel median and substantial allocation churn; it
was rejected. Increasing reuse to 4 MiB recovered the parallel result while
preserving the same accounting bound. The final LAN median was 2.6% below
the baseline; this small sample does not establish throughput equivalence.

With 80 ms RTT on both links, two pairs measured 51.9 MB/s for the baseline
and 54.3 MB/s for the final build. Under 80 ms VPN RTT plus 5 ms jitter in
each direction, four pairs measured medians of 22.3 and 17.1 MB/s respectively,
with broad ranges of 17.4–26.2 and 15.7–59.2 MB/s. An nginx relay over the same
jittered VPN measured 17.4 MB/s (15.9–20.3, three runs). TCP snapshots show
congestion and retransmission variance; TCP receive-window metrics do not
measure Yamux credit. These samples support a lab-network limitation but do
not establish equal performance under jitter. With the original 6 MiB/4 MiB
kernel maxima, two pairs measured 22.3 MB/s for the baseline and 17.5 MB/s
for the final build (12.8–22.1 MB/s); that small comparison is also variable.

Separate 30-second server profiles during repeated 512 MiB LAN downloads
measured the following. Both captures include startup and a forced GC; they
are diagnostic samples, not equal-byte CPU benchmarks.

| Server measurement | Baseline | Final |
| --- | ---: | ---: |
| Retained Go heap after GC | 416.0 MiB | 16.0 MiB |
| Cumulative Go allocations | 1,726.9 MiB | 59.0 MiB |
| RSS after the traffic sequence | 548.2 MiB | 55.6 MiB |
| Peak RSS | 859.1 MiB | 57.1 MiB |
| Sampled CPU time | 12.38 s | 11.90 s |

Baseline Yamux buffer growth retained about 407.9 MiB; final receive chunks
retained about 7.4 MiB. Heap, RSS, kernel socket buffers and admission
reservations are separate measurements. The profile supports a substantial
allocation/retention improvement; it does not establish a CPU speedup.

The new controller measures consumption over elapsed time, including time
spent outside `Read`, and samples at intervals of at least one RTT or 10 ms.
It smooths the measured rate, requires two growth-supporting samples,
discards epochs spanning long idle gaps, and limits each increase.
Existing advertised credit remains backed until
the stream closes; it cannot safely shrink after being granted.
Application-controlled Yamux receive credit, HTTP/2 upload credit, replay
buffers, headers and request state retain strict lifetime reservations. Some
resident application buffer memory is intentionally also present in measured
usage; removing that conservatism requires actual per-buffer commit tracking.
Increasing Linux TCP maxima can increase measured memory when traffic grows the
queues, but does not increase the automatic socket's 512 KiB admission allowance.
These measurements do not demonstrate 10 Gbit/s performance.

The optional HTTP/2 diagnostic isolates receive-window behavior in a Go HTTPS
server configured like the public listener. It is separate from the complete
public-proxy CI test. At 80 ms RTT, median uploads were 161 Mbit/s for HTTP/1.1,
87 Mbit/s with 1 MiB HTTP/2 connection/stream credit, and 182 Mbit/s with 3 MiB
for both. Raising only the stream window left the 87 Mbit/s ceiling unchanged.
The production upload window remains 1 MiB. Review showed that HTTP/2 reset
streams can return connection credit while their handlers still retain unread
bodies; a single connection reservation therefore cannot cover repeated reset
waves. Larger windows need reservation for each body until cleanup, including
bodies canceled before handler admission. The diagnostic has no application
resource ledger and does not enable larger windows in production.

```sh
scripts/benchmark-http2-upload.sh
go test ./internal/tunnel -run '^$' -bench '^BenchmarkTunnelYamuxThroughput$' -benchtime=1s -count=3
go -C third_party/yamux test -run '^$' -bench '^BenchmarkTransportThroughput$' -benchtime=1s -count=3
go -C third_party/yamux test -run '^$' -bench '^BenchmarkReceiveBufferFrameReuse$' -benchtime=1s -count=3
```

The loopback benchmarks compare chunk sizes, initial/window growth, TCP and
TLS. Their throughput is not an end-to-end proxy capacity result. The coalescing
adapter is test-only; its counters measure writes into the transport API,
including TLS, rather than actual TCP writes or TLS records. Coalescing helped
plain TCP in these runs but showed no improvement through TLS.

For CPU, heap and contention profiles, build the optional lab-only profiler:

```sh
go build -tags labprofile -o /tmp/p2pstream-labprofile .
```

In an isolated lab, run the server or agent with
`P2PSTREAM_LABPROFILE=1`, `P2PSTREAM_LABPROFILE_SECONDS=30` and a separate
`P2PSTREAM_LABPROFILE_DIR` for each process. Optional
`P2PSTREAM_LABPROFILE_BLOCK_RATE=1000000` and
`P2PSTREAM_LABPROFILE_MUTEX_FRACTION=100` enable sampled contention profiles.
The timer starts during process initialization, so captures include startup.
Keep traffic running for the interval and wait for `labprofile finished` before
stopping the process. Early termination can leave an incomplete capture.
The profiler writes private local files and exposes no HTTP endpoint. Normal
release builds exclude it. Compare throughput with unprofiled runs; forced GC
and profiling can perturb execution.

```sh
go tool pprof -top /path/to/server/cpu.pprof
go tool pprof -top -inuse_space /path/to/server/heap.pprof
go tool pprof -top -alloc_space /path/to/server/heap.pprof
```

## Reverse-proxy qualification

`scripts/qualify-proxy-performance.sh` is the finite local qualification
harness. It drives the production HTTPS public listener and routing pipeline
through direct and real four-lane agent targets, and compares the direct path
with the installed HAProxy 3.2 control. The control terminates the same P-256
certificate, negotiates the same client HTTP version, forwards to the same
HTTP origin, uses the same four pinned proxy CPUs, and is sampled with the same
load generator. The origin and load generator are pinned to the remaining
available CPUs when the host provides them.

The full run puts each p2pstream server plus its in-process real agent fixture
in a fresh 4 GiB/no-swap cgroup. This combined process envelope is conservative
for RSS and CPU and is not a server-only measurement. HAProxy is a direct-path
control; it has no authenticated TLS/Yamux agent hop, agent destination policy,
or agent stream admission. Agent results therefore quantify the feature/path
cost separately and are not HAProxy-parity claims. Empty WAF, access, traffic,
cache and Sites policies still execute through p2pstream's production routing
pipeline, while HAProxy has no equivalent application policy pipeline.

Acceptance thresholds are fixed before examining the full results:

- On feature-comparable direct scenarios, candidate useful completed
  throughput must be at least 80% of HAProxy. At the fixed 500 request/s
  offered load, direct p95 and p99 must be at most 1.25 times HAProxy.
- Candidate direct and agent throughput must be at least 90% of the `ee1aba7`
  baseline in each sampled workload. Fixed-rate p95 and p99 must be at most
  1.10 times baseline. Closed-loop saturation latency is reported but is not
  called tail-latency parity because the implementations can operate at
  different achieved loads.
- The 4 GiB run must place 1,536 requests concurrently inside the controlled
  origin barrier through the real public listener, complete every verified
  response, return no admission 503 below that validated budget, and complete
  the following recovery workload. A configured client concurrency by itself
  does not satisfy this criterion; the origin-reported inflight high-water is
  recorded.
- Each successful response must have the expected length and content/hash.
  Results record status classes, useful completions/s, p50/p95/p99, CPU time,
  process RSS, Go heap/GC where available, file descriptors, owned socket FDs,
  cgroup current/peak memory and `memory.stat` socket bytes. A TLS-idle phase
  records cold, 512-handshake peak and post-close values.

H1 keep-alive and connection churn, H2 multiplexing, 64 KiB uploads, slow
readers, the fixed-rate comparison and the 1,536-concurrent barrier run in each
full iteration. Three runs alternate baseline and candidate process order;
HAProxy follows each pair. Raw JSONL and process logs stay under ignored
`tmp/`. The smoke mode reduces counts and does not use the 4 GiB container, so
it validates wiring rather than capacity.

The external-agent mode starts the server and each production agent runtime in
separate processes and separate 4 GiB/no-swap cgroups. It requires the expected
legacy or aggregate-v1 negotiation and exact physical lane shape before load,
then records per-process cgroup identity, CPU sampling duration, RSS, descriptor,
socket-memory and OOM data. Every accepted external result must conserve all
request events through the final recorder flush and, outside capacity probes,
must complete every payload-verified request without errors. All process sample
windows end before the server recorder drain, whose wall and CPU cost remain
separate. Aggregate snapshots distinguish initial and grown shared pool bytes,
session metadata, the lane-zero legacy supplement and per-stream legacy growth.
Per-process peaks have distinct sampling intervals and are never summed as if
they were a simultaneous combined peak.

This topology has a total envelope of one 4 GiB server cgroup plus one or four
4 GiB agent cgroups. Its optional 1 GiB ballast is server-only. It supplements
the original combined server-and-agent 4 GiB result and cannot establish that
the same workload fits in one 4 GiB deployment. The checked-in external smoke
only validates fixture wiring: the legacy side established one control plus
three bulk lanes, while the aggregate side explicitly negotiated one control
plus four bulk lanes, and each persisted 100 of 100 verified requests. Separate
constrained capacity, agent-pressure recovery and aggregate
growth/idle/pressure/recovery measurements remain required.
The external candidate agent role also accepts a separate bounded ballast for
that later agent-pressure trace and reports the touched bytes in its own process
snapshot. The legacy smoke overlay and all existing results used zero agent
ballast.

The first qualification pass is recorded under
`docs/operations/benchmarks/proxy-qualification-2026-10-16/`. It is a profile
and correctness pass plus one warmed saturation sample, not the prescribed
three-run qualification. The fixed-rate direct H2 case completed 500.1 useful
requests/s with p95 1.60 ms and p99 1.75 ms, versus HAProxy's 500.2
requests/s, 1.42 ms and 1.58 ms. The 1.13x p95 and 1.10x p99 ratios pass the
predeclared 1.25x fixed-load gate.

The warmed 10-second direct saturation sample did not pass the 80% HAProxy
throughput gate except under connection churn:

| Workload | p2pstream useful requests/s | HAProxy useful requests/s | Ratio | `ee1aba7` baseline ratio |
| --- | ---: | ---: | ---: | ---: |
| H1 keep-alive GET | 20,756 | 82,580 | 25.1% | 94.0% |
| H1 connection churn GET | 4,695 | 4,503 | 104.3% | 96.7% |
| H2 multiplexed GET | 19,118 | 58,095 | 32.9% | 97.0% |
| H2 64 KiB upload | 6,775 | 9,821 | 69.0% | 100.6% |

All responses in that sample were content-verified and no request errors were
observed. The candidate passed the single-sample 90% baseline throughput gate.
Its saturation p95/p99 values are not compared as a latency gate because the
systems achieved different request rates. The raw recorder logs also show
352,707 candidate and 371,470 baseline observability events dropped across the
four direct and four agent workloads. Client-verified completions remain the
throughput authority; dashboard event counts from this pass are incomplete.

A later profile-directed candidate (`d3767c9`) changed observability event
insertion from per-row `INSERT ... RETURNING` scans to a prepared `:exec`
statement inside the existing atomic batch transaction. In one separately
labelled H1 keep-alive diagnostic it completed 25,121 useful requests/s, with
all 251,318 timed requests persisted, zero drops, and 383 queued events drained
explicitly in 6.8 ms after the timed interval. That is 29.6% of the same-run
HAProxy result and 79.9% of an unmatched standard-library reverse proxy with
128 idle origin connections. The standard-library diagnostic used its default
per-request copy-buffer allocation, so it is retained as an allocation-cost
diagnostic rather than a matched application-overhead estimate.

The matched standard-library control reuses the same 32 KiB copy-buffer pool
as production. It reached 44,513 requests/s with 32 idle origin connections and
47,361 requests/s with 128. A subsequent profiled `b4dd36b` candidate reached
25,435 requests/s versus same-run HAProxy at 86,273: 29.5% of HAProxy and 53.7%
of the pooled 128-idle standard-library control. All 254,493 timed candidate
requests were persisted with no drops; the explicit post-load recorder barrier
drained 1,334 records in 29.6 ms and 0.02 proxy CPU-seconds outside the timed
throughput and CPU interval. This is still one diagnostic sample, not a passing
qualification result.

The later context-pool/LOWAT candidate showed that 128 closed-loop clients were
past its useful H1 operating point on this CPU set. Across concurrency 1, 4,
16, 32, 64, 128 and 256, its useful rates were 5,864, 19,345, 29,614, 28,574,
28,140, 24,810 and 21,232 requests/s. The peak therefore occurred at 16. At
that point the stronger pooled standard-library control reached 46,917 and
HAProxy 70,055 requests/s, so p2pstream reached 42.3% of HAProxy and 63.1% of
the Go control. Limiting active requests may improve tail latency, but this
sweep does not support it as a throughput-parity mechanism. The context-pool
allocation profile fell from 4.38 GiB across 305,506 warmup and timed requests
to 3.70 GiB across 298,703, and `newPublicProxyContext` disappeared as a
material allocation site. The profiled 128-client throughput sample was 2.5%
below the earlier run, so it demonstrates the allocation reduction but no
throughput improvement.

The subsequent forwarding-pointer change removed the remaining route
resolution copy while preserving owned snapshots in the retry transport and
cache response body. Its allocation profile fell to 3.42 GiB across 318,884
warmup and timed requests, about 15% less per request than the `d1d952f`
profile. At 128 clients it reached 31.45% of its same-run HAProxy result versus
31.44% for `d1d952f`, so the change has a measured allocation/GC benefit but no
measured normalized throughput benefit. A current mutex profile attributed
79.6% of sampled delay to the standard-library transport idle-connection path;
application adaptive-capacity, admission, selector and recorder paths were
3.05%, 1.45%, 1.17% and 0.77% respectively.

A matched test-only transport experiment compared one pooled Go transport with
32 or 128 idle origin connections against four round-robin transports with 32
each. At 128 clients, single-128 and four-by-32 were effectively equal at
46,392 and 46,493 useful requests/s, but sharding opened 700 new timed origin
connections instead of 19, increased peak FDs from 264 to 310 and worsened
p95/p99. Single-32 reached 42,621 requests/s and opened 7,973 new timed origin
connections. Sharding is therefore not supported. A larger idle budget on the
existing single transport remains a separately qualified production
experiment; the diagnostic does not change operator `MaxConns`, request
admission or cache semantics.

An isolated production overlay then changed only the existing transport's
`MaxIdleConnsPerHost` from 32 to 128. Two alternating H1 runs at 128 clients
improved useful throughput by 5.3% in both orders, reduced p95 by 8--9% and
p99 by 5--10%, and reduced roughly 7,500 timed origin reconnects to 0--21.
H2 throughput improved only 0.9--1.3%, but p95 improved 7--8%, p99 12--13%,
and 5,600--6,600 reconnects fell to zero. Neither H1 nor H2 improved at 16
clients. A TLS origin negotiating upstream HTTP/2 also showed no benefit. With
the TLS origin restricted to HTTP/1.1, 128 improved throughput by 50.9%, cut
p95/p99 from 22.29/34.10 to 12.08/18.02 ms, and eliminated 7,413 reconnects.

The 1 GiB-ballast retention check completed H1 and H2 capacity 128 plus a
512-request recovery for both settings without errors, 503s or admission
misses. The 128 setting retained 142 socket FDs after recovery versus 46 for
32; both reported approximately 1.154 GiB RSS and 4,096 cgroup socket bytes.
This is a measured FD tradeoff, not proof of zero memory cost. The transport's
existing real-TCP pressure-reclaim regression and focused lifecycle suite pass
normally and under the race detector with the larger pool, so the bounded
per-origin idle default is now 128. The total idle budget, accounting and
pressure-reclaim behavior are unchanged, and a positive operator `MaxConns`
continues to use one transport rather than sharding.

Setting `TCP_NOTSENT_LOWAT` to 32 KiB on the existing public, direct-origin and
agent-origin TCP configuration paths reduced the 24-slow-reader socket peak
from 66,355,200 bytes (63.28125 MiB) to 5,996,544 bytes (5.71875 MiB). A
separate controlled-pressure phase then peaked at 461,434,880 of 536,870,912
cgroup bytes, rejected six acquisitions for memory, and recovered healthy with
zero socket bytes and no OOM. Paired candidate/parent H1 and H2 WAN download
and upload results were effectively identical. LOWAT is best-effort and does
not change the conservative automatic socket admission promise, `SO_SNDBUF`,
`SO_RCVBUF`, or the shared Yamux physical connection.

Under the real 4 GiB cgroup, both direct and agent H1 runs opened 1,536 client
TCP connections and reached 1,536 requests inside the origin barrier. H2
reached the same logical concurrency over 10 direct or 8 agent client TCP
connections. Every path completed 3,072 verified responses with zero errors or
503s, followed by a successful 512-request recovery phase. Peak p2pstream RSS
was 192 MiB for direct H1, 193 MiB for direct H2, 319 MiB for agent H1 and
394 MiB for agent H2. The agent fixture includes the server and real in-process
agent in one process. After agent H1, about 1,536 origin keep-alive sockets
remained pooled; the recovery workload succeeded, but RSS stayed near 398 MiB
and 1,592 socket descriptors remained after the later agent recovery sample.
This is functional admission recovery with retained idle-pool resources, not
resident-memory recovery. The run had no 1 GiB ballast, so it does not by
itself reproduce the incident's estimated 28% starting headroom.

A follow-up H1 agent run touched a 1 GiB ballast inside the same 4 GiB cgroup
before opening connections. At 1,536 concurrency, the `ee1aba7` baseline
reached only 404 origin requests and the phased-owner candidate reached 1,221;
neither passed the barrier. The candidate recorded 432 agent-stream
`resource_pressure` misses. Its sampled owner peaks included 1.31 GiB for all
1,536 physical public connections, 276 MiB of overlapping handshake work, and
about 49 MiB each for request and response work. The colocated server and agent
RSS peaked at 1.31 GiB, including the touched ballast; the owner promises explain
the admission decision and are distinct from observed RSS.

The original failed-boundary rows used a 10-second origin-barrier release and
the same 10-second upstream response-header timeout. Their origin high-water,
owner samples and admission-miss counters remain useful, but their final 5xx,
error and useful-completion counts include the induced response-timeout
cascade. Corrected boundary reruns release an unfilled barrier after three
seconds while retaining the 10-second upstream timeout and record that hold in
each JSON row.

With that correction, the 1,400 candidate point returned 1,340 HTTP 204 and 60
HTTP 503 responses, reached 1,315 requests inside the origin, and recorded 145
resource-pressure acquisition misses. At 1,536 the baseline returned 688 HTTP
204 and 848 HTTP 503 responses with origin high-water 573; the candidate
returned 1,189 HTTP 204 and 347 HTTP 503 responses with high-water 1,146. Both
then completed the separate 512-request recovery workload without errors. The
503 counts now represent capacity behavior rather than an origin timeout.

The bounded downward search passed all 2,560 verified requests at 1,280
concurrency with no 503s, request errors or admission misses, then failed at
1,400 after reaching 1,308 requests inside the origin barrier. The 1,400 point
recorded 92 agent-stream resource-pressure misses. The measured 1 GiB-ballast
boundary is therefore above 1,280 and below 1,400 for this colocated agent
fixture. Larger 2,000/4,000 points were not run after the lower failure
established the boundary. In production the agent and its origin sockets are
remote and do not share the server cgroup; separating the fixture processes is
required before attributing the entire boundary to server-resident allocations
or changing the negotiated aggregate stream credit.

```sh
scripts/qualify-proxy-performance.sh --smoke
scripts/qualify-proxy-performance.sh \
  --baseline-binary tmp/proxy-qualification/bin/server-ee1aba7.test \
  --candidate-label candidate
```

The harness does not establish universal reverse-proxy parity. It is a
loopback, single-host comparison without WAN loss, network namespaces, cache
hits, WAF rules, WebSockets, retries, or mixed tenant traffic. CPU affinity
reduces local scheduler variance but origin/load work can still contend for
shared caches and memory bandwidth. Use the WAN regression and workload-specific
production profiling alongside these results.

## Earlier transport measurements

Local AMD Ryzen 9 6900HS, Linux amd64, Go 1.26.6. These are development
benchmarks, not a production throughput guarantee.

| Workload | Previous behavior / control | Updated behavior |
| --- | --- | --- |
| Warm GET allocation | About 6.3 KB and 69 allocations/request | 5.2–5.3 KB and 65 allocations; no new stream opens |
| Warm HTTP/2 TLS POST, 1 KiB | About 8.4 KB and 91 allocations/request | About 6.9 KB and 86 allocations; no new stream opens |
| 4 MiB download, one tunnel, simulated 40 ms RTT | Fixed 512 KiB: 6.8 MB/s | Growth to 2 MiB: 23.9 MB/s |
| API requests alongside two bulk-download loops, simulated 40 ms RTT | One tunnel: median 167–169 ms, p95 172–210 ms | Four tunnels: median 41–42 ms, p95 45–87 ms |

The WAN harness runs the real agent runtime through management TLS and an
HTTP/2 TLS origin. A bounded, pipelined delay relay models propagation in both
directions. It does **not** model packet loss, kernel congestion control or a
specified bandwidth. Relay allocations are part of that harness, so use the
separate warm microbenchmarks for allocation comparisons. WAN download results
use three measured transfers; mixed results use two runs of 30 API requests.

Validation includes 300 retained warm targets; 3,000 simultaneous logical
requests from one client under sufficient synthetic resource headroom; the
existing 4,096-stream capacity test; a 300-request burst through a legacy
64-stream agent; primary-lane loss/reconnect/revocation; mutation replay safety;
resource-exhaustion recovery; and receive-credit lifetime tests. The complete
Go suite, targeted race tests and upstream Yamux race suite pass.

Reproduce without starting a development server:

```sh
go test ./internal/server -run '^$' -bench '^BenchmarkAgentTransport(WarmKeepAliveGET|WarmHTTP2POST1KiB)$' -benchtime=3s -count=2
go test ./internal/server -run '^$' -bench '^BenchmarkRealAgentWANDownload$' -benchtime=3x
go test ./internal/server -run '^$' -bench '^BenchmarkRealAgentWANMixed$' -benchtime=30x -count=2
go test -race ./internal/tunnel ./internal/sysmetrics ./internal/agent ./internal/server -run 'Growing|ExternalResources|ExternalBytes|RealAgent|EnvironmentAgent|Adaptive|AutomaticPublicAdmission|AgentTunnel|AgentTransportPool'
```

## Finding remaining delays

Enable debug traffic tracing for a narrow route/sample. Request events expose
`connection_reused`, `tunnel_lane`, `connection_acquire_ms`,
`tunnel_admission_ms`, `tunnel_open_ms`, `tunnel_handshake_ms`, `agent_dial_ms`,
`agent_dns_ms`, `tls_handshake_ms` and `upstream_first_byte_ms`.

These phases overlap: connection acquisition includes admission, tunnel opening
and TLS; first-byte time starts before connection acquisition. Do not add all
fields together. Agent dial/DNS values come from the remote agent and require
compatible peers. Reused requests normally have zero dial/open/TLS time. Normal
traffic does not install these trace hooks or emit a per-request info log.

Diagnose rejections from configured guards, queue exhaustion, memory headroom,
file-descriptor headroom and sensor errors separately. Application-memory
estimates are conservative and intentionally include resident bytes plus
outstanding advertised credit; they protect that credit against simultaneous
expansion between resource samples. Host sampling observes kernel queue growth
but does not impose an instantaneous hard cap between samples. Physical stream
and queue structures retain a 65,536 guard. The 256 opening backlog bounds
outstanding Yamux handshakes, not lifetime requests. Old protocol peers and
explicit settings may impose lower ceilings.

## QUIC assessment

QUIC remains an optional future transport experiment. Its independent streams
can avoid delivery blocking between streams following packet loss, unlike
streams sharing one TCP connection. See [RFC 9000, section 13](https://www.rfc-editor.org/rfc/rfc9000.html#section-13).
The four-TCP-lane implementation provides useful isolation using the existing
HTTPS upgrade path and is already measured here.

The latency-only harness cannot justify adopting QUIC for loss recovery. A
production QUIC option requires authenticated UDP ingress, stream/resource
accounting, reconnect and update-drain parity, deployment compatibility and
fallback behavior. Evaluate it next on an isolated network with controlled loss,
bandwidth and RTT, comparing API p95/p99, bulk throughput, CPU and memory.
No QUIC listener or new UDP exposure is introduced by this change.

## Local Yamux extension

`third_party/yamux` contains upstream v0.1.2 with its MPL-2.0 license and tests.
The small extension exposes monotonic per-stream receive-window growth without
changing the wire protocol. Its own module tests run separately in CI and Docker
test stages because root `go test ./...` does not traverse nested modules. The
full suite runs normally, followed by `go test -race -short ./...`: upstream's
short mode excludes its 250 MiB and 16 GiB stress transfers from race
instrumentation, while keeping both transfers in normal testing.

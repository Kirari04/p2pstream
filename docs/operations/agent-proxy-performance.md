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
- Streams begin with at most 512 KiB receive credit, then double toward the
  configured maximum (2 MiB by default) as data is consumed. Every increase reserves its
  additional memory first. Denied growth keeps the request serving at its
  current window. Initial and additional reservations remain until peer FIN or
  forced stream cleanup. This avoids charging idle connections for bulk-transfer
  windows they never use.
- The agent's origin sockets request 128 KiB per direction, configurable with
  `TUNNEL_UPSTREAM_SOCKET_BUFFER_BYTES`. Accounting includes Linux's possible
  doubling of both buffers. Increase this on an agent whose origin itself is
  across a high-latency link; larger buffers reduce available connection count
  on memory-constrained machines. The management TCP connections keep kernel
  autotuning. Direct-origin sockets use the same bounded 128 KiB default.
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
  Admission does not temporarily exceed the memory/descriptor budget to keep
  idle sockets warm.
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
automatic admission. Old peers retain their negotiated compatibility ceilings;
additional lanes start only after the primary handshake acknowledges support.
No throughput improvement bypasses agent destination policy or TLS trust.

## Measurements

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
file-descriptor headroom and sensor errors separately. Memory estimates are
conservative and intentionally include resident bytes plus outstanding credit;
they protect against simultaneous expansion between resource samples. Physical
stream and queue structures retain a 65,536 guard. The 256 opening backlog
bounds outstanding Yamux handshakes, not lifetime requests. Old protocol peers
and explicit settings may impose lower ceilings.

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

# Agent proxy performance

Agent forwarding reuses origin connections and accounts for public sockets,
requests, tunnel streams and receive credit. Automatic admission uses measured
memory and file-descriptor headroom as well as outstanding resource promises.
Explicit operator limits, destination policy, parser/body limits and TLS trust
still apply.

Read [upgrade and rollback guidance](upgrades.md) before changing server and agent
versions. Mixed-version connections have different resource costs from negotiated
aggregate connections.

## Connection and stream behavior

The default agent configuration uses four bulk TLS/TCP lanes. Peers that negotiate
aggregate flow control also use a separate control lane. Lanes share the agent's
request limit, health identity, revocation and update drain. Transport reuse is
scoped to the route, agent generation and lane; active responses keep their
ownership until body cleanup finishes.

Aggregate receive credit belongs to a bounded session pool. Storage and accounting
are reserved before new credit is advertised. Returning consumed credit makes it
available to other streams; it does not mean the session's promised capacity or
process RSS has fallen. A session can retain grown capacity until retirement.
Legacy peers instead require per-stream receive reservations and a conservative
accepted-stream backlog allowance.

A request can wait for a reusable connection or tunnel admission until the
configured queue deadline or its own earlier deadline. Protected health work has
a separate bounded admission path; local saturation can skip a probe without
classifying the agent as unhealthy. Legacy admission has different queue behavior,
as described in the upgrade notes.

Cancellation, credential rotation and reconnect terminate affected work. An
already committed response cannot become a new HTTP error response: the client
may receive its exact prefix followed by a truncated-body error. Inspect the
recorded error kind as well as the response status. Automatic replay remains
subject to the configured retry rules and request-body replayability.

## Resource settings

Zero-valued public request, client, peer and origin-connection policy limits use
automatic admission or disable the corresponding fixed policy limit. They do not
disable resource accounting. Keep explicit limits when they express a deployment
policy; remove obsolete overrides only after checking their intended effect.

The adaptive controller samples host memory, finite cgroup constraints, an
explicit Go memory limit when present, and process file-descriptor usage against
`RLIMIT_NOFILE`. The default interval is 100 ms. Missing, stale or invalid samples
reject new admissions. Already active socket queues can grow between samples, so
this controller is not a replacement for deployment memory limits.

Linux public, direct-origin and agent-origin sockets use kernel autotuning by
default. `TUNNEL_UPSTREAM_SOCKET_BUFFER_BYTES=0` leaves socket buffers untouched.
A positive value requests fixed buffers, which can constrain WAN throughput.
Automatic sockets carry a conservative 512 KiB lifetime allowance rather than
reserving the operating system's maximum possible queues. Half-close and idle
pooling retain the relevant charge through physical close.

Public connections also reserve parser, TLS and temporary handshake ownership.
The handshake portion returns on activation; retained connection ownership does
not. Requests reserve decoded metadata and applicable upload/body working space.
A low RSS reading alone does not justify reducing those promises. See the
[public parser ownership constraints](proxy-public-parser-ownership.md).

`SERVER_TUNNEL_ESTIMATED_STREAM_BYTES` is a base estimate. Receive ownership is
accounted for separately, including with an explicit override. Review existing
values during upgrades rather than assuming they preserve the former total
per-stream reservation.

Under pressure, bounded asynchronous cleanup can retire eligible idle transport
entries. It preserves active work and refunds ownership after physical cleanup.
A later request may be needed after that cleanup completes. Increasing queue
lengths or retry counts does not create more memory or descriptor headroom.

## Investigating failures and delays

Separate deliberate policy rejection from resource admission failure and upstream
failure. Check the request event's status, error kind, route, target and agent,
then correlate it with memory/descriptor pressure and configured limits. A single
success-rate percentage mixes these causes.

For memory investigations, keep these measurements distinct:

- Process RSS and anonymous memory.
- Cgroup charge, including file cache and kernel socket memory.
- Go heap allocation and retained heap.
- Reserved resource promises, active leases and physical connections.

Idle socket closure and zero active leases do not imply that RSS immediately
falls. Database cache and session receive pools have different lifetimes. Inspect
both server and agent processes; each owns its own budget.

Enable debug traffic tracing for a narrow route/sample when locating latency.
Request events expose `connection_reused`, `tunnel_lane`,
`connection_acquire_ms`, `tunnel_admission_ms`, `tunnel_open_ms`,
`tunnel_handshake_ms`, `agent_dial_ms`, `agent_dns_ms`, `tls_handshake_ms` and
`upstream_first_byte_ms`. These phases overlap: connection acquisition includes
other phases, and first-byte timing starts before acquisition. Do not add them.
Agent dial/DNS values require compatible peers. Reused requests normally have
zero dial/open/TLS time.

## Validation and limits

CI exercises real public HTTPS forwarding over TCP, constrained-memory socket
admission, ownership lifetimes, cancellation/recovery and the local Yamux module.
The network and memory checks are finite integration workloads:

```sh
scripts/test-proxy-wan.sh
scripts/test-proxy-memory.sh
```

Their Linux namespace, Docker and privilege requirements are documented in the
scripts. They are not development servers or continuous production probes.

Historical fixed-load comparisons reached Caddy's throughput range on selected
workloads but remained below the HAProxy throughput target. Component allocation
reductions and correct cleanup do not establish a current whole-proxy throughput
improvement. Capacity depends on protocol, concurrency, payload, network loss,
RTT, recording, memory limits and peer versions; validate the deployment's mix.

Research logs and raw machine captures are maintained outside the source branch.
The source retains regression tests and this operator summary, rather than
embedding benchmark environments or checkpoint archives in release history.

## Local Yamux fork

`third_party/yamux` retains its upstream MPL-2.0 license and tests. In addition to
receive-window growth, compatible peers negotiate the aggregate allocation-credit
extension documented in `third_party/yamux/P2PSTREAM.md`; legacy peers use the
legacy protocol path. This is a maintained protocol fork, not an unchanged
upstream implementation.

The nested module has its own checks because root `go test ./...` does not
traverse nested modules. CI runs its normal suite and `go test -race -short ./...`.
Short race mode excludes upstream's very large transfers; they remain in the
normal suite.

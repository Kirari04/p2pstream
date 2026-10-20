# Agent proxy performance

Agent forwarding reuses origin connections and accounts for public sockets,
requests and tunnel streams. Automatic admission combines measured memory and
file-descriptor headroom with outstanding resource promises. Explicit policy
limits, destination policy, parser/body limits and TLS trust still apply.

Read [upgrade and rollback guidance](./upgrades) before changing server and agent
versions. Resource accounting runs independently in each process; a server's
available capacity does not guarantee that an agent can admit the same work.

## Connections and streams

The default agent configuration uses four parallel TLS/TCP tunnel connections.
They share the agent's request limit. Origin connection reuse is scoped to the
route, authenticated agent generation and tunnel lane. Active responses retain
their ownership until body cleanup finishes.

Each stream reserves its own initial receive storage. Sustained transfers can
grow the receive window only after reserving the additional storage. Advertised
credit cannot be revoked while that stream remains open. Consuming data or
returning a connection to an idle pool does not release the stream's lifetime
reservation or guarantee a fall in process RSS.

Public forwarding can wait for a reusable connection or join the bounded tunnel
admission queue for transient opening, fairness or per-agent capacity constraints.
The request's earlier cancellation or deadline still applies. Memory/descriptor
pressure and full queues can reject new work; increasing queue lengths or retry
counts does not create more resources. Health probes use bounded admission and
can skip local saturation without declaring the origin unhealthy. They share
the existing tunnel connections with public traffic.

Cancellation, credential rotation and reconnect can terminate affected work. An
already committed response cannot become a new HTTP error response: the client
may receive its exact prefix followed by a truncated-body error. Inspect the
recorded error kind as well as the response status. Automatic replay remains
subject to the configured retry rules and request-body replayability.

## Resource settings

Zero-valued public request, client, peer and origin-connection policy settings
select automatic admission or disable the corresponding fixed policy limit.
They do not disable resource accounting. Explicit limits remain ceilings;
resource pressure can admit fewer requests. Keep overrides that express an
intentional deployment policy and review obsolete overrides before removing them.

The adaptive controller samples host memory, finite cgroup constraints, an
explicit Go memory limit when present, and process file-descriptor usage against
`RLIMIT_NOFILE`. The default interval is 100 ms. Missing, stale or invalid resource
samples reject new admissions. Active socket queues can grow between samples,
so this controller is not a replacement for deployment memory limits.

Initial stream reservations distinguish the processes' actual responsibilities:

| Owner | Default initial reservation | Descriptor accounting |
| --- | --- | --- |
| Server logical stream | 918,144 bytes (896.625 KiB), including receive storage and 256 KiB of stream overhead | No new physical socket per logical stream |
| Agent stream and origin dial | 1,442,432 bytes (1,408.625 KiB), including the server-side stream model and a 512 KiB origin-socket allowance | Two descriptors cover the temporary dual-stack dial peak |

These values use the default 512 KiB initial receive-window target, including
chunk rounding and queue overhead. They are accounting promises rather than
eager allocations or measured per-request consumption. Receive-window growth
reserves additional memory separately. Other connection and request owners also
consume the process budget.

`SERVER_TUNNEL_ESTIMATED_STREAM_BYTES=0` selects the derived initial server-stream
reservation. A positive value is a floor for that complete initial reservation:
the larger of the configured estimate and the derived model applies. It cannot
reduce the receive-storage charge. Review explicit values when upgrading;
raising this floor can reduce concurrency even when measured RSS is low.

Linux public, direct-origin and agent-origin sockets use kernel autotuning by
default. `TUNNEL_UPSTREAM_SOCKET_BUFFER_BYTES=0` leaves socket buffers untouched.
A positive value requests fixed buffers, which can constrain WAN throughput.
Automatic sockets carry a conservative 512 KiB lifetime allowance rather than
reserving the operating system's maximum possible queues. Half-close and idle
pooling retain the relevant charge through physical close.

Public connections also reserve parser, TLS and temporary handshake ownership.
The handshake portion returns on activation; retained connection ownership does
not. Requests reserve decoded metadata and applicable upload/body working space.
A low RSS reading alone does not justify reducing those promises. See
[public parser ownership](./proxy-public-parser-ownership).

Under pressure, bounded asynchronous cleanup can retire eligible idle transport
entries while preserving active work. Ownership returns after physical cleanup.
A later request may be needed after that cleanup completes.

## Investigating failures and delays

Separate policy rejection, resource admission failure and upstream failure.
Check the request event's status, error kind, route, target and agent, then
correlate it with memory/descriptor pressure and configured limits. A single
success-rate percentage mixes these causes.

Keep these memory measurements distinct:

- Process RSS and anonymous memory.
- Cgroup charge, including file cache and kernel socket memory.
- Go heap allocation and retained heap.
- Reserved resource promises, active leases and physical connections.

Idle socket closure and zero active leases do not imply that RSS immediately
falls. Allocator retention and database cache have different lifetimes. Inspect
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

The source includes regressions for resource ownership, cancellation, replay,
header bursts and recovery. CI also defines finite real-TCP WAN and constrained
memory integration checks:

```sh
scripts/test-proxy-wan.sh
scripts/test-proxy-memory.sh
```

Their Linux namespace, Docker and privilege requirements are documented in the
scripts. They are not development servers or continuous production probes.
Capacity and throughput depend on protocol, concurrency, payload, network loss,
RTT, recording, memory limits and peer versions. Validate the deployment's mix;
component allocation reductions do not establish whole-proxy throughput gains.

The local `third_party/yamux` module retains its upstream MPL-2.0 license and
tests. Its per-stream receive-window growth uses the existing wire format;
applications reserve memory before advertising extra credit. See
`third_party/yamux/P2PSTREAM.md` for the maintained changes. Root `go test ./...`
does not traverse this nested module; CI defines its normal suite and
`go test -race -short ./...` separately. Large upstream transfers remain in the
normal suite.

# Local Yamux extension

Based on HashiCorp Yamux v0.1.2, distributed under its original MPL-2.0
license (see LICENSE). Upstream source, tests and test fixtures are retained.

The legacy extension gives each stream an atomic receive-window limit and exposes
`MaxReceiveWindow` / `GrowReceiveWindow`, preserving the original Yamux framing.
The separately negotiated aggregate mode described below adds wire semantics
and message types; it must never be enabled for an unacknowledged peer.
Applications must reserve additional memory before granting credit, and hold
that reservation until the stream fully closes. Limits only grow; already
advertised receive credit must not be revoked. No other streams are changed. Explicit growth publishes positive
credit immediately; ordinary reads replenish at a quarter-window threshold to keep
the data pipeline active before credit runs out.

The fork also closes the underlying connection if a receive-credit/SYN update
times out with an unknown send outcome, cleans up failed or reset stream opens,
and rejects non-positive keep-alive intervals. These fixes keep long-lived
sessions from retaining stale stream state or continuing with uncertain credit.

p2pstream uses this to keep small/idle streams cheap while allowing sustained
transfers to grow their windows without multiplying every idle connection's
memory reservation. Upgrade this copy alongside the upstream module version
and run `go test ./...` followed by `go test -race -short ./...` in this
directory when changing it. Upstream's short mode excludes the 250 MiB and
16 GiB stress transfers, which run in the normal suite: the two-minute timeout is not
a portable throughput expectation under the race detector on small CI runners.
The 16 GiB stress fixture retains keepalives but uses the application's 10-second
write timeout instead of the small-fixture 250 ms timeout, which can expire
behind saturated TLS writes on shared runners. Its full byte-count checks and
120-second completion deadline are unchanged; keepalive timeout unit tests keep
their deliberately short timers.

Receive storage is a FIFO of lazy 64 KiB chunks. Drained bulk chunks are
released; a bounded cache of up to 64 chunks (4 MiB) can be reused even while a
queue remains busy. The cache is lazy and is not preallocated when a stream
opens; it is populated only by chunks already charged at an earlier receive
window peak.
`ReceiveWindowMemory` includes payload rounding, queue slack and
chunk metadata, including cached chunks. Callers must reserve its incremental
value when growing a window; the initial charge must also cover its fixed overhead.
`ReceiveBufferStats` exposes buffered bytes and retained accounted storage.
The receiver validates frame credit before allocating and rejects truncated
frames without treating an early EOF as successful receipt.

`Session.RequestRTT` coalesces asynchronous probes, sharing the in-flight gate
with keepalives. `RTT` reports the minimum successful Ping from a bounded
30-second history, avoiding growth driven by queued control frames. The
application measures consumed bytes over elapsed time to decide whether further credit is useful; neither a lifetime byte count nor a sampled
RSS value authorizes growth. RTT probes use the existing Ping framing.


## Negotiated aggregate flow control

`internal/tunnel` owns negotiation outside this library. The authenticated
upgrade must acknowledge the complete, symmetric `aggregate-v1` contract,
including lane role and every window/frame bound, before aggregate configuration
is enabled. The current contract is defined in
[protocol.go](../../internal/tunnel/protocol.go): a 2 MiB initial session payload
pool, a 1 GiB session ceiling, a 512 KiB initial stream allocation allowance,
a 256 MiB stream allocation ceiling, and 32 KiB maximum DATA payloads. Byte
receive windows remain separately limited by operator policy and measured
growth. Allocation credit is not a replacement for byte credit.

Each nonempty DATA frame consumes its payload rounded up to 4 KiB units from
both the session and stream allocation windows. The receiver validates credit
before borrowing chunks from its bounded session-owned pool. A stream's
allocation allowance cannot exceed one quarter of the session limit, preserving
room for other streams when one peer fragments or stalls its data. Pool memory
accounting includes chunk and slab overhead rather than payload bytes alone.

Aggregate sessions retain the ordinary frame-header layout and version byte,
but use session `WINDOW_UPDATE` on stream zero, a new per-stream
`ALLOCATION_WINDOW_UPDATE`, and `WINDOW_GROW` to distinguish newly reserved
capacity from returned credit. New message types are rejected when aggregate
mode is disabled. Growth follows reservation and publication order: reserve
session storage, grow the pool, grow the stream allocation allowance, then
publish any increased byte window. Advertised credit is irrevocable.

Reads and terminal discards return session units asynchronously through one
publisher per session. Live streams also recover their allocation allowance;
terminal streams return only the session half. The publisher combines only
already queued work, at most 16 distinct streams plus one summed session update,
into one bounded priority write. It adds no batching timer or refund threshold;
a lone refund is published immediately. All deltas remain unit-aligned and the
summed session delta fits one uint32 header.

Selected stream controls are acquired in stream-ID order without holding the
shared receive ledger lock, followed by the publication lock. The physical
reader never waits on those publication locks. The sender commits all selected
receive credits before the first header byte can become visible. Partial writes,
timeouts and shutdown races retain every potentially published promise and
abort the session; they never retry a subset or revoke credit. Selected stream
references survive outside the pending queue until commit or joined cleanup,
including streams that receive more pending refunds while a batch is in flight.

The physical sender gathers each DATA header and payload into one reusable
buffer and performs one write in both legacy and aggregate modes. It detaches
borrowed payload storage under the send-item lock before writing, so a caller
returning after timeout or shutdown may reuse its payload. A timed-out queued
header retains its old backing array; callers replace their header before reuse.
Credit is committed before the first wire byte. A short write is fatal, including
a short write without an error; no remainder is retried. Header-only controls
remain separate queue items. The reusable buffer includes allocator rounding:
a 32 KiB payload plus its 12-byte header uses 40 KiB capacity on the qualified
Go toolchain. Larger legacy frames retain their existing payload-copy behavior
with additional header and allocator rounding; this is not a 12-byte resident
memory increase.

The priority queue remains bounded and separate from the DATA/terminal FIFO.
Local FIN and growth serialization is preserved; a reset crossing an already
selected refund remains tolerated without reviving its stream. A registered
credit-wait generation broadcasts to all blocked writers. Updates allocate a
new notification channel only when a writer registered for that generation;
registration and credit inspection share the same lock, preventing lost wakes.

Session-owned initial and growth leases remain held until `Session.Close`
joins the physical reader, refund publisher and sender and clears pool storage.
Application handler lifetime is a separate obligation: the surrounding serving
wrapper joins its handlers before returning their prepaid control resources.
Shutdown reads drain accepted buffered data before returning a terminal error;
empty reads and credit-blocked writes do not spin while cleanup completes, and
read deadline changes still interrupt a pending reader join.

Aggregate lane zero is reserved for authenticated control work and uses bounded
legacy per-stream DATA flow control; aggregate bulk lanes are numbered 1..N.
The application currently uses its protected class for route health probes
under later agent pressure. Ordinary environment/bulk operations and server
hard-pressure admission keep their normal limits. Public input cannot select
this class.
Legacy peers retain their original compatible lane numbering and receive
storage accounting. Missing capability acknowledgment uses the compatible
legacy path, while malformed or conflicting acknowledgment fails closed.
Negotiated rollback replaces and joins the entire tunnel generation before
starting a fresh compatible generation. Unsupported legacy extra workers remain
dormant, and no aggregate frame is sent on their legacy connections.

When changing this extension, retain the normal and race-short suites above,
including allocation conservation, tiny-frame fairness, reader progress,
terminal/growth races, ambiguous publication, mixed-version rollback and
shutdown/deadline tests. Performance retention additionally requires the
unchanged real-endpoint qualification gates; passing protocol tests alone is
not a throughput or latency claim.

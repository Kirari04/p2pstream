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

Aggregate receivers detect stalled holders using **actual allocation units
consumed by Read**, independently of sender fairness. One session monitor ticks
every quarter of `AggregateReceivePressureTimeout` (zero selects one second).
Each nonempty owner must consume one quarter of its stream allocation allowance
per observation interval, capped at 32 units (128 KiB) and floored at one unit.
This is a documented minimum consumption policy only under low pool reserve,
not a maximum transfer duration. Healthy drain/refill counts even when sampled
occupancy remains full. Partial reads that free no unit do not count. Reset,
cancellation, shutdown, growth and credit publication do not count as reading.

Interval counters saturate at the target and are cleared at each observation
boundary, so past high throughput cannot buy an arbitrarily long idle lease.
Only an insufficient completed interval makes an owner eligible for eviction,
and only when free units are at or below one quarter of the pool. New useful
consumption that reaches the target fences a pending eviction. Empty buffers
leave monitoring. With normal monitor scheduling, absence of useful consumption
becomes detectable within two intervals plus two quarter-interval ticks of the
last productive burst; lock contention and physical I/O are not wall-clock
progress guarantees. Small whole-unit trickles below the interval target remain
eligible. The earlier sampled-occupancy-halving policy is superseded.

On each sender, an intrusive FIFO orders **eligible waiting writers** (byte and
per-stream allocation credit available). Only its head may reserve session
credit. Each reservation is at most one 32 KiB DATA frame; a backlogged writer
rejoins behind waiters on the next write step. New arrivals cannot overtake an
existing waiter, and a writer lacking its own credit cannot block eligible
streams. A waiter with N eligible predecessors needs at most N earlier grants,
consuming at most N*32 KiB of returned credit, before its own next positive grant
(minimum allocation quantum 4 KiB). Cancellation, deadline, close and shutdown
remove waiters and wake the next head. This grant-order bound assumes goroutine
scheduling and a functioning transport/refund path; it is not a network latency
bound. It uses existing DATA and credit frames, no protocol negotiation change,
no scheduler goroutine, and one embedded node per active stream. The guarantee
requires the corrected sender; wire compatibility does not repair an old peer's
scheduler. F3's bounded immutable-send ownership and separate memory charge are
unchanged.
Eviction explicitly sets `ErrReceivePressure` on local reads, discards the
owned buffer once, and returns only physically freed units through the existing
refund publisher. This also applies to unread FIN/FIN buffers: graceful close
normally preserves readable bytes, but a pressure eviction reports an error
instead of silently turning discarded bytes into successful EOF. Ordinary
wire RST notifies the peer; negotiation and published window promises do not
change. At most 16 evicted owners await ordered RST delivery. The monitor never
waits on stream I/O: it uses try-locks in state/receive/ledger order and queues
RST only after prior DATA releases the send lock. `Session.Close` joins the
monitor before joining refunds and releasing pool ownership. The fixed worker,
timer, and bounded owner references fit the session metadata envelope.

Both tunnel endpoints run this receiver policy. Bulk lane selection prefers
lanes with free local receive capacity and peer-granted send credit. If all
live bulk lanes are pressured, it retains the existing admission/deadline path
on a bulk lane; pressure is neither disconnection nor permission to use the
protected control lane. A finite set of stalled holders can be evicted over
successive monitor ticks even if growth reservations are denied. A physically
blocked connection or an unbounded supply of newly admitted hostile streams
is outside that same-lane progress guarantee. No pool slab shrinks here:
retirement, growth caps and application disposal auditing remain separate work.

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

Aggregate DATA has an explicit queue-acceptance boundary. Before acceptance,
rejection returns its allocation reservation without consuming byte credit or
establishment flags. After acceptance, the unique send owns an immutable
header/payload frame and completion channel. `Write` returns the accepted byte
count with a timeout or shutdown error if completion is not yet known; callers
must not replay that prefix. Late completion cannot acknowledge another write.
A physical short write or socket error still terminates the session.

At most eight aggregate DATA frames may be preparing, queued or in flight.
`AggregateSendMemory` reserves 328 KiB per aggregate session: eight rounded
40 KiB frame allocations plus 1 KiB of per-send metadata each. Both application
endpoints include this sender charge in `AggregateSessionResourceBytes`, in
addition to receive storage and stream working buffers. Completion releases a
frame's slot only after the socket write returns and its storage is detached;
shutdown joins the sender and fences submissions before clearing queued frames.
This reservation remains session-owned even if the application stream closes.

DATA and FIN/RST share the ordered FIFO. Establishment flags are consumed only
after DATA acceptance, serialized with stream control publication. Pending
aggregate flags survive local Close while a DATA submission is rejected. The
legacy sender retains its borrowed-body/copy-on-timeout behavior and reusable
gather buffer. Receive-credit publications retain their existing fail-closed
behavior on ambiguous publication; a DATA caller timeout is not such a failure.

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

Buffered reads do not publish further per-stream receive credit after peer FIN
or terminal session closure. A FIN or terminal state observed while a credit
write fails also preserves the bytes already read, allowing the remaining
accepted buffer to drain. Live streams and local half-close retain credit-write
error propagation; aggregate storage and session-wide refunds still run before
this terminal per-stream check. A reverse write failure observed before FIN
can still return bytes with an error. These rules do not promise graceful drain
for every ordering of a physical connection failure.

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

Application lane retirement and disposal
----------------------------------------

The application optionally negotiates `X-P2PStream-Lane-Retirement: 1` on a
bulk HTTP upgrade, with an exact owner ID and (for replacement) the ID being
replaced. One bounded server-initiated readiness stream then exchanges the exact
owner ID, an agent acknowledgment and a server commit after routing publication.
Its metadata fits the fixed session envelope and its payload uses the reserved
aggregate pool. Successful readiness uses FIN and preserves an unread commit;
Reset is reserved for failed handshakes because it can discard peer-buffered
bytes even after a physical write completes. Until commit, the agent cannot
retire its old lane. A lost
committed replacement can be retried against its still-owned draining predecessor
when no successor serves; it cannot create another generation allowance. Duplicate/malformed acknowledgments fail closed. This does not change
Yamux framing or reinterpret GoAway: the server already sends GoAway at setup
to reject agent-initiated streams. The agent drives replacement; after the new
lane is ready it sends its own GoAway in the opposite direction. The server
atomically publishes the new routing slot with a local `StopOpening` fence on
the old session. A caller that selected the old slot before that edge may retry
one unopened stream on the current owner. Established streams retain their
exact session and growth owner, even after leaving the serving array.

A single generation registry owns handshaking, serving and draining sessions at
each endpoint. At most two owners occupy a bulk lane. Before admitting its first
session, the endpoint reserves two complete baseline envelopes, including the
2 MiB receive pool, slab accounting, F3's 328 KiB sender reservation, session
metadata and the endpoint's pending-stream metadata. One slot is replacement
headroom, which growth cannot consume. Control owns its separate fixed envelope.
Global admission remains authoritative for these reservations and every growth.

Local growth policy caps each receive pool at 16 MiB (at most a 4 MiB stream
allocation window). Per-lane additional slab charges are shared by all its
owners: the charge for growth from 2 MiB to 16 MiB, rather than a new allowance
for each replacement. All lanes additionally share a 64 MiB generation growth
charge ceiling. Thus the receive/session reservation bound is the control
envelope plus two baseline envelopes per bulk lane plus 64 MiB, with at most
32 bulk lanes. Stream working reservations remain separately charged. These
are upper ceilings, not allocation targets. Measured growth is clamped to the
local lane ceiling; global or generation denial preserves existing credit.
Publication failures retain their leases through physical cleanup.

Every 30 seconds after readiness or a failed attempt, the agent probes whether
either endpoint has grown. The server returns no-content if neither has; there
is no need to replace pristine lanes. With a grown pool, prepaid replacement
headroom permits make-before-break without a new growth-funded reservation.
Failed upgrades leave the old lane serving and retry at the same bounded cadence.
An update cordon rejects replacement; active/draining owners remain included in
update readiness. No third owner is admitted while the old lane drains. A dead
replacement transport may delay recovery while old active work finishes; no
unconditional no-gap guarantee is made under resource denial or broken I/O.

Once replacement is published, old growth is fenced. Idle pooled HTTP sockets
are closed; active HTTP bodies and streams continue. The old session is closed
only after stream creation is fenced, the stream map is empty, and the retained
receive-owner list, borrowed chunks, application owners and pending opens are
empty. FIN/FIN buffered bytes can
outlive the stream map and remain readable. Normal retirement never silently
discards them. An abandoned application connection drains through its cleanup
wrapper; after any terminal drain error it explicitly disposes/reset its
remaining buffer before returning the working lease. Explicit Reset after
FIN/FIN yields a terminal reset error, not successful EOF for discarded bytes.
A late close-timeout callback preserves a completed FIN/FIN buffer; an incomplete
close that times out reports Reset while disposing unread data. This narrowly
shared timeout correction also applies to legacy streams.
Low-reserve pressure eviction retains the documented useful-consumption policy
above; it is not a maximum transfer duration.

Under healthy handshake and completed application drains, a quiescent grown lane
returns to the prepaid baseline within the next 30-second probe, handshake and
two 100 ms cleanup observations. Active streams can extend draining indefinitely
while making useful progress; caps still apply. Shutdown/revocation fences the
registry and joins all physical owners, including replacement and draining
sessions. Server opens pin the exact owner through creation/registration; forced
shutdown closes the socket, joins those opens, and resets registered application
streams (including detached control FIN buffers) before refunding ownership. A fresh authenticated generation cannot start growing while its
predecessor's owners remain unjoined. Close joins Yamux receive, pressure,
refund, sender and slab-preparation ownership; concurrent growth-release callers
also join lease-refund completion. Only then are grown and baseline reservations
returned. Reservation diagnostics include prepaid headroom; they are not RSS.
Physical `AggregateReceiveWindowStats` verifies that retired slabs are cleared.

An aggregate peer without the optional retirement acknowledgment retains its
initial pool but receives no adaptive pool growth; its ordinary compatible
traffic continues. Legacy flow control remains distinct. Full mixed-version
qualification and matched performance measurements are separate from the
retirement correctness regressions.

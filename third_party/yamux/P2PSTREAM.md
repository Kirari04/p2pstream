# Local Yamux extension

Based on HashiCorp Yamux v0.1.2, distributed under its original MPL-2.0
license (see LICENSE). Upstream source, tests and test fixtures are retained.

The local change gives each stream an atomic receive-window limit and exposes
`MaxReceiveWindow` / `GrowReceiveWindow`. The wire protocol is unchanged.
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
RSS value authorizes growth. The wire format remains unchanged.

# Current aggregate stream credit and receive-wait map

This is a read-only source and closed-evidence review. The runtime paths below
are byte-identical between current `HEAD` (`0895fdd8323708612f2137a63e743b79d6410665`,
which adds evidence only) and production source
`8ac4c8df8237e37777ba2fac0da6f949da17bdea`: `git diff` over `internal/`,
`third_party/`, and `go.mod` is empty. It does not authorize a runtime change
or another WAN run.

## Established source facts

1. **Session-pool warmth and logical-stream warmth are different state.**
   Aggregate bulk sessions start with a 2 MiB session pool and each new stream
   with a 512 KiB allocation limit. The stream's byte receive limit also starts
   at 512 KiB because `NewAggregateYamuxConfig` constructs the base Yamux config
   with `InitialReceiveWindow(maximum)` before applying the aggregate allocation
   contract (`internal/tunnel/protocol.go:43-47`,
   `internal/tunnel/yamux.go:264-295`). `newStream` creates fresh byte-window and
   allocation ledgers for every stream (`third_party/yamux/stream.go:83-109`).
   A new `GrowingConn` also creates a fresh `receiveWindowGrowth` epoch; the
   aggregate growth owner is shared only to serialize and retain session-owned
   reservations (`internal/tunnel/growing_conn.go:45-66,148-162`). Thus a grown,
   quiescent physical pool does not pre-seed a new stream's allocation, byte
   window, sample history, or smoothed rate.

2. **Legacy and aggregate streams use the same fresh byte-window ramp, while
   aggregate streams add two more credit gates.** Both current legacy endpoints
   deliberately pass the 512 KiB initial window into the legacy Yamux config
   (`internal/agent/agent.go:929-930,1126-1128` and
   `internal/server/server.go:1114-1122,935-948`). Both wrappers then grow from
   the per-stream initial limit toward the configured maximum. Growth needs two
   supporting samples, grows at most 2x per decision, and resets a sample after
   a pause longer than `max(1s, 8*RTT)`
   (`internal/tunnel/receive_window_growth.go:25-82`). Aggregate growth must
   publish in dependency order: grow the session pool if needed, grow the
   stream allocation, then grow the byte window
   (`internal/tunnel/growing_conn.go:164-212`). A pre-grown session normally
   skips only the first step; it does not skip allocation or byte growth.

3. **Aggregate receive-credit recycling differs concretely from legacy, but the
   difference is intentional protocol behavior.** An aggregate receive reserves
   rounded session and stream-allocation credit before reading a DATA frame
   (`third_party/yamux/aggregate_window.go:334-355`). When the application drains
   complete 4 KiB chunks, it returns storage and queues matching allocation and
   session refunds (`third_party/yamux/stream.go:162-170,266-291`;
   `third_party/yamux/aggregate_window.go:395-427`). A session worker publishes
   those refunds asynchronously, preserving stream/global pairing and wire
   order (`third_party/yamux/aggregate_window.go:538-559,576-639,666-746`). The
   ordinary per-stream byte-window update is still issued synchronously after
   `Read` (`third_party/yamux/stream.go:176-193,426-511`). Legacy mode has only
   the byte-window update. On the sender, all modes first require byte credit;
   aggregate mode additionally reserves session and stream-allocation credit,
   and waits when any required credit is zero
   (`third_party/yamux/stream.go:309-399`;
   `third_party/yamux/aggregate_window.go:211-247`). This is a real semantic
   difference, but source inspection does not show a violated invariant.

4. **The observed `recvLock` wait is not an aggregate-only lock scope.** The
   aggregate physical reader holds `recvLock` while it reads one bounded DATA
   payload into borrowed chunks (`third_party/yamux/stream.go:935-988`). The
   legacy reader likewise holds `recvLock` across its physical payload read
   (`third_party/yamux/stream.go:872-921`). The handler's wait at
   `Stream.Read:156` therefore can mean that the physical reader owns the lock
   while bytes for the next frame are still arriving; it is not by itself
   evidence of CPU lock contention, refund failure, or an aggregate defect.
   When no bytes are buffered, the handler waits at the explicit select at
   `Stream.Read:207-241`. Neither state says whether the sender was credit
   blocked or the physical path merely paced delivery.

## What the closed H1 trace establishes

The closed sustained H1 reductions assign handler waiting as follows (all
times are nanoseconds). The mutex row is the exact `sync.Mutex.Lock ->
yamux.(*Stream).Read:156` stack; the publication row includes receiver-side
credit publication paths.

| sequence | region | empty-receive select | `recvLock` mutex | credit publication | credit-send result |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 9 | 16,834,737,728 | 12,958,344,754 | 2,807,127,932 | 352,591,613 | 3,120,256 |
| 11 | 8,806,179,136 | 7,420,603,133 | 654,966,205 | 127,854,905 | 2,327,872 |
| 14 | 8,775,656,576 | 7,659,473,141 | 562,788,991 | 17,601,724 | 1,786,752 |
| 15 | 7,634,729,216 | 6,981,230,394 | 102,682,048 | 62,531,008 | 717,569 |

The first fresh retained stream therefore spends most of its extra time waiting
for receive data or waiting behind the physical reader, not executing the
receiver's credit-publication functions. That localizes the delay upstream of
the public response write but does **not** distinguish sender credit starvation
from TCP/path pacing. The tiny missing-TransitionStack totals (3.392-12.032 us)
do not affect that conclusion.

The physical tunnel remains a separate variable. Sequence 9 uses physical
remote tuple `10.212.0.2:38456`; sequences 11/15 use `:38434`, and sequence 14
uses `:38428`, even though origin-transport reuse is separately valid. Current
lane selection rotates physical sessions (`internal/server/agent_tunnels.go:90-118`),
so origin reuse and a warmed aggregate pool do not imply identical physical TCP
history. Earlier sparse evidence found slower phases with finite `ssthresh`
249/242 and congestion windows near 3,100 segments versus about 5,900 for
healthy phases, queued sender data, and no sampled retransmitted-byte increase;
it explicitly left TCP startup/HyStart unproven
(`docs/operations/benchmarks/proxy-qualification-2026-10-16/agent-wan-sparse-tcp/README.md:17-32`).
That hypothesis was already followed by a rejected DATA-emission experiment and
must not be reopened as the same intervention.

## Conclusion and smallest discriminator

No aggregate-versus-legacy source defect is established. The concrete protocol
difference is the aggregate sender's additional session/allocation gates and
asynchronous matching refund publication. The receiver trace cannot say whether
those gates explain sequence 9. Existing tagged counters explicitly count
zero-credit *observations* and state that they are neither unique waits nor
elapsed duration (`third_party/yamux/qualification_aggregate_send_counters.go:7-17,43-58`).
Whole-process block profiles likewise cannot align blocked goroutine time to one
selected transfer.

The smallest new discriminator is a qualification-build-only extension of the
existing destructive per-session send counters, with no policy, timeout, frame,
queue, or request change:

* For each entry into `Stream.write`'s existing WAIT, retain a local monotonic
  start time and the cause observed at the decision point: byte window zero,
  stream-allocation zero, session-allocation zero, or their exact bitwise
  combination. Accumulate count, total duration, and maximum duration after the
  existing select wakes. Preserve shutdown/timeout as outcomes rather than
  treating them as credit.
* Separately accumulate count/total/max around the existing DATA
  `waitForSendErr` call. That separates time waiting for usable credit from time
  after DATA enqueue while the physical sender completes the write.
* Drain once immediately before and once after each predeclared selected
  transfer through the existing qualification snapshot endpoint. Bind the
  session identity, lane, physical tuple, reserved payload bytes, and exact
  transfer sequence. Do not poll during the transfer.
* Deterministic, non-WAN controls should independently exhaust byte credit,
  stream allocation, session allocation, both aggregate allocations, and a
  blocked fake physical writer. Each control asserts the expected nonzero cause
  and natural join; elapsed values need only be positive, not match wall-clock
  constants.

If sequence 9's additional roughly eight seconds appears in a credit-wait
bucket, sender credit is the next boundary to inspect. If credit waits are
small while DATA send-completion time is large, the evidence favors physical
lane/TCP pacing. If neither accounts for it, the observer is incomplete. This
proposal measures an unobserved duration; it does not repeat the rejected DATA,
refund queue, epoch, TCP low-water, allocation, growth-policy, or SQL variants.
It should be reviewed only after the matched H2 arm closes.

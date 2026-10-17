# Linux CUBIC and Yamux source review

## Scope and disposition

This is a read-only source interpretation of the frozen `sustained-tcp-state-1`
observation. It does not change source, socket policy, sysctls, qualification
thresholds, or any accepted run artifact. The sustained cohort remains rejected:
H1 is accepted, while H2 is inconclusive because sequences 15 and 16 violated
the fixed 30-second reuse condition and the H2 retained/steady floors failed.

The source review finds no correctness defect and does not support a runtime
tuning change. It narrows the missing evidence to the interval between socket
binding and the selected transfer's pre-snapshot, where `snd_ssthresh` first
changed from its initial value.

## Exact kernel alignment

The running host reported Linux `7.0.0-38-generic #38-Ubuntu`. Ubuntu publishes
source version `7.0.0-38.38`. The official Launchpad source description and
distribution patch are bound in `INPUTS.json`. The Ubuntu patch changes the
relevant `snd_ssthresh` assignments in upstream Linux v7.0 only to
`WRITE_ONCE(...)`; it does not change their values or conditions. Therefore the
upstream v7.0 functions cited below are semantically aligned with the running
Ubuntu source for this question.

All five sender sockets in both arms were captured at owner bind with
`snd_cwnd=10`, `snd_ssthresh=2147483647`, CA state Open, zero retransmissions,
and zero payload bytes sent. The observed H1 values near 460 and H2 values near
240 were therefore created after the existing raw owner bound the live socket,
but before the selected interval's pre-snapshot.

## What can lower or retain the threshold

`tcp_current_ssthresh()` returns the existing threshold during CWR/Recovery.
Outside reduction it returns `max(existing_ssthresh, 3*cwnd/4)`
(`include/net/tcp.h`, upstream v7.0 lines 1481-1500). It cannot lower the
existing threshold outside reduction.

With `tcp_slow_start_after_idle=1`, the first queued data after more than one
RTO and no packets in flight calls `tcp_cwnd_restart()` (`tcp.h` lines
1659-1670). Restart first assigns `snd_ssthresh=tcp_current_ssthresh()` and then
halves only `cwnd` toward the restart window (`tcp_output.c` lines 166-181).
The application-limited validation path uses the same non-decreasing threshold
function before reducing `cwnd` (`tcp_output.c` lines 2117-2132). Consequently,
neither idle restart nor ordinary application-limited validation can explain a
transition from the initial infinite threshold to 240 or 460.

CUBIC's `CA_EVENT_TX_START` handler only shifts its cubic epoch after idle; it
does not assign `snd_ssthresh` (`tcp_cubic.c` lines 142-160). CUBIC does have two
paths that can create the observed finite history:

* HyStart ACK-train or delay detection, while still in slow start and with
  `cwnd >= 16`, assigns `snd_ssthresh=cwnd` and increments the corresponding
  namespace `TCPHystart*` counter (`tcp_cubic.c` lines 386-449).
* Congestion recovery/loss asks CUBIC for its threshold. CUBIC returns 70% of
  the then-current `cwnd` (minimum two), and the TCP loss/recovery paths assign
  that result. Those paths may later return to CA Open, so an Open pre-snapshot
  does not exclude earlier recovery.

Other explicit setters exist (initialization, metrics, MTU and undo paths), so
the retained data cannot label the creator atomically. It does establish that
the H1/H2 threshold difference predates selected timed DATA and is retained
socket history rather than a value produced by the selected interval's idle
restart.

This also separates the two observed effects. H2 sequences 9 and 11 were slow
without any pre-to-post retransmission delta, so loss during those selected
intervals cannot explain their baseline deficit. H2 sequence 14 had eight
pre-to-post retransmissions, which may contribute to its additional delay, but
all retained wait/raw-pair samples reported zero retransmissions and cannot
locate those retransmissions within the interval. H1 sequence 9 passed the
floor despite nine retransmissions observed only by the post-snapshot.

## Yamux receive-credit path

The public read path removes bytes from the aggregate receive buffer, returns
aggregate allocation, releases `recvLock`, and then synchronously calls
`sendWindowUpdate()` (`stream.go` lines 155-176).

The normal update computes the newly available credit and suppresses it until
`delta >= maxReceiveWindow/4`; forced growth is separate. A positive update
commits `recvWindow`, encodes one 12-byte header-only WINDOW_UPDATE, and waits
for its physical send completion (`stream_window_update_send_qualification.go`
lines 10-64; `const.go` defines the 12-byte header).

Aggregate WINDOW_UPDATE frames use the bounded priority `controlCh` (capacity
64). The send loop checks this queue before the bulk FIFO. A control arriving
concurrently can follow at most the single DATA frame already selected, but it
cannot preempt an in-progress TLS/TCP write (`session.go` lines 18-20, 88-96,
465-480; `session_send_loop_qualification.go` lines 20-40 and 77-104).

This path is internally consistent. Quarter-window batching is an explicit
policy, not a source discrepancy. The retained byte-credit trace already shows
that receiver eligibility, enqueue/completion, peer apply/notify, and retry are
locally fast once each event occurs. The missing time is before eligibility and
inside the shared transport history; source alone does not justify lowering
the quarter-window threshold or changing queue/socket policy.

## Smallest new discriminator

If another observation is approved, the narrow source change should extend the
existing qualification-only raw owner rather than add a poller or background
sampler:

1. Starting from the already captured live bind snapshot, take synchronous
   TCP_INFO snapshots immediately before and after existing DATA raw writes
   only until that owner's first `snd_ssthresh` transition, then stop.
2. Retain a fixed record containing the last equal-threshold sample, the first
   differing sample, raw ordinal, owner/session/stream identity, monotonic
   brackets, provider cost, CA state, cwnd, ssthresh, retransmission counters,
   bytes sent/acked, and `last_data_sent`. A fixed attempt cap must make a
   never-transitioning socket fail inconclusive rather than sample forever.
3. At the existing serialized transfer boundaries, record fixed namespace
   `TCPHystartTrainDetect`, `TCPHystartTrainCwnd`,
   `TCPHystartDelayDetect`, and `TCPHystartDelayCwnd` values. These are emitted
   by the exact CUBIC assignment sites. Because they are namespace-wide, use
   them only when one owner transition and no competing transfer fall inside
   the boundary; otherwise mark attribution ambiguous.

This observation would bracket where 2147483647 became approximately 240 or
460. A matching HyStart counter increment with no per-socket retransmission
advance would support HyStart creation. A retransmission/recovery advance
without HyStart would support the loss path. Neither result would by itself
prove that the retained threshold caused later throughput; it would identify
the history creator. No new WAN run, timeout adjustment, LOWAT experiment, or
runtime policy change is justified until that creator is observed.

## Limits

TCP_INFO snapshots are discrete and cannot prove that an unobserved kernel
state never occurred. Namespace MIB counters are not per socket. The control
frame queue ordering does not establish kernel delivery time. Wall clocks are
not subtracted across processes. The existing run remains a single matched
observation, and no causal or parity claim follows from it.

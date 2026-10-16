# Stream warmth and sustained schedule map

Scope: read-only interpretation of the current production runtime at
`8ac4c8df8237e37777ba2fac0da6f949da17bdea`. The cited runtime files are
byte-identical between that commit and the active worktree. This map does not
propose another growth trace, allocation/growth-policy experiment, or workload.

## Source facts

### A reused HTTP connection retains one logical Yamux stream

The route transport key includes the round-robin tunnel lane. One pooled
`http.Transport` is retained per key, with keepalives enabled and a 30 second
idle timeout. Its `DialContext` opens a Yamux stream only when net/http needs a
new connection (`internal/server/agent_transport_pool.go:109-124,159-203,
457-503`; lane selection is `internal/server/agent_tunnels.go:90-118`).

The dial path calls `session.Open()` once and wraps that stream in one
capacity-managed connection containing one `GrowingConn`
(`internal/server/public_routing.go:1001-1020` and
`internal/server/agent_request_limiter.go:40-65`). On the peer, the accepted
stream is likewise wrapped in one `GrowingConn`, then connected to one origin
socket for the complete raw relay lifetime (`internal/agent/agent.go:1359-1376,
1447-1510`). Therefore a successful net/http keepalive reuse preserves:

- the same Yamux stream on both endpoints;
- each endpoint's stream byte window and aggregate allocation window;
- each endpoint's `GrowingConn.growth` estimator; and
- the origin TCP connection behind that stream.

These objects end when the pooled connection/Yamux stream ends, not at an HTTP
request boundary. A new HTTP connection on an already-grown physical lane gets
a new logical Yamux stream and new per-stream growth state.

### A grown physical session does not make a new stream warm

Every aggregate stream starts with 256 KiB of Yamux byte credit, a 512 KiB byte
receive cap, and a 512 KiB allocation receive limit. Aggregate session
construction sets `MaxStreamWindowSize` to `InitialReceiveWindow`, whose
production cap is 512 KiB; Yamux's fixed initial byte credit is 256 KiB
(`internal/tunnel/yamux.go:14-34,264-295`;
`internal/tunnel/protocol.go:43-47,99-106`;
`third_party/yamux/const.go:147-148`;
`third_party/yamux/stream.go:83-109`). The physical session pool starts at
2 MiB. Negotiated ceilings are 1 GiB for the session and 256 MiB for one
stream's allocation. Although the operator byte-window ceiling is 1 GiB by
default, `NewAggregateGrowingConn` clamps its effective stream ceiling to the
256 MiB allocation maximum (`internal/tunnel/growing_conn.go:45-66`).

The lane's physical aggregate pool and its reservations live for the Yamux
session, across logical-stream closure. A new stream still starts with fresh
256 KiB advertised byte credit, 512 KiB byte/allocation caps, and a fresh
growth estimator. The estimator
requires two supporting samples, caps one increase at a doubling, never
shrinks, and resets a burst after a pause or blocked read longer than one
second at the configured 80 ms RTT (`internal/tunnel/receive_window_growth.go:
13-82`).

For each accepted target, aggregate growth computes a session target of four
times the proposed stream allocation. If that target is already within the
retained physical pool, it skips the adaptive reservation, slab allocation,
and session-window publication. It still publishes the new stream allocation
and byte-window limits synchronously (`internal/tunnel/growing_conn.go:
164-212`; `third_party/yamux/aggregate_window.go:759-806,809-850`;
`third_party/yamux/stream.go:535-548`). Thus a pre-grown lane removes the
session-memory growth path, but it does not remove a cold logical stream's
demand measurement or its per-stream credit ramp.

When the retained pool is insufficient, server-side session growth reserves
from the server receive-window owner and agent-side growth reserves from the
agent aggregate-growth owner (`internal/server/server.go:1013-1017`;
`internal/agent/agent.go:1138-1150`;
`internal/agent/adaptive_capacity.go:358-393`). Those reservations belong to
the physical session, not the individual HTTP request.

The sustained row boundary reports only the physical session receive limit.
It cannot reveal a retained stream's byte or allocation limit. A large, fully
available session pool therefore does not prove that the next logical stream
starts with a large per-stream window.

## Exact post-close schedule

The first eight transfers explicitly close idle agent transports after each
row. Rows 9-12 then create one persistent connection on lanes 1-4 in order;
rows 13-16 revisit those four lane-specific keys in order. The direction that
receives the 256 MiB body determines which endpoint's stream is warm:

| Sequence | Lane/key visit | Large-body receiver | State at start |
| ---: | --- | --- | --- |
| 9 download | lane 1, new | server | New server stream receiver: cold 256 KiB byte credit / 512 KiB caps; lane pool already grown by warmups. |
| 10 upload | lane 2, new | agent | New agent stream receiver: cold 256 KiB byte credit / 512 KiB caps. Server receives only the 32-byte response. |
| 11 download | lane 3, new | server | New server stream receiver: cold 256 KiB byte credit / 512 KiB caps. |
| 12 upload | lane 4, new | agent | New agent stream receiver: cold 256 KiB byte credit / 512 KiB caps. |
| 13 upload | lane 1, reuse of 9 | agent | Same stream, but its agent receiver previously saw only the small GET request: direction-cold. |
| 14 download | lane 2, reuse of 10 | server | Same stream, but its server receiver previously saw only the 32-byte upload response: direction-cold. |
| 15 download | lane 3, reuse of 11 | server | Same stream and same large receive direction: byte/allocation limits and estimator persist warm. |
| 16 upload | lane 4, reuse of 12 | agent | Same stream and same large receive direction: byte/allocation limits and estimator persist warm. |

This predicts a schedule effect: row 15 should avoid the server-side stream
ramp paid by rows 9, 11, and 14, and row 16 should avoid the corresponding
agent-side ramp paid by rows 10, 12, and 13. It can explain a recurring
new/direction-cold versus same-direction-reused shape. It cannot by itself
explain a public-H2-specific deficit because H1 and H2 use the same lane and
stream schedule. The copy timing attribution must show whether the additional
H2 wall time is carried by upstream `Read` or downstream `Write`.

There is a narrower state distinction for rows 13 and 14. The receiver that
will later take the large body first sees only small opposite-direction HTTP
traffic, followed by a long idle period. On the first later large-body read,
the estimator sees an epoch older than one second,
resets, and returns without proposing growth
(`internal/tunnel/receive_window_growth.go:29-45`). Rows 9 and 11 instead begin
with a newly constructed estimator. This reset discards only that read's demand
sample; it does not reset already-advertised credit and is not evidence for a
multi-second stall.

## Existing diagnostic boundary

This schedule is not a new growth mechanism. The preserved growth trace used
the same four fresh warmups, retained rows, and alternating timed order
(`docs/operations/benchmarks/proxy-qualification-2026-10-16/
agent-wan-growth-trace/README.md:30-36`). In its slow 18.548 second retained
download, the stream reached about 3.938 MiB by 1.85 seconds, then remained near
6-8 MB/s; the 16 MiB proposal arrived at 15.918 seconds. RTT remained about
80.183 ms, with no reservation denial or publication error
(`README.md:52-84`). The later allocation-counter diagnostic found about
1.00414 allocation bytes per payload byte, refuting roughly eightfold 4 KiB
rounding amplification (`README.md:86-92`).

That evidence already excludes reservation denial, unknown RTT, and allocation
rounding amplification for the captured historical case. It does not make its
pre-current runtime a same-source control, and it does not identify whether
slow downstream consumption caused the delayed demand-based growth. The current
copy-boundary totals should be interpreted together with the schedule above;
they do not justify repeating the closed growth/allocation experiments.

## Current attribution consequence and source discrepancy

The supplied completed timing totals put the severe H2 row-14 deficit inside
upstream `Read` (17.334 seconds versus 0.803 seconds in downstream `Write`).
Rows 9 and 11 are also direction-cold but have roughly seven-second H2 read
totals. Direction-cold stream state is therefore a real conditioning variable
and fits row 15's fast same-direction reuse, but it is not sufficient to cause
the severe H2 event. The final audit still owns the exact reuse attestation.

The concrete reporting discrepancy is that the runner labels steady downloads
`all-server-receive-lanes-prewarmed-and-quiescent` even though it proves only
that each *physical session pool* is grown and quiescent. It does not prove the
selected logical stream's byte/allocation windows are warm
(`tmp/continuation-20261016/wan-protocol-source/runs/sustained-matched-1/
preflight/runner-rebuilt.sh:690-706`). Existing row-boundary lane stats expose
session pool limits only. They cannot confirm the per-stream distinction above.
The bounded check is therefore offline: retain the exact connection-key/reuse
audit and interpret rows by prior receive direction; do not treat the existing
"prewarmed" label as per-stream evidence.

## Hypothesis, kept separate from the facts

Logical-stream warmth is a plausible explanation for row 15 being uniformly
fast while rows 9, 11, and 14 pay some ramp cost. If H1 and H2 have similar
upstream-read ramp shapes but H2 alone accumulates much more downstream-write
time, stream warmth is conditioning rather than the H2 cause. If the excess is
instead in upstream reads on direction-cold rows and disappears on the
same-direction reuse, the source supports a per-stream credit/demand ramp, but
the fixed copy metrics still cannot distinguish byte credit, allocation credit,
publication scheduling, origin delivery, or tunnel scheduling without internal
time-aligned state.

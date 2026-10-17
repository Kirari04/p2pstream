# Reverse-proxy qualification evidence, 2026-10-16

These artifacts preserve the first bounded reverse-proxy qualification pass.
They support workload-specific conclusions only. The loopback origin and load
generator ran on the same 16-CPU host; proxy processes were pinned to CPUs
`0-3`, load/origin work to CPUs `4-15`, and Go proxies used `GOMAXPROCS=4`.
HAProxy used `nbthread 4` on the same proxy CPU set. p2pstream capacity runs
used a 4 GiB Docker cgroup with swap disabled. All successful responses were
validated by expected length and content or hash before counting as useful.
On this host logical CPUs `0-1` share physical core 0 and `2-3` share physical
core 1. The proxy set therefore supplies four matched logical CPUs on two
physical cores to both p2pstream and HAProxy. A later distinct-core diagnostic
may improve absolute throughput, but does not retroactively change this matched
comparison.

## Files

- [`current-production-wan/README.md`](current-production-wan/README.md)
  records the original H2 WAN workload against current source `8ac4c8d` and a
  rebuilt compatible legacy agent. The required fresh three-pair cohort fails
  download in pairs 1 and 3 with all 96 payloads, events and lifecycle exits
  exact; a separate passing single pair does not clear the gate.
- [`matched-verified-wan-protocol/README.md`](matched-verified-wan-protocol/README.md)
  records a separate 32-row matched verified-certificate H1/H2 diagnostic on
  the same source checkpoint with production files byte-identical. H1 passes
  every 29.7 MB/s phase floor; H2 fails retained-warmup at 14.242 MB/s and
  steady state at 27.327 MB/s. Exact protocol, reuse and natural cleanup checks
  pass in both arms. This diagnostic does not clear the original WAN gate.
- [`matched-verified-wan-copy-attribution/README.md`](matched-verified-wan-copy-attribution/README.md)
  preserves the failed and corrected copy-timing fixtures and a one-shot
  32-row sustained diagnostic. H1 fails two download floors. H2 retains the
  exact key and origin on its slow sequence 14, then expires the intended
  sequence-15/16 revisits; its upstream-reuse classification is inconclusive.
  The observed `Read` duration does not establish a cause or clear the WAN gate.
- [`matched-verified-wan-read-wait-trace/README.md`](matched-verified-wan-read-wait-trace/README.md)
  preserves completed tiny H1 and H2 execution-trace diagnostics, their
  original analyzer failures, separate offline reductions and independent
  handler-state partitions. It retains no runtime change and does not establish
  a cause or clear the WAN gate.
- [`matched-verified-wan-read-wait-stack-intern/README.md`](matched-verified-wan-read-wait-stack-intern/README.md)
  preserves focused compact-format validation, both rejected 16-row sustained
  observations, separate generation-status reductions and exact omitted-H2
  trace bindings. Production source remains unchanged; the package establishes
  no cause or runtime fix and clears no qualification gate.
- [`matched-verified-wan-sender-wait-observer/README.md`](matched-verified-wan-sender-wait-observer/README.md)
  preserves five offline observer-validation attempts, including rejected
  ordinary-code differences and the final passing normal/race, schema and
  exact code/layout proof. It contains no endpoint traffic or runtime fix and
  clears no performance qualification gate.
- [`matched-verified-wan-sender-wait-integration/README.md`](matched-verified-wan-sender-wait-integration/README.md)
  preserves the authenticated smoke, sustained H1/H2 failure, sender closure
  records and read-only stream-lifetime analysis. Production source remains
  unchanged; the package establishes no cause or runtime fix and clears no
  qualification gate.
- [`sql-hierarchical64-rejected/README.md`](sql-hierarchical64-rejected/README.md)
  preserves the rejected 64→16→single event-insert screen. Large flushes improve,
  but a smaller-flush pair exceeds the fixed regression ceiling; no code is retained.
- [`native-go-profile/README.md`](native-go-profile/README.md)
  records one matched production/native CPU/allocation pair, per-completion
  costs, natural cleanup and an independent binding of preserved metadata.
  SQL batching is an experiment target; no production change is retained.
- [`native-go-attribution/README.md`](native-go-attribution/README.md)
  records 24 matched HTTP/HTTPS H1 rows and 8,920,049 exact responses.
  Production reaches 64.9–70.5% of the deliberately simpler native Go control;
  all four preregistered profiling triggers pass.
- [`agent-wan-http-stage/README.md`](agent-wan-http-stage/README.md)
  preserves sixteen healthy native HTTP/1 agent transfers, the original
  pool-category validator failure and a strict, separately hashed offline replay.
  Public HTTPS/H2 remains the next diagnostic boundary; the WAN gate is open.
- [`yamux-terminal-buffered-drain/README.md`](yamux-terminal-buffered-drain/README.md)
  records the terminal credit-write fix, deterministic failing baseline and
  passing full normal/race-short suites. Its scope is buffered drain after
  observed FIN or terminal session state; the WAN regression remains open.
- [`final-runtime-reference/README.md`](final-runtime-reference/README.md)
  records the fresh `248bad0` matched 36-row HTTPS reference. Candidate reaches
  101–120% of Caddy and 34–67% of HAProxy; all HAProxy 80% gates fail.
- [`final-runtime-moderate-latency/README.md`](final-runtime-moderate-latency/README.md)
  preserves 36 equal-offered-load HTTPS latency rows with exact delivery and
  zero errors. All four Caddy-relative gates pass; all four HAProxy gates fail.
- [`agent-wan-transport-ceiling/README.md`](agent-wan-transport-ceiling/README.md)
  preserves the complete standalone Yamux/raw TLS 64-transfer nonreproduction,
  exact ownership and cleanup checks, and separate original fixture failures.
  Both standalone Yamux arms remain healthy; the original WAN failure is open.
- [`agent-relay-cancellation/README.md`](agent-relay-cancellation/README.md)
  records focused and complete agent/tunnel normal and race passes for joined
  cancellation cleanup. This lifecycle fix does not clear the WAN failure.
- [`final-unchanged-module-races/README.md`](final-unchanged-module-races/README.md)
  preserves passing frozen-source Yamux and system-metrics race checks. Their
  reuse requires byte-identical inputs. The terminal-drain fix changes Yamux,
  which now has its own new module checks; final integration races remain pending.
- [`final-runtime-tiny-response/README.md`](final-runtime-tiny-response/README.md)
  records the final `dc738e9` low-latency 24-row tiny-response pass.
- [`final-runtime-standing-pools/README.md`](final-runtime-standing-pools/README.md)
  records the final-runtime 16- and 32-agent idle standing-pool passes.
- [`final-runtime-wan-failure/README.md`](final-runtime-wan-failure/README.md)
  preserves the final-runtime large-transfer download regression, its nonzero
  outcome and a separate derived diagnostic summary.
- [`final-runtime-wan-stability/README.md`](final-runtime-wan-stability/README.md)
  repeats the unchanged workload across three fresh alternating endpoint pairs.
  All three independently reproduce the download regression with exact payload
  and event conservation.
- [`agent-wan-tcp-diagnostic/README.md`](agent-wan-tcp-diagnostic/README.md)
  records a separate frozen-binary TCP/profile diagnostic that did not
  reproduce the stall and does not supersede the failed acceptance cohorts.
- [`agent-wan-credit-diagnostic/README.md`](agent-wan-credit-diagnostic/README.md)
  records a second healthy nonreproduction with tagged cheap credit snapshots
  and TCP sampling, without native profiling. It does not clear the failures.
- [`agent-wan-growth-trace/README.md`](agent-wan-growth-trace/README.md)
  captures slow individual downloads with deferred bounded growth traces.
  Its full cohort median passes; it does not clear the repeated failure.
- [`agent-wan-allocation-counters/README.md`](agent-wan-allocation-counters/README.md)
  captures the download failure with post-transfer DATA reservation counters.
  Roughly 32 KiB frames and a 1.00414 charge/payload ratio refute the proposed
  eightfold allocation-amplification mechanism without establishing a fix.
- [`agent-wan-sparse-tcp/README.md`](agent-wan-sparse-tcp/README.md)
  records an ordinary-binary diagnostic with one TCP capture per second at
  each endpoint. Two mildly slower downloads have finite early slow-start
  thresholds and no observed retransmission increase; the cohort median
  passes and does not clear the repeated qualification failures.
- [`agent-wan-data-emission-causal/README.md`](agent-wan-data-emission-causal/README.md)
  preserves a fixed 96-transfer gather-versus-split DATA emission comparison.
  The split overlay fails its retention gate in two of three download pairs;
  all payloads and events are exact, and no production change is retained.
- [`agent-wan-runtime-bisection/README.md`](agent-wan-runtime-bisection/README.md)
  compares current and historical Yamux at both endpoints with one current
  application, harness and driver. All 96 transfers are exact, but the
  historical arm fails the every-pair download improvement threshold. Its
  steadier phase distribution is a diagnostic clue, not a qualified fix.
- [`agent-wan-refund-emission-causal/README.md`](agent-wan-refund-emission-causal/README.md)
  preserves 96 exact transfers comparing gathered and split refund-control
  writes. The primary improvement gate fails in two pairs. A separate
  all-phase transition screen narrowly passes; no production fix is retained.
- [`agent-wan-refund-queue-ack-causal/README.md`](agent-wan-refund-queue-ack-causal/README.md)
  preserves a following 96-transfer queue/acknowledgment comparison. All transfers
  are exact, but two pairs fail the primary improvement gate. Its all-phase
  floor screen passes while a smaller transition penalty remains.
- [`agent-wan-refund-epoch-causal/README.md`](agent-wan-refund-epoch-causal/README.md)
  preserves 96 exact transfers for bounded deferred-global scheduling. Both
  diagnostic screens fail; the directional transition penalty remains and no
  production publisher change is retained.
- [`agent-wan-tunnel-lowat-causal/README.md`](agent-wan-tunnel-lowat-causal/README.md)
  preserves a 96-transfer physical-tunnel socket comparison with verified
  settings and 12 stable paired socket snapshots. Download performance is effectively
  unchanged, both timing screens fail, and no option change is retained.
- [`caddy-reference/moderate-runner-validation/README.md`](caddy-reference/moderate-runner-validation/README.md)
  records 19 synthetic calibration/schedule checks for the separate
  moderate-load latency comparison. Actual results are in
  `final-runtime-moderate-latency/`.
- [`caddy-reference/moderate-caddy-extension/README.md`](caddy-reference/moderate-caddy-extension/README.md)
  preregisters 12 additional Caddy rows at the unchanged candidate/HAProxy
  offered rates, preserving the original 24-row comparison. Its 28 synthetic
  contracts and bounded wrapper cleanup checks pass; the fresh 36-row
  performance outcome is in `final-runtime-moderate-latency/`.
- [`caddy-reference/matched-h2-fixture-validation/README.md`](caddy-reference/matched-h2-fixture-validation/README.md)
  records the bounded fixture checks for a future reference comparison with
  one prewarmed H2 client connection per proxy. It is not a performance result.
- [`caddy-reference/README.md`](caddy-reference/README.md) documents the clean
  36-row Caddy/HAProxy/p2pstream reference cohort. Its p2pstream binary is the
  frozen `3ec137c` candidate; later library and protocol foundations in `0d6a3a8`
  were not present in that binary.
- [`recorder-batch/README.md`](recorder-batch/README.md) records the bounded
  prepared-recorder batch experiment and its focused correctness checks.
- [`post-batch-profile/README.md`](post-batch-profile/README.md) records the
  clean post-batch direct-path CPU, allocation and contention profiles.
- [`direct-cache-readhit/README.md`](direct-cache-readhit/README.md) records
  the rejected direct-target cache read-hit experiment and its no-gain result.
- [`lower-base-capacity/README.md`](lower-base-capacity/README.md) preserves
  the exact constrained 1,536-request lower-base cohort, mixed-version bursts,
  provenance and raw machine-readable rows.
- [`agent-idle-lifecycle/README.md`](agent-idle-lifecycle/README.md) follows
  1,536 retained aggregate streams through natural and forced idle cleanup
  without disconnecting the five physical tunnel lanes.
- [`agent-idle-lifecycle-four/README.md`](agent-idle-lifecycle-four/README.md)
  repeats that cleanup across four external agents and 20 physical lanes.
- [`agent-wan-growth/README.md`](agent-wan-growth/README.md) records the
  80 ms / 1 Gbit/s aggregate-versus-legacy large-transfer comparison and exact
  server- and agent-side receiving-pool growth.
- [`agent-grown-pool-pressure/README.md`](agent-grown-pool-pressure/README.md)
  records the greater-than-80-MiB-per-lane pressure, cold reserved-control,
  canceled-upload ownership and recovery qualification in separate 4 GiB
  server and agent cgroups.
- [`agent-tiny-response/README.md`](agent-tiny-response/README.md) records the
  failed 1 ms aggregate-versus-legacy tiny-response gate across H1/H2 at 16
  and 128 concurrent requests, with exact payload and event conservation.
- [`agent-tiny-response-post-refund/README.md`](agent-tiny-response-post-refund/README.md)
  repeats that gate after the paired-refund and shutdown-waiter checkpoint.
  H1 at 16 clients passes, but the overall 24-row cohort still fails.
- [`agent-tiny-response-lazy-wake-h2c128/README.md`](agent-tiny-response-lazy-wake-h2c128/README.md)
  records the subsequent H2/c128-only lazy-wake screen. It passes throughput
  but fails both tail gates, so the full matrix was not rerun.
- [`agent-tiny-response-refund-batch/README.md`](agent-tiny-response-refund-batch/README.md)
  records the batched-refund H2/c128 screen and full matrix. The screen passes;
  the full matrix fails only the H2/c128 p95 gate.
- [`agent-tiny-response-data-gather/README.md`](agent-tiny-response-data-gather/README.md)
  records the DATA-gather H2/c128 screen and full 24-row pass, with exact
  request and event conservation and a later-runtime revalidation caveat.
- [`agent-tiny-profiles/README.md`](agent-tiny-profiles/README.md) records native
  CPU, allocation, mutex and block profiles of the first failed tiny-response
  candidate and its matched legacy endpoint.
- [`agent-post-batch-profiles/README.md`](agent-post-batch-profiles/README.md)
  records the post-refund-batch native CPU and allocation profiles that guided
  the bounded DATA gathering change.
- [`proxy-public-parser-ownership.md`](../../proxy-public-parser-ownership.md)
  records the separate public-parser ownership decision and the bounds that
  still require implementation and qualification.
- [`capacity-boundaries-cold/README.md`](capacity-boundaries-cold/README.md)
  records cold-start direct and remote-agent H1/H2 constrained boundaries.
- [`capacity-established-h2/README.md`](capacity-established-h2/README.md)
  records the distinct pre-established remote-agent H2 3,072-request pass.
- `capacity-and-profile.jsonl` is the initial candidate `095c923` correctness,
  fixed-rate, streaming, H1/H2 1,536-barrier and recovery pass.
- `warmed-throughput.jsonl` is one alternating baseline/candidate pass with a
  two-second warmup and ten-second unbuffered closed-loop timed interval.
- `stdlib-and-postsql-diagnostic.jsonl` is the later `d3767c9` H1 diagnostic
  against default `httputil.ReverseProxy` controls and HAProxy.
- `stdlib-pooled-diagnostic.jsonl` uses the same reusable 32 KiB copy-buffer
  pool as production.
- `final-h1-profile.jsonl` is the profiled `b4dd36b` H1 comparison. Its raw CPU,
  heap and allocation profiles remain in the ignored local run directory named
  in the operations report; profile binaries are not checked in.
- `ballast-capacity-boundary.jsonl` contains the H1 agent 1 GiB-ballast boundary
  search: baseline and candidate at 1,536, then candidate points at 1,024,
  1,280 and 1,400. The highest measured candidate pass is 1,280 and the lowest
  measured failure is 1,400.
- `ballast-capacity-corrected.jsonl` supersedes the failure-row status counts
  with a three-second barrier hold, exact HTTP status codes and a separate
  512-request recovery row after each capacity point.
- `h1-concurrency-sweep.jsonl` compares the `d1d952f` context-reuse candidate,
  pooled standard-library controls and HAProxy at 1, 4, 16, 32, 64, 128 and
  256 closed-loop clients. The 128-client candidate row was CPU/allocation
  profiled.
- `lowat-validation.jsonl` is the sanitized paired socket-memory, controlled
  pressure and WAN result set for `TCP_NOTSENT_LOWAT`, preserving byte counts
  and MiB conversions separately.
- `post-forwarding-qualification.jsonl` contains the clean `ca8a081` H1
  allocation/CPU/contention diagnostics, one full baseline/candidate/control
  regression matrix and the 500 request/s offered-load tail check. The full
  matrix rows carry an explicit note because a competing compile briefly
  overlapped the late candidate agent phase; use those rows for correctness and
  regression visibility rather than decisive agent throughput.
- `stdlib-transport-sharding.jsonl` is the test-only pooled Go transport
  diagnostic at 16 and 128 clients. Each row records its shard count, total and
  per-shard idle budgets in `resource_envelope`, plus newly opened origin TCP
  connections after warmup.
- `production-idle-pool.jsonl` compares the unchanged single production
  transport at 32 idle connections per origin with an isolated one-line 128
  overlay. It includes low/high-concurrency H1/H2, alternating repeats, a 1 GiB
  ballast retention/recovery run and TLS-origin runs with negotiated origin
  protocol recorded.
- `external-agent-smoke.jsonl` is a host-process correctness smoke for the
  separate-process fixture. The frozen `3ec137c` legacy overlay established one
  control plus three bulk lanes; the `1418407` candidate explicitly negotiated
  aggregate-v1 with one control plus four bulk lanes. The first pair and the
  final `81f5bc6` harness pair each completed and persisted 100/100 responses.
  The final pair also records matched per-process sampling intervals and passed
  the fail-closed mode, lane, payload and recorder-conservation checks. These
  rows are not timing or cgroup-capacity evidence.

## Latest-runtime status

The latest measured original-H2 WAN runtime in this evidence set is current
source `8ac4c8df8237e37777ba2fac0da6f949da17bdea`, including the `4477c0b`
terminal buffered-drain fix. Its required fresh alternating three-pair cohort
fails download in pairs 1 and 3: aggregate/legacy ratios are 81.10%, 101.98%
and 81.10%; uploads pass at 98.57%, 99.73% and 99.51%. The pooled download
ratio also fails at 81.67%. All 96 transfers are exact HTTP/2 200 responses
with exact payload and persisted-event conservation, and all 15 endpoint,
origin and namespace-helper process exits are natural and joined. A separate
32-transfer pair passes both directions but is not combined with the required
cohort. The source contains `4477c0b`; the comparison does not establish that
change as the cause.
It uses the historical `InsecureSkipVerify` IP-URL/no-SNI load configuration,
so it is not matched verified-certificate evidence. The complete evidence is
in [`current-production-wan/`](current-production-wan/README.md). The WAN gate
remains failed, and the result does not support readiness, merge, release,
deployment or a broad proxy-parity claim.
A separate matched verified H1/H2 diagnostic on the same source checkpoint is
complete. Its production files are byte-identical; its overlays only add the
bounded test transport selection, TLS observations and trace correlation. H1
passes all three independent 29.7 MB/s download-phase floors, with minima of
33.592 MB/s warmup, 29.729 MB/s retained warmup and 30.034 MB/s steady state.
H2 passes warmup at 33.586 MB/s but fails retained warmup at 14.242 MB/s and
steady state at 27.327 MB/s. All 32 rows have exact HTTP 200 payloads, verified
certificate and ALPN observations, event conservation and trace correlation.
The four actual retained revisits in each arm preserve the same bound origin
connection after 23.94–27.71 seconds idle, and every endpoint and namespace
exit is natural and joined. The H2 runner returns 1 after cleanup solely for
the failed floors. This is a failed protocol diagnostic, not a causal finding
or original-gate clearance; HAProxy, pressure, capacity and integration gates
remain separate. See the
[matched verified H1/H2 evidence](matched-verified-wan-protocol/README.md).

A bounded follow-up adds same-clock response-copy timings without retaining a
runtime change. Its single 16-row-per-arm sustained cohort is rejected. H1 is
observationally complete but falls below 29.7 MB/s at sequences 9 and 11
(29.452 and 29.673 MB/s). H2 sequence 14 reuses the exact key and origin, but
reaches only 14.550 MB/s; 17.334 seconds of its 18.305-second handler interval
are observed in upstream `Read`, versus 0.803 seconds in downstream `Write`.
That delay moves the intended sequence-15/16 revisits to 34.588/35.001 seconds
idle, beyond the unchanged 30-second deadline, and both open new origins. H2
therefore remains `inconclusive-upstream-reuse`. All 32 payloads, protocols,
certificate observations, events and copy totals are exact, and all ten
process exits are natural. This localizes an observed boundary without
establishing a cause. The original 90% WAN blocker
and the HAProxy, pressure, capacity and integration gates remain open. This
result prompted the completed bounded read-wait trace follow-up described
below. See the [copy-timing evidence](matched-verified-wan-copy-attribution/README.md).

A bounded read-wait trace follow-up also retains no runtime change. The H1 tiny
arm completed 13 rows with natural lifecycle and clean resources, but its
original analyzer rejected all three selected traces. The standalone H2 arm
completed 13 exact HTTP/2 rows with verified TLS, reuse, trace, copy, resource
and lifecycle evidence; its original analyzer statuses are 1, 1 and 0, so the
arm, validation and wrapper remain failed. Separate offline reductions return
0 for all three traces in each arm while remaining attribution-incomplete.
Independent audits conserve every handler-region nanosecond and place the
zero-PC events outside the relevant handler paths; these scheduler-state
partitions do not identify a cause. Reviewed export safety permits three small
raw traces per arm and omits expanded parsed and wire intermediates. The
original 90% WAN blocker and the HAProxy, pressure, capacity and integration
gates remain open. Full-text sustained analysis was rejected before traffic
because its projected parsed output exceeds the 512 MiB physical-file budget.
See the [read-wait trace evidence](matched-verified-wan-read-wait-trace/README.md).

A following lossless stack-intern diagnostic keeps the 512 MiB physical-file
limit and adds a separate 4 GiB logical expanded budget. Its sustained H1
launch fails retained download at 15.781 MB/s. The standalone 16-row H2
complement fails download at sequences 9, 11 and 14 (27.543945, 27.497200 and
27.571749 MB/s). Both original four-trace analyzer sets reject missing
`TransitionStack` records; eight separate corrected offline reductions return
zero but remain attribution incomplete and do not change either failed
performance arm. The independent partition audit applies to H1; the H2
comparison is not an independent decoder or audit. H2 raw traces and canonical
streams are omitted by exact hash, and no H2 wire decode or raw safety scan was
performed. Source review establishes no cause. The tagged sender-duration
design is a proposed source-only checkpoint, with later work in progress
outside the package. Production source remains unchanged, and all WAN, HAProxy,
pressure, capacity, integration, release and merge gates remain open. See the
[compact read-wait evidence](matched-verified-wan-read-wait-stack-intern/README.md).

The tagged sender-duration observer first passes its bounded offline source
proof after five preserved attempts. Its authenticated matched integration then
completes 32 exact status-200 transfers with the requested H1/H2 protocol on
unchanged production source. H1
fails the 29.7 MB/s download floor at a 15.5586 MB/s minimum, and sequences
13/14 cross the 30-second idle deadline, leaving retained reuse inconclusive.
H2 retains valid reuse but fails at 27.4961 MB/s, with a 27.3702 MB/s steady
minimum. All payload, TLS, protocol and sender-closure checks pass, and cleanup
is natural and joined. The observer distinguishes DATA payload, Yamux-framed
plaintext submitted to `conn.Write`, and elapsed time in the call into the
TLS-wrapped transport; ciphertext bytes, delivery, TCP completion and NIC
completion are unmeasured. A mapped read-only stream-lifetime analysis finds
zero sender credit wait on the same-direction reused steady H2 stream at sequence
15 and positive waits on direction-cold sequences 9, 11 and 14. The available
growth history is agent receive-side evidence in the opposite direction; no
time-aligned server receiver growth or credit snapshot exists. These records
establish no cause, runtime fix or original WAN clearance. The HAProxy,
pressure, capacity, integration, release and merge gates remain open. See the
[sender observer source evidence](matched-verified-wan-sender-wait-observer/README.md)
and [matched sender integration evidence](matched-verified-wan-sender-wait-integration/README.md).

The follow-up below-TLS/server-growth diagnostic completes 32 exact status-200
transfers with the requested H1/H2 protocol and natural cleanup. H1 remains a
fixed-floor failure at 29.673138 MB/s retained; H2 fails at 27.473338 MB/s
retained and 27.517686 MB/s steady. All eight selected sender, encrypted
below-TLS write-buffer and server-growth intervals pass identity, containment,
conservation and drop checks. Direction-cold H2 rows associate the slower
transfers with credit-window growth and positive sender wait, while the
same-direction reused steady sequence 15 records no selected wait and passes. The
records do not distinguish cause from consequence because peer update and wake
timing remain absent. They establish no production defect, runtime fix, policy
change or WAN gate clearance. See the
[below-TLS/server-growth evidence](matched-verified-wan-below-tls-server-growth/README.md).

The byte-credit recycle follow-up completes another 32 exact status-200
transfers with the requested H1/H2 protocol and natural cleanup, but both arms
remain rejected and `inconclusive-upstream-reuse`. H1 fails the steady download
floor at 22.408983 MB/s; H2 fails at 27.455443 MB/s retained and 18.800107 MB/s
steady. All eight selected byte-credit, sender, raw-write and server-growth
joins pass exact conservation and closure. In the two conditioning-clean rows,
H2 byte-zero wait exceeds H1 by 714.781 ms and 873.756 ms, concentrated in
ranges containing normal-read credit. Individual receiver/peer local sections
are microsecond-scale, with receiver totals about 4–6 ms, so they do not explain
the excess. A same-endpoint cadence supplement shows no material
post-publication cadence expansion under its coarse wall-label comparison and
places the remaining difference before receiver eligibility; it cannot split
inbound DATA from consumer or HTTP/2 backpressure, and cross-process transit
remains unmeasured. The result establishes no cause, runtime fix, timeout or
window policy change, or WAN gate clearance. See the
[byte-credit recycle evidence](matched-verified-wan-byte-credit-recycle/README.md).

The earlier final-runtime checkpoint
`dc738e9000e60cd7ce3a0279127486f3bc05067a` has a low-latency tiny-response
matrix that passes all 12 throughput and tail gates with exact conservation. Its
16- and 32-agent idle standing-pool cohorts also pass exact topology, identity,
promise, cgroup, health, cleanup and OOM gates. The final-runtime WAN cohort
fails the download gate at 80.53% of the compatible legacy median while upload
passes at 101.70%. A fixed three-pair stability check then reproduces the
download failure in every fresh pair at 80.40%, 81.61% and 79.72%, while all
uploads pass. Both failed raw results are retained and are not superseded by
the earlier passing `e2df1b1` cohort. A profiled TCP diagnostic did not
reproduce the stall. A second replay with tagged in-flight credit snapshots
and TCP sampling, without native profiling, also stays healthy. Neither clears
the failed cohorts or identifies their cause. A bounded post-transfer growth
sample trace then captures an 18.55-second retained download and a slow first
timed download, although later transfers make that cohort median pass. It
shows no reservation denial and stable RTT. A subsequent counter trace captures
the failure at a 42.43% download ratio, but measures approximately 32 KiB DATA
frames and only 1.00414 times payload allocation charge. This refutes the
proposed eightfold allocation-amplification mechanism. Delayed receive-window
growth remains correlated with more byte-credit-zero observations, whose
attempt counts are not wait durations; no production fix follows yet.

Subsequent physical DATA-emission and historical-runtime comparisons also fail
their predeclared improvement gates. The refund-control emission comparison
fails its main gate twice while narrowly passing a separate all-phase floor
screen. The subsequent queue/acknowledgment comparison also fails its main gate
twice, despite exact transfers and a higher all-phase floor; a transition penalty
remains. A bounded deferred-global epoch comparison then fails both screens.
A physical-tunnel unsent-queue setting is subsequently verified at both
endpoints but remains effectively neutral and fails both timing screens.
These diagnostics do not clear the original repeated WAN failure or qualify a
production change.

Historical failure and optimization artifacts below remain useful for tracing
the decisions that produced `dc738e9`; they are not the current qualification
outcome. Fresh `248bad0` direct HTTPS reference and moderate-load latency cohorts
support Caddy-class performance for their tested tiny-GET workloads, while the
HAProxy throughput and tail gates fail. A complete standalone transport
diagnostic does not reproduce the original WAN stall and does not clear it.
Final-runtime grown-pool pressure, combined 1,536-request recovery, final
integration races and resolution of the original WAN failure remain outstanding.

The subsequent CI shutdown failure exposes an obsolete per-stream credit write
during accepted buffered drain. The terminal-state correction passes five new
deterministic regressions, the original TLS test repetitions, and full Yamux
normal/race-short suites. The original CI failure, baseline failures and the
first full-suite fixture conflict are retained in the
[terminal buffered-drain evidence](yamux-terminal-buffered-drain/README.md).
This correctness fix does not clear the WAN failure or qualify performance for
the changed runtime; frozen diagnostic binaries retain their original identities.

The initial 1,400 and 1,536 failure rows used a 10-second barrier release equal
to the upstream response-header timeout. Treat their origin high-water, owner
samples and admission-miss counters as capacity evidence; their final status,
error and useful-completion counts include the resulting timeout cascade.
Corrected reruns use a three-second maximum barrier hold and carry an explicit
`barrier_max_hold_seconds` field.

In the corrected 1,400 candidate row, 1,340 responses were 204 and 60 were 503;
origin high-water was 1,315 and the recorder counted 145 resource-pressure
admission misses. At 1,536, the baseline returned 688 204 and 848 503 responses
with origin high-water 573; the candidate returned 1,189 204 and 347 503 with
origin high-water 1,146. Both completed the following 512-request recovery row
without errors. Admission-miss counters can exceed final 503 counts because a
request may miss a transient acquisition and subsequently acquire on retry.

The completed H1 sweep found the highest sampled candidate rate at 16 clients:
29,614 useful requests/s, versus 46,917 for the stronger pooled
standard-library control and 70,055 for HAProxy. Candidate throughput was
5,864 at one client, 19,345 at four, 28,574 at 32, 28,140 at 64, 24,810 at 128
and 21,232 at 256. At the candidate optimum it reached 42.3% of HAProxy and
63.1% of the Go control, so limiting active requests may improve tail latency
but cannot close the throughput qualification gap. The 128-client profile
allocated 3.70 GiB across 298,703 warmup and timed requests; the earlier
pre-context-pool profile allocated 4.38 GiB across 305,506.
`newPublicProxyContext` no longer appeared as a material allocation site, but
the single profiled throughput sample did not establish a throughput gain.

Passing the pooled route resolution into forwarding by pointer removed the
former approximately 414 MiB allocation site. The `ca8a081` profile allocated
3.42 GiB across 318,884 warmup and timed requests, versus 3.788 GiB across
298,703 for `d1d952f`, about 15% less per request. Normalized throughput did not
improve: at 128 clients `ca8a081` and `d1d952f` were respectively 31.45% and
31.44% of their same-run HAProxy controls. All eight direct/agent sustained
candidate rows completed without client errors and persisted exactly one event
per useful response after draining. The clean 500 request/s direct row had
p95/p99 1.662/1.827 ms versus HAProxy 1.436/1.586 ms, ratios 1.157/1.152 that
pass the predeclared 1.25 tail gate.

Four pooled Go transports with 32 idle connections each did not improve over
one transport with 128 total idle connections. At 128 clients they completed
46,493 and 46,392 useful requests/s respectively, while sharding opened 700
new timed origin connections instead of 19, increased peak FDs from 264 to 310
and worsened both tails. One transport with only 32 idle connections reached
42,621 requests/s and opened 7,973 new origin connections. This rejects
sharding and isolates the single-transport idle budget as a separate production
experiment; it does not authorize a default change.

The production overlay confirmed a narrower benefit. At 128 clients, two
alternating H1 repeats improved by 5.3% in both orders, reduced p95 by 8--9%
and p99 by 5--10%, and cut approximately 7,500 timed origin reconnects to
0--21. H2 throughput improved only 0.9--1.3%, while p95 improved 7--8%, p99
12--13%, and 5,600--6,600 reconnects fell to zero. At 16 clients neither H1
nor H2 improved. With a TLS origin that negotiated upstream HTTP/2, neither
pool reconnected and 128 did not help. When the TLS origin advertised only
HTTP/1.1, 128 reached 25,263 versus 16,741 requests/s, reduced p95/p99 from
22.29/34.10 to 12.08/18.02 ms, and eliminated 7,413 reconnects. The candidate
still reached only 35.7% of HAProxy in that workload.

The 1 GiB-ballast H1/H2 capacity and recovery run completed 128/128 and then
512/512 requests for both configurations with zero errors, 503s or admission
misses and exact event conservation. After recovery, idle-128 retained 142
socket FDs versus 46 for idle-32, while both reported 4,096 cgroup socket bytes
and approximately 1.154 GiB RSS. This shows the FD retention tradeoff at this
scale; it is not evidence that extra idle sockets have no memory cost.

The existing real-TCP pressure-reclaim regression and focused direct-transport
lifecycle suite passed normally and under the race detector with the 128
overlay. The bounded per-origin idle default is therefore retained at 128.
The total idle budget, positive operator `MaxConns`, accounting and pressure
reclaim behavior are unchanged.

The external-agent fixture removes the earlier server/agent process and ledger
co-location. It starts the server and each production `agent.RunContext` in an
independent process, requires the expected negotiated mode and physical lane
shape before load, and records each process PID, container name, CPU set,
cgroup path/inode, RSS, descriptors, cgroup memory/socket values and OOM
deltas. Aggregate rows separately record initial pool, current grown pool,
growth reservation, session metadata, lane-zero legacy supplement and legacy
per-stream growth. The frozen legacy overlay uses the same application setup
and event recorder while leaving candidate-only private capacity fields absent.
Each process has its own sampling interval. Peak values from independent
processes are not summed or described as a simultaneous combined peak.

The decisive external topology is one 4 GiB server cgroup plus one or four
independent 4 GiB agent cgroups. Its 1 GiB ballast belongs only to the server.
It supplements rather than replaces the original combined server+agent 4 GiB
cgroup rows, including the failed 1 GiB-ballast 1,536-request gate. Separate
agent pressure/recovery and aggregate growth/idle/pressure/recovery traces are
still required before this fixture supports a capacity claim.
The current candidate agent role can apply and report a distinct bounded agent
ballast for that future pressure trace. The legacy smoke overlay and all rows
in this directory used zero agent ballast.

LOWAT reduced the 24-slow-reader socket peak from 66,355,200 bytes (63.28125
MiB) to 5,996,544 bytes (5.71875 MiB). The split controlled-pressure test then
peaked at 461,434,880 of 536,870,912 cgroup bytes, rejected six acquisitions,
and recovered healthy with zero socket bytes and no OOM. Candidate and parent
WAN download/upload measurements were effectively identical; see the raw rows
for each H1/H2 direct, agent and control value.

SHA-256 digests:

```text
1d5a88150b38dfa730621a9e53e09268410080667d6c1c3061dd4e0a75997357  capacity-and-profile.jsonl
272ef696c69706eb0e6868370dbc7fa6e66d14b4493bcd9d8088ad8d33ce62ff  warmed-throughput.jsonl
17b676d7ca564d9f62bb4a862dfee25a566bebe93dbe679f3c10d78eb3426e9d  stdlib-and-postsql-diagnostic.jsonl
c4465a2f75d07d90174e307f4623d18201ef6a102cb4a4f60cc6b71b8d91e156  stdlib-pooled-diagnostic.jsonl
c27f2eb6295a2145195947164876e546849587fe000ab45a58fd209b00132cb5  final-h1-profile.jsonl
e3e6ff8dd4c417716068c303a391efafbed17b6a5f124e8d5affb62318666df7  ballast-capacity-boundary.jsonl
c18c1de6751ae59597f20186035b15097d8a7fe96b5d3ffc6f2bd864f6789b0c  ballast-capacity-corrected.jsonl
a2c9f590486d21b662c3915e07cc87208cd92c366f46ca2ab4ba53f056e32517  h1-concurrency-sweep.jsonl
2d11a48da396e11b2d438fe097cc72f79a467b0bba4251c236c7ad8fb711cc3e  lowat-validation.jsonl
432ee9596ef0355fe8308149479728bb1a51555f4b3cc87cfe26895102e2b568  post-forwarding-qualification.jsonl
b4ab546fa295fc17f7eeacdccf0b47ace151cdd686a3ff6a24ed3e9f66e638bb  stdlib-transport-sharding.jsonl
404fe7b47c19b847213f99fc2756044cd7ab7a2651eca93ae3228a12543b42d8  production-idle-pool.jsonl
3c4bb8783903bf91b0d92a87e76ae13c7943339be1d69d5ce6c9db696b8a008c  external-agent-smoke.jsonl
```

## Source and binary provenance

| Label | Production source | Harness SHA-256 | Binary SHA-256 |
| --- | --- | --- | --- |
| `baseline-ee1aba7` | `ee1aba7b9091b65f42fc6529041507a8410141c2` | `dbde050725e44d1c9b85bd41b08c04dc259d0b381493ead038bc2b84950d1af6` | `235b331c7d96fa181cec74f138d37aa72fe6724569f65e694a29422002ac006c` |
| `candidate-095c923` | `095c9239edb8e007db3f64ba2e757b8900d00cd4` | same frozen comparison harness | `b5a57d3f037cb78d898a43d08861072c9b44b1a06452a985027c2942770096ff` |
| `candidate-d3767c9` | `d3767c930c52d49693a1bd8e8fcd2fb4f66c53b4` | `a94bea3b3f3cf5a8a1757a475d908ecfd051772561219e900206790d114e1a9b` | `266b9d2f01fe8c1f521e57feee4cf998b6085235fea52c513dccf9b20580354b` |
| `candidate-b4dd36b` | `b4dd36bffd57dc959c936e2eac1cced55a8cae7a` | `96b85f9adb748835e9922ef4b539311af9ba2e71afae1ffca3ce626d567343c3` | `ddd5dea5b8f9cf219ca0ffaf13370dc786f4a40ca2f4b247e16146226b7a0e57` |
| `candidate-b4dd36b` ballast capacity | `b4dd36bffd57dc959c936e2eac1cced55a8cae7a` | `cdbc6007a7dc5ea0979219af5dbd9dd153f9c259fdb4608d62e302d1547d0dd4` | `71a3268b7cbd048a3055064b32a6f5b47a58434b597a4144f2c0ee1990f2a86a` |
| `candidate-d1d952f` H1 sweep | `d1d952f0bbe18481a65c3b45e9e16fc6f9738382` | `27d5185a1e2d1923eb8183ef87f694b45b862989746f349aa44c5591ff3d25c9` | `2965a9af3f48907eeaafa364e13bcd019d634914be7f99969150b98ac462659b` |
| `candidate-ca8a081` forwarding | `ca8a0818312fbefbd8af9699cd9469f29ed82b0b` | `27d5185a1e2d1923eb8183ef87f694b45b862989746f349aa44c5591ff3d25c9` | `b9fb8cd02c43a4d1179be50f28b5716edb85d8329113c1ec73d02c28c7e2322a` |
| sharding diagnostic driver | `ca8a0818312fbefbd8af9699cd9469f29ed82b0b` plus test-only harness | `1e3652a5716727fef32ae53ac166d2a0200b8b5a26f641ebc867298953821b3c` | `2fafa0220f0beaa0a3f09ec8c0f9c2b4b587e89464a006bc0cb868bf622c3ac7` |
| idle-32 alternating/TLS proxy | `ca8a0818312fbefbd8af9699cd9469f29ed82b0b` | `89c4ce099ab3237c298653206752a7457f684aaa9e6b8d33fb3036f2f3efefdb` | `45fd2509c41c1160b7ec2d6d4d775bfd1fd1616f5689f5f62c289620cbe06b0b` |
| idle-128 alternating/TLS proxy | `ca8a0818312fbefbd8af9699cd9469f29ed82b0b` plus `MaxIdleConnsPerHost=128` overlay `bbddfe2c21628622e74ff478d2767d6e05ab7f5698b084810b23dbda7e3807c4` | `89c4ce099ab3237c298653206752a7457f684aaa9e6b8d33fb3036f2f3efefdb` | `1b8f72e618bbde0c9f2ad776e5497dcb151bc2dc73b3e014e65bd1bd16bfbc63` |
| origin-protocol diagnostic driver | `ca8a0818312fbefbd8af9699cd9469f29ed82b0b` plus test-only harness | `9a3ac62225dd404d4a93cb9560678041cfb84995e0c38f7bd183547ff3ee68f0` | `805d6f2f028fa0fa72a478840b485a0eca646e6bf3223ebd37907b8d709b4093` |
| external legacy server | `3ec137c3ad3d07028e867042fb00d3933912d7ab` plus frozen overlay `32425482c29081d7c41410ecad206d8938fecb0f4e9308e6670be1a413d3952c` | existing `3ec137c` server harness | `669918fe63e3f305cdb0c4f3dab843df61791a75ccfa27668edee4cfbd7867fc` |
| external legacy agent | `3ec137c3ad3d07028e867042fb00d3933912d7ab` plus frozen overlay `43dbba91e6485b1fd5376c2331614adf8728f32d2aff0aea2ba08362dd0698bb` | frozen external agent role | `16d9ca0744ca2696d6177bb7066572b3707c219181377eebcf22bae754b37599` |
| external aggregate server | `1418407376fdaeefb6808826f379ce68ace9e89e` | `fe5139188b59e18faaef7127e45dd653edfac635eb1d950251812e3715a3c339` | `dd7a902932634966d8c19a3c2da57dcda4211f43ad2e20135b4afa050a68958f` |
| external aggregate agent | `1418407376fdaeefb6808826f379ce68ace9e89e` | `d8424a0873000b918887a398e65e8365312b0a7fb34e20e0ebba1c1f108cb272` | `a053ec3ffa9f96192f6ced4fc5777c2a56bfcfd076a2adbeaccb91bc95d58e77` |
| final external aggregate server | `81f5bc6c41507e952b9b4202c86643fb51cb378b` plus frozen overlay | `1e1a2cf1b40f9454774c2930a341dfb97c93f5e30bfcd300d32f8766174f9b30` | `1e49b0821ca175cf4ae1493146c6cfeb76ef5ca2b8d16b37fb2aa4ec8fb8eed2` |
| final external aggregate agent | `81f5bc6c41507e952b9b4202c86643fb51cb378b` plus frozen overlay | `da88194ef9968058721e3e5ea56d3f73c0588693b59130fb60fa2ea8974efbb6` | `b4d08ccb0deb0d72e38e6cbba2440185a1a4cc39d173f181a182583aef9e15dd` |

The qualification script SHA-256 was
`5e93c56e781b912b52867f0338781ec3733fd5287a11d003ee57a3af059df25b`
for the sharding diagnostic,
`a31074f8c19864eb8f83a679cfa9e64122710ea04246ef9a64bf9f58facd6493`
for the alternating idle-pool runs, and
`13297f8b9fb1fa166819bbee722df431c3b68928ec5b691db146460ff1d69c6e`
for the origin-protocol runs. The external-agent smoke used script SHA-256
`abeac5a6ddbbe049f65ba3fe02c67f96b8437b3567244f45cd3580b72d7c05fc`.
The final accepted external smoke used frozen script SHA-256
`d8708bb496c8aee9c90e9c217e6095e74be164893102487af7df9613d06cd2bc`.

The baseline and candidates were built from clean archives. Only the
qualification test harness was overlaid, so unrelated shared-worktree edits
could not enter the server binary:

```sh
mkdir -p tmp/proxy-qualification/bin tmp/proxy-qualification/builds
build_dir=$(mktemp -d -p tmp/proxy-qualification/builds baseline-ee1aba7.XXXXXX)
git archive ee1aba7 | tar -x -C "$build_dir"
install -m 0644 internal/server/proxy_qualification_test.go \
  "$build_dir/internal/server/proxy_qualification_test.go"
output=$(realpath tmp/proxy-qualification/bin/server-ee1aba7.test)
(cd "$build_dir" && go test -c -o "$output" ./internal/server)
sha256sum "$output" "$build_dir/internal/server/proxy_qualification_test.go"
```

The external smoke used frozen server and agent overlays for both sides. These
commands reconstruct the exact source inputs; Go binary hashes can still carry
absolute build-directory metadata:

```sh
legacy_dir=$(mktemp -d -p tmp/proxy-qualification/builds external-legacy.XXXXXX)
git archive 3ec137c3ad3d07028e867042fb00d3933912d7ab | tar -x -C "$legacy_dir"
install -m 0644 internal/server/testdata/proxy_qualification_external_legacy_test.go \
  "$legacy_dir/internal/server/proxy_qualification_external_legacy_test.go"
install -m 0644 internal/agent/testdata/proxy_qualification_external_legacy_test.go \
  "$legacy_dir/internal/agent/proxy_qualification_external_legacy_test.go"
(cd "$legacy_dir" && go test -c -o /tmp/external-legacy-server.test ./internal/server)
(cd "$legacy_dir" && go test -c -o /tmp/external-legacy-agent.test ./internal/agent)

aggregate_dir=$(mktemp -d -p tmp/proxy-qualification/builds external-aggregate.XXXXXX)
git archive 1418407376fdaeefb6808826f379ce68ace9e89e | tar -x -C "$aggregate_dir"
install -m 0644 internal/server/testdata/proxy_qualification_external_1418407_test.go.txt \
  "$aggregate_dir/internal/server/proxy_qualification_test.go"
install -m 0644 internal/agent/testdata/proxy_qualification_external_1418407_test.go.txt \
  "$aggregate_dir/internal/agent/proxy_qualification_external_test.go"
(cd "$aggregate_dir" && go test -c -o /tmp/external-aggregate-server.test ./internal/server)
(cd "$aggregate_dir" && go test -c -o /tmp/external-aggregate-agent.test ./internal/agent)

sha256sum \
  internal/server/testdata/proxy_qualification_external_1418407_test.go.txt \
  internal/agent/testdata/proxy_qualification_external_1418407_test.go.txt \
  internal/server/testdata/proxy_qualification_external_legacy_test.go \
  internal/agent/testdata/proxy_qualification_external_legacy_test.go
```

The final accepted aggregate pair used production source `81f5bc6` and the
frozen `*_external_81f5bc6_test.go.txt` overlays. Its exact orchestrator is
`scripts/testdata/qualify-proxy-performance-external-81f5bc6.sh.txt`:

```sh
final_dir=$(mktemp -d -p tmp/proxy-qualification/builds external-final.XXXXXX)
git archive 81f5bc6c41507e952b9b4202c86643fb51cb378b | tar -x -C "$final_dir"
install -m 0644 internal/server/testdata/proxy_qualification_external_81f5bc6_test.go.txt \
  "$final_dir/internal/server/proxy_qualification_test.go"
install -m 0644 internal/agent/testdata/proxy_qualification_external_81f5bc6_test.go.txt \
  "$final_dir/internal/agent/proxy_qualification_external_test.go"
(cd "$final_dir" && go test -c -o /tmp/external-final-server.test ./internal/server)
(cd "$final_dir" && go test -c -o /tmp/external-final-agent.test ./internal/agent)
```

The host-process correctness smoke was then run with one external agent and
the `steady-100` scenario. Capacity runs omit `--smoke`, create distinct 4 GiB
cgroups, and use one or four agents:

```sh
scripts/qualify-proxy-performance.sh --smoke --external-agents 1 \
  --scenario-filter steady-100 \
  --baseline-binary /tmp/external-legacy-server.test \
  --baseline-agent-binary /tmp/external-legacy-agent.test \
  --candidate-binary /tmp/external-aggregate-server.test \
  --candidate-agent-binary /tmp/external-aggregate-agent.test \
  --driver-binary /tmp/external-aggregate-server.test
```

Replace `ee1aba7` and the output label to replay a candidate. The current
orchestrator accepts frozen server and driver binaries explicitly:

```sh
scripts/qualify-proxy-performance.sh \
  --baseline-binary tmp/proxy-qualification/bin/server-ee1aba7.test \
  --candidate-binary tmp/proxy-qualification/bin/server-candidate.test \
  --driver-binary tmp/proxy-qualification/bin/server-driver.test \
  --candidate-label candidate-095c923
```

The constrained H1 boundary command substitutes the desired concurrency and
uses the clean-archive driver containing owner instrumentation:

```sh
scripts/qualify-proxy-performance.sh \
  --capacity-only --capacity-concurrency 1280 --capacity-protocol h1 \
  --path-filter agent --ballast-mib 1024 \
  --candidate-binary tmp/proxy-qualification/bin/server-harness-checkpoint.test \
  --driver-binary tmp/proxy-qualification/bin/server-harness-checkpoint.test \
  --candidate-label candidate-b4dd36b
```

The current harness reads newer owner arrays by reflection and was verified to
compile when overlaid on a clean `ee1aba7` archive. The exact uncommitted
historical harness used for the first baseline binary is preserved as
`internal/server/testdata/proxy_qualification_ee1aba7_test.go.txt`, SHA-256
`dbde050725e44d1c9b85bd41b08c04dc259d0b381493ead038bc2b84950d1af6`.
Copy that file instead when auditing the original executable's source. Go test
binary hashes can differ when rebuilt from another absolute archive directory
because debug/build metadata includes build context; the source and production
commit hashes are the reproducibility anchors.

Timed runs flush the observability recorder before warmup, again between
warmup and timed counter reset, and after the load. The final flush wall and
proxy CPU time are reported separately and excluded from timed useful
requests/s and proxy CPU. Older JSONL rows predate these counters; their process
logs recorded event drops and the operations report calls that limitation out.

## Environment and HAProxy control

```text
Linux 7.0.0-38-generic x86_64
Go go1.26.6 linux/amd64
Docker Engine 28.3.3
HAProxy 3.2.9-1ubuntu2.2
```

Each run generated an ephemeral P-256 certificate shared by p2pstream and
HAProxy. The sanitized HAProxy configuration was:

```text
global
  maxconn 4096
  nbthread 4
defaults
  mode http
  timeout connect 5s
  timeout client 30s
  timeout server 30s
frontend public
  bind 127.0.0.1:<ephemeral> ssl crt <ephemeral-bundle> alpn h2,http/1.1
  default_backend origin
backend origin
  server origin 127.0.0.1:<ephemeral>
```

HAProxy is a same-TLS, same-hop direct forwarding control. It does not execute
p2pstream's access, WAF, cache, Sites, observability or resource-accounting
pipeline and has no agent-tunnel equivalent. Agent rows contain the server and
real in-process agent in one process, so their RSS and CPU are combined.
HAProxy's host-cgroup socket counters are not process-isolated and must not be
compared with p2pstream's Docker-cgroup `memory.stat` values.

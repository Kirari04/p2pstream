# WAN TCP and native-profile diagnostic

This finite diagnostic reuses the exact frozen `dc738e9000e60cd7ce3a0279127486f3bc05067a`
server and aggregate-agent binaries and the frozen `3ec137c3ad3d07028e867042fb00d3933912d7ab`
legacy agent. It investigates the preceding [failed WAN cohort](../final-runtime-wan-failure/README.md).
It does not qualify performance or override that failure.

The full 32-transfer order, 256 MiB payloads, CPU sets, 80 ms configured RTT,
1 Gbit/s shaping, protocol windows and 90% throughput gate remain unchanged.
Endpoint wrappers enable native CPU and block profiles over complete endpoint
lifetimes. An external collector on CPUs 8–15 samples both sides' TCP sockets
and router qdiscs. Phase timestamps bracket load invocations; they include
invocation/cleanup overhead and are not the load driver's exact timed interval.
One diagnostic argument differs: the pool-readiness assertion is 80 MiB rather
than the failed cohort's 8 MiB. This strengthens a post-warmup assertion without
changing advertised receive windows, payloads or transfer order. Profiling and
sampling can change scheduling, so these timings are diagnostic only.

All 32 transfers return verified payloads and exactly one persisted event each,
with zero request errors, drops, queue residue or drain errors. The diagnostic
median download/upload ratios are 1.0050/0.9928. The slow 22.6-second retained
and 9.8-second timed downloads do **not** reproduce. Consequently, this replay
cannot identify the cause of the original failure or justify a production fix.

The collector records 5,691 entries with zero capture errors. Each network side has
2,603 TCP capture commands across the two endpoint cohorts. Server sampling intervals have a 100.14 ms median, 100.21 ms
p95 and 101.36 ms maximum; the agent maximum is 102.70 ms. Both netem qdiscs
report zero drops, overlimits and requeues over the captured interval.
Connection logs map all nine physical sockets to exact negotiated lane IDs.
The selected active sender is identified by the largest transmitted-byte delta
within each phase, then mapped by its four-tuple to those connection logs.

Aggregate download phases in this healthy replay start at a congestion window
of 10 segments and typically reach roughly 5,900 segments. Timed sender queues
have medians near 3.0–3.1 MiB, with about 6.8 seconds of cumulative send-buffer
limitation per approximately eight-second phase and no retransmitted bytes.
These observations describe this replay; they do not explain TCP state in the
uncaptured failed run. No instantaneous Yamux sender-credit snapshot exists in
these frozen binaries, so the TCP series cannot directly distinguish all byte,
stream-allocation and session-allocation waits.

Whole-agent block profiles attribute 6.58 seconds of legacy and 5.05 seconds of
aggregate time to the stream's credit-wait select, and 55.27/56.52 seconds to
waiting for send completion. These sums cover startup, warmups, both directions
and cleanup; blocked goroutine time is not elapsed transfer time. The profiles
show no extra sustained aggregate credit wait in this healthy replay, without
ruling out transient starvation in the original failure.

`tcp-active-sender-with-lanes.csv` retains all selected sender samples, while
`tcp-phases-with-lanes.json`, `physical-lanes.json`, phase markers and the
telemetry summary retain the reductions and qdisc endpoints. `reports/`
contains all sixteen native CPU/block text reports. Native blobs and the full
20.4 MB raw TCP/qdisc stream remain at the hashed local paths in the manifest;
selected time series and text reports cannot reconstruct those missing blobs.

For replay, build clean archives as in the linked WAN qualification, copy the
saved original runner, wrappers, collector and phase marker into a fresh
scratch directory with `scripts/`, `profiles/` and `telemetry/`, and substitute
its paths and the verified binary paths throughout. Execute the exact recorded
command once. The runner diff contains only collector lifecycle and phase
markers. After all children join, run the saved telemetry/profile analysis and
lane annotation scripts. Exact native `go tool pprof` commands are preserved
in `profile-commands.json`. Every tracked artifact is covered by `SHA256SUMS`.

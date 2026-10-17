# Sparse ordinary-binary WAN TCP diagnostic

This diagnostic reuses the exact ordinary `dc738e9` server and aggregate-agent
binaries and the frozen `3ec137c` legacy agent. It keeps the original 32-transfer
workload, 256 MiB payloads, 8 MiB pool assertion, CPU sets and 80 ms / 1 Gbit/s
path. It adds two TCP captures per second, one at each endpoint, plus qdisc
snapshots before and after the run. Bash `EPOCHREALTIME` brackets transfers
without a subprocess. There are no added credit polls, trace hooks or profiles;
the original load fixture's standard runtime/resource HTTP sampling remains.

All 32 payloads and persisted events are exact, with zero request errors,
drops, queue residue or drain errors. The run exits zero in 263.392 seconds.
The diagnostic download/upload median ratios are 0.998394 / 1.000465.
These instrumented timings do not supersede the three failed ordinary
[WAN qualification pairs](../final-runtime-wan-stability/README.md).

Two individual aggregate downloads start more slowly. The retained warmup
takes 9.813 seconds and timed transfer 2 takes 9.770 seconds, including load
invocation and cleanup. Their sampled sender congestion windows reach about
3,100 segments with finite slow-start thresholds of 249 and 242 segments.
Healthy timed transfers 1 and 3 take 8.051 and 7.833 seconds and reach about
5,900 segments. The `ss` output omits `ssthresh` on those healthy sockets;
the analyzer's default zero means absent, not a measured threshold of zero.

Both slower phases have queued sender data: median send queues are 3.367 and
3.320 MB, with maximum unsent data of 2.922 and 2.583 MB. Retransmitted-byte
counters do not increase from each download's prephase sample through its
last sample. At one-second resolution this is not an exact whole-phase loss
measurement. Finite early thresholds without observed retransmissions support
a TCP startup/HyStart hypothesis, but do not prove that mechanism or identify
the source change responsible for it. A separate causal comparison must test
the physical DATA emission change while holding flow control constant.

The collector obtains 259 TCP samples per side, with median intervals of
1,000.146 / 1,000.080 ms and maxima of 1,001.228 / 1,007.090 ms. There are no
capture errors, qdisc drops, overlimits or requeues. Collector CPU is 0.391
seconds plus 8.400 seconds in its command children. All 32 phases map to nine
exact physical connections and logged lane IDs. Sample timestamps are recorded
after `ss` returns; the reductions retain prephase baseline age and the gap
between the final sample and phase end.

The full raw TCP/qdisc stream, all result rows and markers, original runner,
diagnostic diff, collector, capture manifest and derived outputs are committed.
Raw fixture ready files and server logs remain local because they can contain
ephemeral credentials. Their hashes and paths are in `evidence-manifest.json`.
`connection-inputs.json` retains only the connection address, lane, time, log
filename, process ID and endpoint URLs used by the original post-run annotator.

The original analysis sources are preserved as `*.original.py.txt`. The replay
versions change only metadata loading to read `connection-inputs.json`. After
copying this directory to a scratch directory, regenerate the numeric results
without endpoint processes:

```sh
python3 analyze-telemetry.py.txt
python3 annotate-lanes.py.txt
```

The saved benchmark command records the original scratch paths. Replaying the
benchmark itself requires substituting fresh paths and verified binaries in
the command and diagnostic runner, with exclusive benchmark CPU ownership.
Every committed artifact is covered by `SHA256SUMS`.

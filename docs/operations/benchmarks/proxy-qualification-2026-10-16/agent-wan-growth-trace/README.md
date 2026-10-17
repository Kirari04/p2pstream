# Deferred growth trace captures a slow download ramp

This diagnostic captures slow individual downloads without live credit/TCP
polling or native profiles. It does not establish their cause or clear the
[fixed three-pair failure](../final-runtime-wan-stability/README.md). All 32
transfers complete correctly; later downloads are fast enough that this
instrumented cohort's median timing gate passes. The three uninstrumented
failed pairs remain decisive for production qualification.

Production behavior remains based on
`dc738e9000e60cd7ce3a0279127486f3bc05067a`. Tagged diagnostic source
`6fd2e8a148229b24037c65204c3bc2bab6bd8822` adds a process-fixed 4,096-event
ring at existing completed receive-growth sample epochs. It reuses the
controller's sample-start timestamp, records scalar state/outcomes under a
private mutex, and retains bounded session references until drain. There is
no per-read extra clock, formatting, event allocation, timer or goroutine.
Normal builds use a compile-time-false hook; ordinary-build disassembly
contains no qualification calls. Diagnostic overhead still exists and can
change timing.

## Workload and provenance

Both current test binaries were built from a clean Git archive of `6fd2e8a`.
The saved clean-build manifest records commands, Go 1.26.6, archive and binary
hashes. Its embedded self-hash covers the text before that line was appended;
the authoritative external final-file digest is recorded in `manifest.json`.
The compatible legacy agent stays frozen at `3ec137c` with its preserved
qualification overlay and receives no growth-drain HTTP requests.

The original full 32-transfer workload and order are unchanged: configured
80 ms / 1 Gbit/s, four fresh 256 MiB warmups per direction, retained warmups,
then three timed transfers per direction, legacy followed by aggregate. The
8 MiB receiving-pool assertion is unchanged. The runner diff only adds server
and agent drains after the existing transfer checks and result append, plus
Bash timestamp bookends for their full added delay. There are no pre-transfer
markers, live collectors, TCP sampling or native profiles.

The two drain subprocesses add a median 72.536 ms between transfers (minimum
68.341, p95 80.334, maximum 81.592 ms; sum 2.369 seconds). No added HTTP drain
runs during a timed transfer. The remaining bounded mutex/reference-retention
observer means this is still diagnostic evidence.

## Captured slow and healthy cases

All 32 attempted transfers produce exact verified HTTP 200 payloads and 32
persisted events, with zero client errors, drops or queue residue. The finite
runner exits zero after 283.782 seconds. Its median aggregate/legacy ratio is
100.19% download and 98.48% upload. These cohort medians conceal the captured
slow retained warmup and first timed download and do not override the earlier
uninstrumented three-pair failure.

| Download phase | Legacy transfer time | Aggregate transfer time | Aggregate throughput |
| --- | ---: | ---: | ---: |
| retained warmup | 7.901 s | 18.548 s | 14.47 MB/s |
| timed 1 | 7.930 s | 9.745 s | 27.55 MB/s |
| timed 2 | 8.126 s | 7.915 s | 33.92 MB/s |
| timed 3 | 7.679 s | 7.771 s | 34.54 MB/s |

The ring captures 2,436 unique growth epochs: 199 successful proposals and
2,237 no-proposal samples. There are no reservation denials, publication
errors, overwritten events, unmapped sessions, duplicate epochs or sample-time
inversions. All 48 real drains and 16 explicit legacy-agent skips are retained.
RTT on the slow retained stream stays at 80.183 ms.

The following successful-proposal timestamps are offsets from that stream's
first retained growth-ring sample, **not** transfer start or publication
completion. The ring records outcome after publication while retaining the
controller's earlier sample-start time.

| Download stream | Lane | 4 MiB proposal | 8 MiB proposal | 16 MiB proposal |
| --- | ---: | ---: | ---: | ---: |
| legacy retained warmup | 0 | 0.892 s | 1.241 s | 1.509 s |
| aggregate retained warmup | 1 | 7.633 s | 11.171 s | 15.918 s |
| aggregate timed 1 | 3 | 1.527 s | 2.734 s | 3.944 s |
| aggregate timed 2 | 2 | 0.803 s | 0.980 s | 1.323 s |
| aggregate timed 3, reused direction | 3 | already above | already above | already above |

The slow retained stream already reaches about 3.938 MiB by 1.85 seconds and
then plateaus just below the table's 4 MiB threshold. Its payload rate is only
about 6–8 MB/s despite that byte window and roughly 80 ms RTT. Aggregate's
256 MiB maximum and legacy's 1 GiB maximum are both far above the observed
plateau. The trace rules out reservation denial and unknown/inflated RTT in
this case. It does not yet distinguish TCP pacing, allocation-credit pressure,
publication scheduling or their feedback into measured demand.

A subsequent [allocation-counter diagnostic](../agent-wan-allocation-counters/README.md)
captures the slowdown while measuring DATA reservations. Download frames carry
about 32,005 bytes on average and allocation charge is only 1.00414 times
payload, refuting the hypothesis that 4 KiB rounding creates roughly eightfold
allocation amplification in the captured slow case. Delayed demand-based
growth and byte-credit-zero observations remain correlated, but those attempt
counts are not durations and do not establish a production fix.

## Complete artifacts and replay

`growth/` includes all 64 original drain responses/skips and all 32 added-delay
records. `analysis/growth-events.csv` preserves all 2,436 events;
`growth-per-stream.json` groups every transfer/role/lane/session/stream; and
`growth-summary.json` validates payload/event conservation, complete drains,
identity mapping, ordering, overflow and observer delay. Event insertion order
is authoritative; pre-publication sample times can invert across concurrent
streams, so the analyzer reports inversions descriptively. The
`denial_active_after_outcome` field compares the sample timestamp with the
resulting denial deadline, including a denial caused by that attempt.

The original and modified runner, their exact diff, drain/analyzer sources,
launch command, build metadata and result rows are included. Binaries and
source archive remain local with hashes in `diagnostic-summary.json`; all raw
growth responses are committed. The source archive is reproducible with
`git archive 6fd2e8a148229b24037c65204c3bc2bab6bd8822`.

For replay, rebuild the current fixtures with
`go test -tags=p2pstream_qualification -c` for `./internal/server` and
`./internal/agent` at the recorded ref, and rebuild the compatible legacy
fixture using its preserved overlay. Restore saved `.txt` script suffixes,
replace the embedded original root
`/tmp/p2pstream-wan-growth-diagnostic.k9m08_ma` consistently with a fresh root,
create its `scripts` and `growth` directories, and use the saved launch
arguments with those exact binaries. Write `exit.json` only after the finite
runner and its owned cleanup exit, then invoke `analyze-growth.py <root>`.
Do not add polling or profiling to this replay.

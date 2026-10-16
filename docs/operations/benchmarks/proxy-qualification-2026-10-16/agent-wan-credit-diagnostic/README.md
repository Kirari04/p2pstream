# WAN credit and TCP diagnostic: healthy nonreproduction

This is diagnostic evidence, not an acceptance run. The fixed uninstrumented
[three-pair check](../final-runtime-wan-stability/README.md) fails download
independently at 80.40%, 81.61% and 79.72% of legacy. This separate replay did
not reproduce that slowdown and does not override those failures or establish
a root cause. No production behavior was changed from runtime
`dc738e9000e60cd7ce3a0279127486f3bc05067a`.

Test-tag source `c879e85284e3225fbf693f3ce98139973a124eac` adds bounded,
observational Yamux credit snapshots and an early fixture-only HTTP branch.
The branch returns before runtime memory sampling, admission refresh, event
queries or owner inspection. It copies at most eight stream pointers per
session before separately reading credit locks/atomics; a closed stream can
appear, and the result is not an atomic conservation view. The single-agent
collector rejects more than five physical sessions or a response over 256 KiB.

Both current binaries were rebuilt sequentially from a clean immutable Git
archive, without overlays or worktree copies. `build-manifest.json` records
archive hash, Go environment, exact commands and binary hashes. The frozen
compatible legacy agent remains from `3ec137c` and has no snapshot API: the
collector sends it no diagnostic HTTP requests. The current server can still
report its legacy-side byte credit.

## Workload and result

The original runner, transfer order and 8 MiB receiving-pool assertion are
preserved: configured 80 ms / 1 Gbit/s, four 256 MiB warmups per direction,
one retained-transport warmup per direction, then three timed transfers per
direction, legacy followed by aggregate. All 32 payloads and persisted events
are exact HTTP 200 responses with zero client errors. The finite runner exits
zero after 267.585 seconds. Its timing ratios are 99.69% download and 98.60%
upload; instrumentation makes these ratios nonqualifying.

No native CPU or block profiling ran. Three owned collectors sampled the
cheap server and aggregate-agent API every 20 ms, TCP state every 100 ms and
qdiscs every second. Collectors used CPUs 8–15. Server and agent credit calls
had median durations 0.429/0.442 ms and maximum durations 2.483/2.417 ms.
There are 13,042 server and 6,603 aggregate-agent captures with zero truncated
sessions. Median capture intervals are 20.111 ms. The reported interval range
includes endpoint turnover; it is not a within-transfer jitter bound.

Eight server and seven agent API errors occur during endpoint shutdown; none
falls inside a marked transfer phase. TCP collection has zero capture errors.
Both qdiscs report zero drops, overlimits and requeues. TCP sampling medians
are 100.12/100.12 ms on the server/agent sides. All collectors report requested
stop, and all fixture readiness PIDs are gone after exit.

Observer cost is retained rather than assumed negligible. Server/agent Python
credit collectors use 7.162/4.207 CPU seconds. The TCP collector uses 3.496
CPU seconds itself plus 72.595 CPU seconds in its child commands across the
whole 267.585-second run. Endpoint work serving snapshots is additional.

## Healthy first-direction growth

The active physical lane is selected using the largest TCP byte delta in each
phase, then mapped through server physical tunnel tuples plus lane/session
identity. Agent-side Yamux addresses are opaque `yamux:local` / `yamux:remote`
values and are not treated as physical tuple evidence.
New download directions start with 512 KiB stream receive limits. The following
crossings are observed in the current server's byte receive limit, measured
from each phase marker:

| Download phase | Lane | 1 MiB | 4 MiB | 8 MiB | 16 MiB |
| --- | ---: | ---: | ---: | ---: | ---: |
| retained warmup | 1 | 0.747 s | 0.988 s | 1.149 s | 1.591 s |
| timed 1 | 3 | 0.663 s | 0.985 s | 1.166 s | 1.408 s |
| timed 2 | 2 | 0.678 s | 0.919 s | 1.081 s | 1.342 s |
| timed 3, reused direction | 3 | already above | already above | already above | already above |

Sender allocation limits follow those growth crossings about 40 ms later.
New-direction sender byte-zero observations end around 0.94–1.01 seconds;
aggregate session allocation credit never reaches zero in the active interior
samples. Later sender byte and allocation credit remain plentiful. This
healthy trace gives no evidence of a sustained publication or credit stall.

Each healthy download starts with TCP congestion window 10 and reaches a
median around 5,900 segments. Median send queues are about 3.17 MiB, with
6.73–6.88 seconds of cumulative send-buffer limiting per roughly eight-second
phase. Retransmitted bytes are zero except 4,344 bytes on timed lane 2.
These are observations of the healthy replay, not an explanation of the failed
runs. Polling, extra endpoint work or build differences could affect timing;
no causal attribution is made.

## Artifacts and replay

`results.jsonl`, `credit-phases.json`, `credit-active-lanes.csv`,
`tcp-phases-with-lanes.json`, `tcp-active-sender-with-lanes.csv` and
`credit-coverage.json` preserve every transfer and selected-lane observation.
The CSV records min/max across captured streams and retains their IDs; it
does not reconstruct each full snapshot. Full raw credit/TCP JSONL, immutable
source archive and binaries remain local with sizes and SHA-256 hashes in
`diagnostic-summary.json`. Those omitted raw snapshots cannot be recovered
from the selected CSV alone. The source archive can be reproduced with
`git archive c879e85284e3225fbf693f3ce98139973a124eac`.

The saved runner differs from the saved original only by collector lifecycle
and phase markers; `runner-diagnostic.patch.txt` shows the exact diff. Python
sources, exact launch command and clean build metadata are included. To
replay, rebuild the current fixture binaries with
`go test -tags=p2pstream_qualification -c` for `./internal/server` and
`./internal/agent` at the recorded ref, and rebuild the frozen compatible
legacy fixture using its preserved overlay. Restore the `.txt` scripts to
their original names, replace the embedded original diagnostic root
`/tmp/p2pstream-wan-credit-diagnostic.x4j_xovk` consistently with a fresh output
root, create its `scripts` and `telemetry` directories, and run the arguments
in `run-command.txt` with those exact binaries. Do not reuse profile wrappers.
Run the three saved analysis scripts in order only after `exit.json` exists.

The next diagnostic records bounded existing growth-sample events and drains
only after each transfer, without live credit/TCP polling. Until the repeated
uninstrumented gate passes on a proven fix, final pressure, capacity, matched
reference and final race qualification remain pending.

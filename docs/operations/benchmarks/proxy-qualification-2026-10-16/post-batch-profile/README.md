# Current direct-path profile after bounded SQL batches and idle-128

These finite, separate CPU/allocation and contention passes use the same clean
`3ec137c3ad3d07028e867042fb00d3933912d7ab` server/driver executable as the
retained Caddy reference. There are no source or harness overlays. Proxy CPUs
are 0–3 (four logical CPUs on two physical cores), load/origin CPUs 4–15,
`GOMAXPROCS=4`, one 4 GiB Docker cgroup without swap, native TLS, H1 and 128
closed-loop clients. Each pass warms for two seconds and measures ten seconds.
Astra and both workers acknowledged source-only before the root-owned runs.

The CPU pass returned 271,497/271,497 correct timed responses and persisted
exactly 271,497 events; the contention pass returned/persisted 261,306/261,306.
Neither dropped events or reported a client, drain or event-query error. The
profiled rates are diagnostics and do not replace the unprofiled three-pass
Caddy/HAProxy comparison. CPU profiles include warmup; allocation profiles
include setup and fixture initialization as well. Post-load event drain is
outside the timed useful-throughput/CPU counter window.

The CPU profile contains 46.29 CPU-seconds across 12.19 wall-seconds. Its
largest flat sites are native syscalls 26.61% and cgo 10.74%; SQLite statement
execution accounts for about 10% cumulatively. Allocation/GC and native HTTP
header/request work remain material. The allocation profile reports 3.28 GB
for setup plus 326,694 warmup/timed requests: database/sql argument conversion
9.91%, MIME header reads 9.50%, Header.Clone 8.62%, Request.WithContext 5.42%.
These are profile shares, not isolated speedup predictions.

The separately sampled mutex profile attributes 37.72% of delay to the Go
transport idle-connection queue and 20.16% to returning idle connections.
The application direct-transport cache contributes 12.04%, adaptive-capacity
refresh 12.49% (including the overlapping adaptive sampler call stack), and
route selection 3.28%. Cumulative stack percentages overlap and must not be
summed. The block profile is dominated by normal transport/select waits;
those delays are not CPU consumption.

The direct cache serializes every hit to update last-use time. This supports
a bounded read-lock/atomic timestamp experiment with the same physical
transport and connection ownership. The completed
[read-hit experiment](../direct-cache-readhit/README.md) reduced isolated
lookup time but did not improve sustained throughput, so production was
restored. This does not support reintroducing the previously rejected
transport sharding. SQL argument conversion remains a
possible later allocation target, but replacing database/sql transaction and
cancellation semantics requires its own evidence. Native HTTP request/header
copies also require careful asynchronous ownership review before changing.

`active-stage-alloc-list.txt` attributes 111.54 MB of flat allocation to the
unconditional traffic-shaper decision helper call at source line 989. The
no-policy workload never selects a shaper, but passing the decision by value
to a helper returning its address makes that copy escape. The completed
[selected-branch experiment](../shaper-bypass-guard/README.md) removed that
allocation site, but its mixed paired throughput and tails did not establish
a sustained benefit. The production call site remains unchanged.

The extraction commands use `go tool pprof -top`, `-top -cum`, and
`-top -alloc_space` with the recorded executable and profiles. Raw `.pprof`
files remain in the ignored run directories in `provenance.json`; committed
text tables and JSONL results retain their SHA-256 hashes. The runner is the
committed 3ec137c `scripts/qualify-proxy-performance.sh`, invoked with
`--runs 1 --decisive-only --scenario-filter h1-keepalive-duration
--path-filter direct --decisive-concurrency 128 --duration-seconds 10
--warmup-seconds 2`, the recorded server binary for candidate and driver, and
separate `--profile-mode cpu` / `contention` passes with profiling enabled.

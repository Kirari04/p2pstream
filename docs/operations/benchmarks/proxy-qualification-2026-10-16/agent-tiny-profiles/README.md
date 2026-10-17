# Real-endpoint profiles of the tiny-response regression

These diagnostic replays use the identical archived server and agent binaries
from the [failed unprofiled qualification](../agent-tiny-response/README.md).
Each of two sequential passes runs only legacy then aggregate H2 at 128 streams,
with 2 seconds of warmup and 10 timed seconds over the same 1 ms configured RTT,
1 Gbit/s path. The first pass enables native Go test CPU and sampled allocation
profiles at both real endpoints; the second separately enables mutex and block
profiles. Profiles cover each endpoint's entire finite lifetime, including
startup and cleanup. They do not replace the three alternating unprofiled pairs
or change any performance gate.

The runner differs only by filtering both workload-order lists to H2 c128;
`runner-filter.patch.txt` records the exact change. `--runs 1` follows
`--tiny-response`, overriding that flag's three-repetition default. The runner
exits 1 after preserving both rows because its unchanged qualification summary
still requires all four cells and three repetitions. Its log records
`h1-c16 legacy sample count = 0, want 3`; this is expected for the filtered
diagnostic and is not an endpoint or response failure.

All four timed rows conserve attempted, useful and persisted event counts:
196,008 legacy and 158,743 aggregate in the CPU/allocation pass; 188,356 and
153,212 in the separate contention pass. All payload/status/protocol checks
passed. Profiled timing is diagnostic only.

The clearest allocation targets are in aggregate flow control. The agent's
sampled whole-lifetime allocation estimate is 191.41 MiB versus 62.23 MiB for
legacy, despite fewer completed warmup-plus-timed requests (190,640 versus
233,304). Aggregate `publishAggregateCredit` accounts for 93.51 MiB cumulatively;
its stream-refund and global-refund paths account for 51.00 and 42.50 MiB
cumulatively. These overlap with the 45 MiB flat allocation estimate for
`waitForSendErrCommit`. Separately, `broadcastSendLocked` accounts for 41 MiB.
Allocation totals measure churn, not resident memory, and include startup,
profiling and cleanup. They justify investigating bounded refund publication
and wakeup allocation without expanding receive windows.

Whole-lifetime CPU needs a further distinction. The aggregate agent profile
contains 33.47 CPU seconds over 15.07 seconds, versus 18.22 over 12.28 seconds
for legacy. Its `Stream.Read` wait-select line accounts for 9.53 CPU seconds.
The independently sampled **timed** agent CPU is only 19.95 versus 15.84 seconds,
so a large part of the profile difference occurs outside the timed interval.
This suggests a cleanup/shutdown hotspot; the whole-profile select cost cannot
be attributed to the throughput regression. Focused annotated reports preserve
that line-level evidence for source review. Large block-profile totals include
ordinary idle and network waits and are not independently proof of contention.

`reports/` contains endpoint CPU, sampled allocation, mutex, block, cumulative
and focused text reports. `manifest.json` preserves executable and wrapper
hashes, exact commands, native-profile SHA-256 identifiers and local blob paths.
Native profile blobs and binaries remain local rather than in Git. The binary
hashes in each raw row are the wrapper hashes; the manifest identifies the
underlying archived binaries. All tracked evidence is covered by `SHA256SUMS`.

For replay, first build the exact binaries with the source and legacy-overlay
instructions in the linked failed qualification. Substitute those binary paths
and a fresh writable profile-output directory into the copied wrappers, then
substitute the wrapper paths and output path into each `command.json`. Run the
two commands sequentially using the copied filtered runner. For report replay,
`manifest.json` records each exact `go tool pprof` invocation. Raw profiles must
be captured again or read from their recorded local locations; text reports
alone cannot regenerate native profiles.

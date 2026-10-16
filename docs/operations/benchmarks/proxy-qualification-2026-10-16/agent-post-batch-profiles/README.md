# Post-batch real-endpoint diagnostic profiles

This finite replay profiles the remaining H2/c128 tail failure after immutable
`5288994b012c7566999ece140d7bdaa8bbd9d984`. It reuses exactly the archived
binaries from the [bounded refund-batch qualification](../agent-tiny-response-refund-batch/README.md),
with the same current server and either the frozen legacy or aggregate agent.
It runs one legacy-then-aggregate pair at 1 ms configured RTT, with 2 seconds of
warmup and 10 timed seconds. Native Go test CPU and sampled allocation profiles
cover both real endpoints' complete finite lifetimes. These are diagnostic
profiles, not a timing qualification or a replacement for the failed full
three-pair matrix.

The copied focused runner collects only H2/c128. Its exact four-line collection
and summary filter is saved in `runner-filter.patch.txt`. `--runs 1` deliberately
follows `--tiny-response`; the unchanged summary still requires three samples
and exits 1 with `h2-c128 legacy sample count = 1, want 3`. All 187,833 legacy
and 168,107 aggregate timed requests completed usefully with exact persisted
events and no request error. Warmup completions were 37,136 and 34,000.

The shutdown spin seen in the earlier whole-lifetime profile is absent from
this profile's dominant costs. The legacy agent captured 17.89 CPU seconds over
12.28 seconds; aggregate captured 21.46 over 12.24 seconds. Independently sampled
timed agent CPU was 15.63 versus 18.46 seconds, and server CPU 35.50 versus 35.96.
Whole-profile costs include warmup, startup and cleanup; profiled throughput
ratios are not acceptance evidence.

The measured residual CPU target is the physical sender. Aggregate agent
`sendLoop` accounts for 7.47 CPU seconds (34.81% cumulatively): its header-write
line accounts for 4.65 seconds, its body-write line for 2.24 seconds, and body
copying for 0.03 seconds. At the server, the corresponding sender totals are
6.88, 4.60 and 1.97 CPU seconds. The header line handles both DATA headers and
header-only controls/refund batches, so it is not an isolated DATA-header cost
or a prediction of removable CPU. This evidence supports a separate bounded
header/body gather experiment with unchanged ownership and framing.

The profile also exposes allocation churn introduced by the initial fixed-size
batch representation. Aggregate agent sampled whole-lifetime allocations are
263.27 MiB; `publishAggregateRefund` accounts for 181.06 MiB flat and 197.06 MiB
cumulatively. Annotated reports attribute 73.03 MiB to the work argument and
71.03 MiB to its active-parts array, plus 20 MiB to fresh error channels and
5.50 MiB to batch headers. The server's two array sites account for 66.03 and
63.02 MiB. These totals measure churn, not resident memory. Publisher CPU is
0.86 seconds (4.01% cumulatively), but that excludes physical control writes
executed by the separate sender goroutine. Array reuse remains a separately
recorded target; this replay does not qualify an implementation of it.

`reports/` preserves CPU, cumulative CPU, allocation-space/object estimates and
annotated sender/publisher reports. `manifest.json` maps executable-wrapper
hashes from the raw rows to the actual frozen binaries, and records exact
commands and native-profile SHA-256 identifiers. Native blobs and binaries stay
at their recorded local paths. No mutex/block replay was needed to identify
these residual sites. All tracked artifacts are covered by `SHA256SUMS`.

For replay, use the archived-source and frozen legacy-overlay build instructions
in the linked qualification, substitute fresh paths into the copied wrappers
and manifest command, and run the filtered runner once with its native profile
flags. The recorded `go tool pprof` commands regenerate the text reports when
the native blobs are available. Native profile blobs cannot be reconstructed
from text reports alone.

# Native Go application-cost profiling pair

After all four cells in the unprofiled matched attribution cohort met their fixed profiling trigger, one production-then-native HTTP/H1/c16 pair used the same immutable ordinary Go 1.26.6 binary, origin, socket/transport settings, two-second warmup, ten-second timed load, CPU set and 4 GiB/no-swap envelope. Symmetric Go test CPU/heap flags capture both complete proxy process lifetimes; the production-only profile environment hook remains unset. This is diagnostic evidence, not a repeated throughput qualification or a production retention gate.

| Whole proxy lifetime / warmup plus timed completions | Production | Native |
|---|---:|---:|
| Warmup + timed completions | 411,173 | 573,526 |
| Sampled CPU per completion | 109.93 µs | 76.72 µs |
| Allocated bytes per completion | 10,357.04 | 7,109.59 |
| Allocated objects per completion | 139.43 | 79.10 |

Both timed rows pass exact payload/status/protocol checks (342,085 and 475,645 timed completions), and production event persistence is exact. Four matching-certificate smoke arms pass. Natural process/container cleanup leaves no owned resources, with six attempted results and two accepted profile rows. All four profile blobs parse and pass `go tool pprof`; their hashes and positive sample totals are recorded in the rows and analysis verification.

The normalized syscall/header costs are close across roles. The distinct SQLite/cgo event-persistence path is a useful next experiment target: SQLite statement execution accounts for 12.79 µs per production completion cumulatively, overlapping with 12.82 µs flat cgo cost. These figures must not be added. The event-insert subtree allocates about 1,202 bytes per production completion. Full details, stack attribution, commands and source references are in `analysis/REPORT.md`. The next approved screen compares a hierarchical 64-row → existing 16-row → single-row insert with baseline. A simple 16→64 constant change would regress partial batches, and the existing 35-row tests would not exercise a 64-row statement. The screen must preserve all fields, rollups/touches and cancellation across an actual 64-row boundary before measuring 63/127/512-event flushes. No such production change is retained by this profile.

Normalization is a scale aid. Profiles include startup/readiness, warmup, timed requests and shutdown, allocation totals are sampled, and one sequential pair has no repetition or statistical confidence. Percentage shares alone cannot identify removable cost or predict a speedup. Existing transport read-hit and shaper-bypass experiments remain rejected.

## Provenance discrepancy

The original derivative runner's final cleanup rewrite retains its outcome binding but leaves successful `rows`, `results_sha256`, and `summary_sha256` null in final `raw/provenance.json`. The original file is preserved. `analysis/verification.json` independently checks exact rows, protocols/events, resource envelopes, natural outcome, profile/report identities and computes the missing result/summary/provenance bindings. The separate `binding-manifest.json` binds original raw files and all four native profiles; root `SHA256SUMS` additionally binds all copied artifacts. This reporting discrepancy does not change raw runtime observations or turn the profiled pair into a throughput acceptance cohort; no traffic was rerun to repair metadata.

`source/` contains the separately hashed derivative runner, exact diff from the unchanged attribution runner, lineage manifest and six passing pure contracts. It verifies the original bundle rather than rewriting historical build manifests or rebuilding Go code. `raw/` preserves the attempted/accepted rows, summary, outcome, smoke and text profile reports; `analysis/` preserves normalized totals and additional annotated/cumulative reports. The four native profile blobs and executable remain at the local paths and hashes in `local-profile-artifacts.json` and the frozen native attribution build manifest. Private fixture keys and scratch databases remain untracked. `export-map.json` maps copied bytes to local originals. Replay the exact runner using the derivative manifest SHA recorded in its lineage and a new disk-backed output parent; use `analysis/COMMANDS.md` for text extraction, with the recorded binary and native profile paths.

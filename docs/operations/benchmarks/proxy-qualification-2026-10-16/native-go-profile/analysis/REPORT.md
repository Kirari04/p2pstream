# Native profile pair analysis

## Scope and result

This is a diagnostic-only comparison of one production → native HTTP/H1/c16 pair. Both arms used the same ordinary Go 1.26.6 binary, 2 seconds of warmup, 10 seconds of timed load, proxy CPUs 0–3, load CPUs 4–15, and a 4 GiB/no-swap container envelope. Profiles cover server startup, readiness, warmup, timed load, and teardown.

The run completed cleanly: six attempts (four smoke and two timed), two accepted timed rows, zero request errors, exact 256-byte responses, exact production event conservation, no OOM events, no forced cleanup, and no remaining process groups or containers. The matched-certificate smoke passed.

## Absolute normalization

The divisor is `warmup_requests + useful_completed`, verified independently against each profile's raw sample totals.

| Metric | Production | Native | Production − native |
|---|---:|---:|---:|
| Normalization completions | 411,173 | 573,526 | −162,353 |
| Timed throughput | 34,204.86 req/s | 47,556.51 req/s | −13,351.65 req/s |
| Sampled CPU | 109.93 µs/completion | 76.72 µs/completion | +33.21 µs/completion |
| `alloc_space` | 10,357.04 B/completion | 7,109.59 B/completion | +3,247.44 B/completion |
| Sampled allocated objects | 139.43/completion | 79.10/completion | +60.33/completion |

These are whole-lifetime, sampled values. They provide scale; the single sequential pair does not establish causality or statistical confidence.

## Profile evidence

The largest transport costs are nearly symmetric after normalization:

| Node | Production | Native |
|---|---:|---:|
| `linux.Syscall6` flat CPU | 32.35 µs/completion | 31.49 µs/completion |
| `internal/poll.(*FD).Write` cumulative CPU | 23.54 µs/completion | 23.16 µs/completion |
| `internal/poll.(*FD).Read` cumulative CPU | 9.39 µs/completion | 9.61 µs/completion |
| `net/http.Header.Clone` flat allocation | 901.74 B/completion | 895.20 B/completion |
| `net/http.Header.Clone` cumulative CPU | 1.51 µs/completion | 1.27 µs/completion |
| `runtime.mallocgc` cumulative CPU | 8.03 µs/completion | 7.27 µs/completion |

This argues against treating sockets, header cloning, or GC as the main production-specific discrepancy in this pair. `Request.WithContext` allocation is higher in production (595.65 versus 359.37 B/completion), but it is smaller than the event-persistence allocation subtree.

Production-only or production-specific nodes provide the clearest actionable evidence:

| Node | Absolute normalized value |
|---|---:|
| `runtime.cgocall` flat CPU | 12.82 µs/completion |
| `go-sqlite3.(*SQLiteStmt).execSync` cumulative CPU | 12.79 µs/completion |
| Event insert subtree cumulative CPU | 3.70 µs/completion |
| Event insert subtree cumulative allocation | 1,201.95 B/completion |
| `database/sql.driverArgsConnLocked` flat allocation | 962.22 B/completion |
| Event argument boxing flat allocation | 197.64 B/completion |
| SQLite `bindValue` flat allocation | 53.56 B/completion |
| Route target selection cumulative CPU | 0.95 µs/completion |
| Route resolution cumulative CPU | 1.12 µs/completion |
| Admission reservation cumulative CPU | 0.80 µs/completion |
| Admission reservation cumulative allocation | 142.82 B/completion |
| Public context initialization cumulative CPU | 1.97 µs/completion |
| Public context initialization cumulative allocation | 595.59 B/completion |

Cumulative nodes overlap descendants, and `runtime.cgocall` overlaps work represented by SQLite stacks; these rows must not be added together. Flat allocations are sampled estimates. The table reports absolute normalized measurements rather than interpreting percentage-share differences as causal effects.

## One prioritized next step

Measure a larger fixed multi-row SQLite event insert, starting with 64 rows versus the current 16, while preserving all event fields, the bounded recorder, the existing transaction, and exact event conservation.

The frozen source drains up to 512 events per recorder flush, but `ProxyRequestEventInserter` executes them in fixed groups of 16. Each event binds 23 values, so a 16-row statement uses 368 variables and a 64-row statement would use 1,472. The pinned `go-sqlite3` amalgamation exposes a 32,766-variable ceiling. Moving from 16 to 64 would reduce `sqlite3_step` executions for a full 512-event flush from 32 to 8 while retaining bound parameters and the existing security/event behavior.

This is the first experiment because the matched profile shows 12.79 µs/completion under SQLite `execSync`, 12.82 µs/completion in cgo, and 1,201.95 B/completion in the event-insert allocation subtree. It will not remove per-field binding or `database/sql` argument conversion, so any gain must be demonstrated under the same exact event-conservation checks. No event or security feature should be disabled.

## Evidence discrepancy and limits

The final `provenance.json` correctly binds the outcome, runner, lineage manifest, binary, and derivative patch, but its final cleanup rewrite leaves `rows`, `results_sha256`, and `summary_sha256` null. The actual results and summary hashes are recorded in `verification.json`, and every response, event, resource, profile, report, and outcome check passes independently. This is a provenance-binding limitation, not a runtime-correctness failure.

The profile is one sequential pair. Allocation profiles are sampled cumulative `alloc_space`; CPU samples include lifecycle work outside request handling. Follow-up work should retain the same hostile-input correctness, event conservation, cgroup, cleanup, and matched-pair checks.

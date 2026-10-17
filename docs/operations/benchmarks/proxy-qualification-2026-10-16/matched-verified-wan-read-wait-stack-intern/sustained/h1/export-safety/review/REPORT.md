# Sustained H1 execution-trace export-safety review

Verdict: the four closed H1 raw Go execution traces are acceptable for the
diagnostic evidence export when accompanied by exact hashes, original
reduction and status records, and this report. The single reviewed safety run
returned final status 0; all four pinned `go tool trace -d=wire` invocations
and the audit returned 0 with empty stderr. Pre/post tool, source, format, and
index hashes are identical.

| Sequence | Raw bytes | Raw SHA-256 | Adapter/parser/canonicalizer | Original analyzer |
|---:|---:|---|---|---:|
| 9 | 2,694,361 | `2af2e15c73d88e6619f5833d26c276f0d290c4d4941bdf92f0e6364cadab1130` | 0/0/0 | 1 |
| 11 | 2,142,077 | `1289428cb6965ba3f0aa1cc3a0e4f763d874f19193b96eb29054922417cd4515` | 0/0/0 | 1 |
| 14 | 2,184,944 | `52f2ff5676606f7f542e4f61afa7c24d32c8d2ae63817d037546adbf088ed4d7` | 0/0/0 | 1 |
| 15 | 2,057,661 | `93b9e7665c26e302a550e5d4b4476488f67f021a234b7b3ab3b0b6091e6822f5` | 0/0/0 | 1 |

Every raw, wire, physical canonical, logically expanded canonical, and
reduction scan found no configured sensitive label. The raw, wire, physical
canonical, and logical scans found zero lowercase 64-hex runs. Every wire
string inventory contains the exact constant `wan-read-wait-request` task and
`public-proxy-handler` region, contains no NUL string or 64-hex value, and has
only accepted runtime labels. Logical annotation inventory found exactly one
task pair, one region pair, and zero user log events in each trace.

The sensitive values were loaded only in memory from excluded closed-run
paths. No value, fragment, length, or secret-derived hash was emitted. The
generated trace-control bearer was process-local and was not persisted, so
exact bearer-byte comparison is unavailable. The text inventory and generic
64-hex rejection are supporting controls rather than proof against arbitrary
binary encoding of that unavailable value.

Raw traces disclose whole-server scheduler and timing events, goroutine,
thread, and processor identifiers, GC and heap metrics, function names,
absolute source/build/module-cache paths, and trace wall-clock synchronization.
Compact canonical traces, logical expanded content, wire dumps, TLS material,
readiness/config/environment records, and endpoint logs are excluded from the
export.

All four original analyzer statuses remain 1 because positive waiting
intervals lack `TransitionStack`. The export-safety verdict does not validate
causal attribution and does not replace the original analyzer or whole-cohort
failure status.

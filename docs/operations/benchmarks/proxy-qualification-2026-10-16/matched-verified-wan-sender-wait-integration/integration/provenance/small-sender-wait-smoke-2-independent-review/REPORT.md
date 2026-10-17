# Independent raw review: small sender-wait smoke 2

This review reads the closed H1 and H2 artifacts only. It does not rerun the arm validator, endpoint, or workload. The canonical 259-file run ledger (`83770a2a4e978d9be76954753c704c1ab2d511eef096aa8cfcc95646141ccae8`) and result-review final ledger (`37c4e3d836f2b52542369d1e58526ce0ea86e895251fb90266bdbda77d3b9a31`) verify with status 0.

## Verdict

The raw correctness evidence is accepted independently for both arms. Each arm contains exactly 16 responses in the fixed order, with exact 4 MiB configured payloads, successful status 200, complete response-copy records, and the expected public protocol on every response. H1 records HTTP major 1 and ALPN `http/1.1`; H2 records HTTP major 2 and ALPN `h2`. Every response binds the expected leaf certificate fingerprint. The origin side remains HTTP/1 as configured.

This is a small diagnostic fixture. Its rates are not a sustained performance gate and this review makes no performance acceptance claim.

## Lifecycle and resources

Each arm has five inner `process-exit` records: agent, server, origin, agent namespace, and router namespace. All have natural status 0, `group_joined=true`, `forced=false`, and `invalidates_success=false`. The outer runner has status 0, joins its group, and reports no forced cleanup. The delegated scope launcher and sentinel join; the final unit query is status 0 with `not-found/inactive/dead`.

Both outer namespace attestations record private user, PID, mount, and network namespaces, shell PID 1, Python parent PID 1, and an unchanged cgroup namespace. Both server resource attestations accept the 4 GiB memory maximum, zero swap, unbounded `memory.high`, zero memory events, and a peak within the limit. Peak memory was 57,946,112 bytes for H1 and 62,726,144 bytes for H2.

The later `supervisor-cleanup.json` has `entry_status=1` because the arm validator failed after the runner had already completed and its PID had been cleared. It records `forced_supervisor_cleanup=false`. It is not evidence of a failed runner join; `supervisor-lifecycle.json` is the raw join record and is natural/complete for both arms.

## Sender-duration intervals

The fixed selected sequences are 9, 11, 14, and 15. All eight records have status 0 for transfer, sender pre/post queries and validation, resource queries, and health checks. Each pre-control request finishes before its copy handler starts, and each handler returns before its post-control request begins.

Each selected post snapshot identifies exactly one lane and stream ID 6. DATA frame, payload, completion, physical attempt, and physical success counts conserve exactly. Physical wire bytes equal DATA payload bytes plus 12 bytes per frame. Allocation charge is page-aligned and within the per-frame bound. Error, short-write, abandoned, ambiguity, overflow, pending ownership, and active ownership fields are zero. DATA call duration telescopes exactly into enqueue-select plus completion-select duration; physical-write duration does not exceed the DATA-call duration.

| Arm | Sequence | Lane | Frames | DATA payload bytes | Physical wire bytes | Credit-wait ns | DATA-call ns | Physical-write ns |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| H1 | 9 | 1 | 180 | 4,194,532 | 4,196,692 | 715,789,192 | 5,735,341 | 4,666,745 |
| H1 | 11 | 3 | 150 | 4,194,532 | 4,196,332 | 718,031,881 | 5,565,305 | 4,549,966 |
| H1 | 14 | 2 | 134 | 4,194,517 | 4,196,125 | 716,777,216 | 5,950,443 | 4,966,426 |
| H1 | 15 | 3 | 133 | 4,194,517 | 4,196,113 | 559,958,445 | 3,087,127 | 2,440,106 |
| H2 | 9 | 1 | 178 | 4,194,532 | 4,196,668 | 717,278,045 | 6,431,588 | 5,199,993 |
| H2 | 11 | 3 | 178 | 4,194,532 | 4,196,668 | 717,020,194 | 6,644,335 | 5,341,486 |
| H2 | 14 | 2 | 149 | 4,194,517 | 4,196,305 | 719,719,772 | 5,766,926 | 4,806,674 |
| H2 | 15 | 3 | 150 | 4,194,517 | 4,196,317 | 560,396,955 | 2,611,845 | 2,174,556 |

The DATA payload domain includes HTTP response bytes and headers. It is conserved separately from the exact 4 MiB load response; the table must not be read as claiming DATA payload equals load payload.

Both terminal sender records and saved terminal validations have status 0 and `closure_valid=true`. All lanes have zero active reservation, caller, enqueue, queued, physical, and credit-wait ownership at the terminal boundary.

## Reuse

Sequences 13 through 16 revisit the exact four physical connection keys. Every revisit retains the same origin connection ID and has an observed idle age under 30 seconds. H1 idle ages range from 4.064 to 4.234 seconds; H2 idle ages range from 4.095 to 4.269 seconds.

## Preserved validator discrepancy

The original arm validators remain status 1 with the exact stderr `WAN protocol arm validation failed: preflight hash manifest is incomplete`. Their emitted attestations contain no other validation issue and classify the observations as complete. A read-only set comparison finds no missing preflight keys and exactly four additional correctly bound keys in each arm: `base_cp7_checkpoint`, `base_cp7_reviewed_manifest`, `base_cp7_runner`, and `sender_harness_receipt`. This independent raw verdict does not rewrite the original arm result or claim official validator acceptance.

The first independent audit attempt is preserved under `attempt-1/` with status 1. Its local audit source incorrectly required bulk-lane `complete/reset_performed` semantics on disabled control lane 0. Revision 2 corrects only that audit predicate and exits 0.


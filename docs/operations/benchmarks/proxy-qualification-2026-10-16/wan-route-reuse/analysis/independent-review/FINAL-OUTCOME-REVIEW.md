# Final original96 WAN outcome review

**Decision: the scoped original WAN regression gate clears. Retain the same-target idle transport preference for the normal `dev` PR process.** The completed run is valid and positive under the unchanged original qualification contract. This is not general throughput parity, an isolated causal percentage improvement, or proof that every slow transfer has disappeared.

This independent review read saved evidence, recomputed arithmetic and checked file identities. It did not execute validators, tests, builds, traffic or another namespace scan. Root's extractor exited naturally with status 0, empty stderr and `complete_valid_positive`; the driver and all saved runner/validation/default-proof statuses also passed.

## Original performance gates

Ratios below are aggregate/legacy steady throughput medians. Every per-pair and pooled gate requires at least 0.90.

| Comparison | Order | Download | Upload |
|---|---|---:|---:|
| Pair 1 | Legacy / aggregate | 0.9849439286 | 1.0065439280 |
| Pair 2 | Aggregate / legacy | 0.9711889528 | 1.0293070872 |
| Pair 3 | Legacy / aggregate | 0.9889095447 | 1.0378428584 |
| Pooled | Nine steady samples per mode/direction | 0.9864852517 | 1.0293647613 |

The independent calculations match the canonical attestation and summary. Pooled download medians are 36.9323 MB/s aggregate and 37.4382 MB/s legacy; upload medians are 39.4898 and 38.3632 MB/s. Pooling uses the original nine-sample medians, not averaged pair ratios or the earlier diagnostic screen's harmonic criterion.

All 96 scheduled transfers are present in exact order: 48 lane warmups, 12 retained warmups and 36 steady transfers across three pairs. All have one completed attempt, zero errors, the exact 256 MiB configured payload and expected response bytes, H2/HTTP 200, and one persisted usage event each. Rate/payload/wall arithmetic and current provenance agree. No events were dropped. The 24 aggregate post-warmup rows retain four grown bulk pools at both endpoints, lanes 1–4; the smallest observed pool is 75 MiB, above the unchanged 8 MiB threshold. Pre-timing quiescence remains the original runner's gate; this review does not reinterpret mid-transfer snapshots as quiescent.

**Residual limitation:** aggregate retained-download rows 25 and 41 achieved only 14.3414 and 14.6476 MB/s. Those transfers succeeded but remain slow. Retained warmups were excluded from steady medians by the original contract, before this run; no outcome-dependent exclusion was introduced. The result supports the scoped steady-state fix, not elimination of cold/retained-transfer latency.

## Lifecycle, defaults and scope

All 15 recorded private-process exits are natural zero exits with joined process groups and no forced-success invalidation: twelve endpoint processes, the shared origin, and two namespace helpers. One origin and one topology served all three pairs. The outer supervisor exited 0 with accepted cleanup, no adopted descendants, no remaining owned identities and no force action. Artifact use was 1,721,272 bytes against the fixed 268,435,456-byte limit; no traffic retry occurred.

Host, nested runner, agent/origin and router pre/post snapshots consistently record CUBIC and integer `tcp_slow_start_after_idle=1`. Helper PID/start-time/netns and lifecycle identities agree; host wrapper-parent identity is stable. The runner correctly joins the canonical runner's second network namespace, not the outer wrapper's distinct network namespace. These are factual point-in-time default observations, not continuous monitoring or per-socket controller measurements. No OS policy write was added.

The optional final host scan found no matching owned namespace among 588 readable links from 464 processes, but 1,268 links were permission-denied. Therefore it proves only absence among readable entries. Natural ownership closure and private lifecycle joins are independently accepted; no complete host-to-private endpoint PID mapping or universal namespace-disappearance claim is made. No cgroup ownership claim is needed.

## Tested identity and recommendation

All 530 frozen SOURCE entries were independently rehashed after completion without mismatch, including the 430 compiled production-source files and all three binaries. The tested current reference is explicitly `bd593420a6905f8922bb96d07ec351483c0df2c3+public-route-idle-reuse-source-b359dc67c290193f68bf33519853df3473e31f9a4d8e718c0bd1ccc282d14947`; it does not claim that uncommitted changes were present in the base commit.

The same freshly built ordinary, untagged server served both modes; aggregate used the matching fresh ordinary agent, while legacy reused its exact proven binary. Build receipt `d428e45fd861c8ed7a4346920e53c2a56d1fe678660fd343707f9841f53af624` binds that pair. The timed transfer function remains byte-identical to the original runner. Prior focused normal and race checks each passed 69 top-level tests, including 96 subtests/test entries, without source drift.

The production change prefers an eligible idle transport for the same complete route key and atomically claims it through body lifetime, while preserving four-lane warmup, busy fallback and control isolation. Together, the reviewed source, focused checks and this original96 result justify retaining it. No earlier controller, idle-policy or early-share diagnostic is being relabeled as qualification; no comparison against the historical failed cohort establishes an isolated causal improvement. Normal PR/CI review remains separate from this WAN gate.

Exact reviewed inputs are in `FINAL-OUTCOME-INPUTS.json`; recomputation and scope observations are in `FINAL-OUTCOME-ARITHMETIC.json`. `FINAL-OUTCOME-HASHES.json` binds these three review artifacts to the saved outcome and frozen SOURCE.

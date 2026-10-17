# Suggested fixed offline screen — not executed or authorized yet

Run only after the coordinator grants CPUs and accepts this source proposal. Use immutable baseline `27c8e31f3826ab5326d941b20644c1f56159d99d` and candidate equal to that exact source plus candidate.patch. Do not use evidence-only HEAD as an unrecorded runtime replacement. Both arms receive the same temporary correctness/benchmark overlay. Bind source, patch, overlay, pinned Go 1.26.6 and binary hashes; retain failed attempts. No production configuration or batch/schedule is changed.

## Correctness

Prefer real persisted-row coverage. Add an identical database-level overlay to both arms using the existing SQLite fixtures: exercise single Insert and fixed 16-row batches with a short remainder; use distinct non-nullable fields and all eight positive valid FK IDs; then set each nullable field's Valid=false with a nonzero nonexistent payload and verify it persists as SQL NULL (including SQL typeof/is-null checks). Verify all 23 stored fields and row order through SQL reads. This should pass on baseline and candidate and must not call or duplicate the private helper as an oracle.

For optional valid-zero, negative and signed-extreme IDs, compare baseline and candidate database outcomes under identical seeded FK fixtures: SQL values/types on success; constraint classification and complete rollback on failure. Do not assume zero is a valid FK, convert it to NULL, or expect an invalid nonzero payload to violate a FK when Valid=false. The baseline's actual database behavior is the reference. Existing test coverage remains the primary contract oracle; source proof supplies the exact nil/zero/nonzero conversion for all eight fields.

Use the existing server tests unchanged as the persistence/ordering/error oracles:

- TestPreparedProxyRequestEventBatchPreservesAtomicRowsRollupsAndTouches: all 23 fields, mixed valid IDs/NULLs, ordered 35 rows across 16+16+3, raw/rollup/cache-touch transaction rollback, and invalid FK at rows 0/17/32.
- TestPreparedProxyRequestEventBatchCancellationRollsBack: cancellation inside the second 16-row SQLite group must retain context.Canceled and roll back raw rows, all rollups and cache touch.
- TestObservabilityRecorderFlushesQueuedEventsAsAggregatedRollups, TestObservabilityRecorderCoalescesPublicCacheTouches and TestObservabilityRecorderCoalescesCacheTouchesPerGeneration.
- TestObservabilityRecorderBoundsPendingEventsWhenDatabaseFails, TestCloseObservabilityRecorderFlushesQueuedEvents, TestCloseObservabilityRecorderCancelsActiveFlushAndDrains, TestObservabilityRecorderCanceledCloseCanRetryWithFreshContext.

Run these once per arm normally; run the candidate focused set once under race. All must pass. Normal/failure/cancellation behavior is a prerequisite, not a performance tradeoff. Existing repository and CI checks remain required if the patch is later implemented.

## Balanced allocation and timing measurements

Use identical fixed 512-event workloads, unchanged 16-row execution groups and the same SQLite DSN/transaction/persistence rules. Two bounded measurements:

1. Existing `BenchmarkPreparedProxyRequestEventBatchFlush/(background|production_deadline)` unchanged, including raw/rollup writes and the real cancellable five-second production context. Its current fixture has all IDs NULL. Preserve its out-of-timer table cleanup. Do not replace production_deadline with a Background-only shortcut.
2. A temporary insert benchmark overlay executing the existing PrepareProxyRequestEventInserter → InsertBatch → Close within a transaction on 512 events; reset table state outside timing. Four fixed data shapes: all NULL IDs; ordinary direct event with valid listener/route/target IDs and other IDs NULL; all eight IDs valid and small; all eight IDs valid and >255. Create required FK rows outside timing; use distinct immutable event data. Run both Background and the same five-second cancellable context. Do not vary SQL batch length, SQL text, schema or event count between arms. The benchmark is measurement code only and is identical in both builds.

Pin the measurement process to CPU 0, GOMAXPROCS 1, cached Go 1.26.6; use CPUs 0–7/GOMAXPROCS 4 only for the preceding serial builds/tests. No pressure, WAN, server, test, compilation or other heavy work overlaps measurements. For each benchmark case, collect exactly ten samples per arm with `-benchmem -benchtime=300ms -count=1 -cpu=1`, in ten balanced pairs: AB,BA,AB,BA,... . Each arm/case must report the exact 512 events/op so B/op and allocs/op can be normalized per event. Preserve every sample and command; no extra runs to chase a pass.

Predeclare retention criteria before execution: all correctness/race checks pass; each insert data shape in both contexts saves at least 64 B/event and four allocations/event; existing full flush cases save at least 64 B/event and four allocations/event; median candidate ns/op is no more than 1.03× baseline in every case. The source mechanism targets up to 128 B/eight nullable boxes, so these thresholds require a material demonstrated fraction rather than noise. If any requirement fails, stop and report; do not relax it, add iterations, enlarge SQL batches or infer success from background-only results. The coordinator may instead reject the source proposal without measurement.

This small allocation screen cannot establish whole-application throughput improvement or native/HAProxy/Caddy parity. No app/WAN profile or final capacity gate is implied by it. Broader checks only become appropriate after a retained implementation and explicit workload planning.

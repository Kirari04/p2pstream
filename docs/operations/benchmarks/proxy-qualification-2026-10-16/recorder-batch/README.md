# Deadline-aware raw-event batching

The existing prepared-insert benchmark used `context.Background()`. Production
flushes use `runBackgroundFlush`, which installs the normal five-second deadline
and a cancellable flush operation. The pinned go-sqlite3 driver executes those
contexts through a goroutine/channel cancellation path for each statement.
Both benchmark modes now exercise the same 512-event full recorder flush,
including raw rows, rollups and transaction commit. Table cleanup remains
outside the timed interval.

The candidate groups raw inserts into at most 16 rows (368 bound parameters)
per execution. It lazily prepares that statement from the generated single-row
VALUES tuple and retains the single-row statement for a short final remainder.
All request data is bound. Every group, rollup and cache touch still belongs to
the same transaction and uses the caller's cancellation context.

Three alternating baseline/candidate pairs, each mode at `500ms`, measured:

| Flush context | Baseline median | Candidate median | Baseline bytes / allocations | Candidate bytes / allocations |
| --- | ---: | ---: | ---: | ---: |
| Background | 3.922 ms | 3.685 ms | 741,993 / 10,016 | 703,966 / 7,623 |
| Production deadline | 6.379 ms | 4.072 ms | 854,376 / 12,171 | 716,571 / 7,858 |

Production-deadline wall time fell 36.2%, allocated bytes 16.1%, and allocations
35.4% in this microbenchmark. These are flush-level measurements, not a claim
about end-to-end request throughput. The same full pipeline must be measured
with event conservation after this checkpoint.

Both binaries came from clean archives of
`d08f271e572bcb7600478a1e6396fc99b8ffbf53` with the identical final
`internal/server/observability_prepared_insert_test.go` overlay. Only the
candidate additionally overlaid the inserter and rollup implementation files.
No uncommitted tunnel or transport changes entered either binary. Exact file
and binary hashes are in `provenance.json`; individual observations are in
`benchmarks.jsonl`. Go 1.26.6, `GOMAXPROCS=4`, and CPU affinity `0,1,2,3` were
used for both sides; those four logical CPUs occupy two physical cores on this
host. Raw binaries, frozen source and logs remain locally in
`/tmp/p2pstream-sql-batch-pair.4uv3SB/`.

Replay after building the baseline and candidate with those explicit overlays:

```sh
for repetition in 1 2 3; do
  for variant in baseline candidate; do
    GOMAXPROCS=4 taskset -c 0,1,2,3 "$variant.test" \
      -test.run '^$' \
      -test.bench '^BenchmarkPreparedProxyRequestEventBatchFlush$' \
      -test.benchtime=500ms -test.count=1 -test.benchmem
  done
done
```

Focused normal tests, ten repeated race runs and the broader recorder,
dashboard and observability-cleanup suite passed. They round-trip all
23 fields across 35 rows, including valid foreign keys, NULLs and distinct
timestamps; test foreign-key rollback at rows 0, 17 and 32; and inject a
cache-touch failure after raw insertion. The cancellation regression blocks a
bounded test-only SQLite callback at row 17, cancels the normal five-second
transaction context after the first group has executed, releases the callback
unconditionally, and verifies that raw rows, every derived rollup table and
the cache touch all remain unchanged.

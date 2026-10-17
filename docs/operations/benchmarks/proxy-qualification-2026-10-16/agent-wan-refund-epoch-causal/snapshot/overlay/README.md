# Aggregate refund deferred-global epoch causal diagnostic

This diagnostic compares two ordinary, tag-off builds from application source
`5eeccaaa797254d7bde4db9854a337a02c3bc28e`.

Both variants apply `common-scaffold.patch` and retain the current selected-ref
registry, lazy send-wake behavior, shutdown join, DATA gather/copy ownership,
tagged diagnostic hooks, stream/session structures, and wire protocol.

- `refund-current-batch-control` leaves the current bounded joint refund batch
  unchanged: up to 16 distinct stream updates and the corresponding session
  update are one priority item, one accounting commit, and one physical write.
- `refund-streams-before-global-epoch` differs only by the private boolean in
  `deferred-global-toggle.patch`. It serially publishes at most 16 selected
  stream allocation refunds, counting terminal selections and same-ID
  requeues, then publishes their accumulated matching session refund. When a
  selection empties the stream queue, the same `aggregate.mu` observation may
  also include bounded global-only credit. If the epoch ends with queued
  streams, their unselected global matches remain pending.

The deferred arm caps the accumulated session update at the largest 4 KiB
aligned `uint32`. Each stream and session item has its own priority-queue
allocation and acknowledgment. This allocation and scheduler overhead is
intrinsic to the diagnostic.

One connection-write timer is shared across the epoch. Shutdown/deadline is
checked before each enqueue and after each successful acknowledgment, which
prevents another publication after termination is observed. Synchronous waits
for `controlHdrLock` or `publishMu` are not interruptible, so the deadline
bounds queue/ack waits after lock acquisition rather than arbitrary lock-holder
time.

Each selected stream refund and its matching session refund move from pending
to publishing atomically. A stream commit clears only its selected stream slot;
the accumulated session credit remains session-owned until the final session
update commits or joined session cleanup clears it. Terminal selected streams
suppress only the stream update and retain their matching session refund. Any
timeout, shutdown, short write, or write error permanently aborts publication;
there is no retry or speculative credit grant.

This bounded epoch is not a faithful restoration of the historical unbounded
drain and is not a production candidate.

## Portable replay

From a clean archive of the base source ref:

```sh
patch -p1 < common-scaffold.patch
# Apply only for refund-streams-before-global-epoch:
patch -p1 < deferred-global-toggle.patch
```

Both patches use canonical `a/third_party/...` and `b/third_party/...` paths.
Fresh forward application reproduces the exact recorded control/candidate
source trees. Actual reverse application restores the control and clean-base
trees, including removal of the diagnostic test file.

## Validation

Final validation used the absolute Go 1.26.6 toolchain recorded in
`build-env.txt`, with `GOTOOLCHAIN=local`, `GOWORK=off`, empty `GOFLAGS`, and
`TMPDIR`, `TEMP`, `TMP`, `GOTMPDIR`, `GOCACHE`, and `GOMODCACHE` on the owned
ext4 diagnostic tree.

The deferred arm passed:

```sh
go test ./... -run '^TestDeferredGlobal' -count=10 -timeout=120s
go test -race ./... -run '^TestDeferredGlobal' -count=5 -timeout=180s
go test ./... -run '^TestAggregate(RandomizedLifecycleConservesSessionPool|EarlySessionCloseClearsConcurrentTrafficCohort|RefundBatchCapsDistinctStreamsAndLeavesFIFO|RefundBatchAmbiguousWriteCommitsAtEveryHeaderBoundary|RefundBatchCloseClearsQueuedPrecommitOwnership|GrowAfterAndConcurrentWithCloseRetainsNoSlabs)$' -count=3 -timeout=180s
go test -race ./... -run '^TestAggregate(RandomizedLifecycleConservesSessionPool|EarlySessionCloseClearsConcurrentTrafficCohort|RefundBatchCapsDistinctStreamsAndLeavesFIFO|RefundBatchAmbiguousWriteCommitsAtEveryHeaderBoundary|RefundBatchCloseClearsQueuedPrecommitOwnership|GrowAfterAndConcurrentWithCloseRetainsNoSlabs)$' -count=3 -timeout=240s
go test ./... -count=1 -timeout=240s
```

The current arm passed existing aggregate refund focused normal count 10,
focused race count 5, and full Yamux normal count 1. Both arms compiled ordinary
tag-off `internal/agent` and `internal/server` test binaries. The copied
`driver-origin.test` is byte-for-byte identical to the current-arm server.

The `validation/superseded-go1.26.3` directory preserves the first focused
normal pass made with the host default Go after its version mismatch was
detected. It is explicitly non-final and supports no recorded build identity.

`SHA256SUMS` is generated last and deliberately excludes its own digest.

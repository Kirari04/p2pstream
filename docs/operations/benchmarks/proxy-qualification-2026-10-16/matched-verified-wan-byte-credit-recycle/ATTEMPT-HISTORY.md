# Attempt history

- Protocol source checkpoints, focused attempt 1 failure, focused attempt 2
  normal/race pass, strict ordinary-proof failures, and the accepted narrower
  equivalence supplement are preserved.
- Adapter checkpoints 1–11 preserve rejected schemas, validator corrections,
  the authenticated terminal-state correction, the successful nine-request
  route control, cleanup correction, and the frozen-checkpoint mutation incident.
- Build attempt 1 fails the inherited ordinary fixture boundary. Attempt 2 is
  the pre-terminal accepted build. Attempt 3 fails one stale test expectation.
  Attempt 4 passes but is provenance-invalid. Attempt 5 is accepted.
- Runner history preserves every failed or superseded source checkpoint,
  materialization, publication, and final-contract attempt. Runner source 10 is
  rejected only for unrelated mature checkpoint-path changes; a separate
  `02c1...` value was a reporting error, not a generated/timed-runner byte change.
- Tiny 1 is rejected after the terminal HTTP 409 and withholds H2. Tiny 2 passes
  both arms. Sustained 1 completes both arms naturally but fails throughput and
  reuse conditioning in both arms.

Exact source and result hashes are in `PACKAGE-MAP.tsv`, the embedded manifests,
and `ACCEPTED-BINDINGS.tsv`.

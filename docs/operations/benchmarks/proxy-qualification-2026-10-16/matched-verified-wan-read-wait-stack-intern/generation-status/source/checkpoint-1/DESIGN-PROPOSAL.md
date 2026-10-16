# Go generation-status wait continuity correction

This is a source-only diagnostic reducer derivative. It does not change the endpoint binary, trace capture, public handler, timed transfer runner, workload, timeout, floor, trace cap, compact representation, or lifecycle policy. The original sustained H1 reductions remain immutable and rejected.

## Observed input contract

The independent closed-input extraction is `../wan-read-wait-stack-intern-source/independent-canonical-partition-source/runs/sustained-h1-1/missing-wait-context.json` (SHA-256 `542c61cadaa212f20c5c352b489163be31f09caa91390311c4f0ef9539ac67c3`). All 39 previously missing-stack intervals have the same handler-resource transition:

- `Waiting->Waiting Reason=""`
- no `TransitionStack`
- the next handler-resource transition is `Waiting->Runnable`

Counts and following-interval durations are sequence 9: 16 / 12,032 ns; sequence 11: 8 / 4,224 ns; sequence 14: 8 / 7,168 ns; sequence 15: 7 / 3,392 ns. Resource GoID identifies the handler; the event header GID does not.

## Pinned Go 1.26.6 semantics

The canonical toolchain is `/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64`.

- `src/runtime/traceruntime.go:486-494` explicitly emits stackless `GoStatus(Waiting)` before unblocking when the status has not yet been emitted in the generation. Its comment says the waiting goroutine's stack cannot safely be acquired. File SHA-256: `9cc5ffc359ac31558fdc2cf721ce70df38f9cfe0c28aef9755f71250a56ae0cb`.
- `src/runtime/tracestatus.go:20-32` emits `EvGoStatus` when stack ID is zero. SHA-256: `4db02b2b49c5bc682913d8265b0c4f4ce2913d3b2997c6722b1a9f1345140843`.
- `src/internal/trace/tracev2/events.go:48` defines `EvGoStatus` as the goroutine status at the start of a generation. SHA-256: `1f67012ee9a2b913027a173cf6ca34423cc33d9403536a7252cc9457d7894657`.
- `src/internal/trace/order.go:364-385` validates an existing goroutine's status and records its current state as the old state, so a known waiting goroutine becomes `Waiting->Waiting`. SHA-256: `12675d3f9cf46064f625fab59bb2ac2188333040d7302a0c8cf21dc4b48f1f35`.
- `src/internal/trace/event.go:1092-1096,1301-1317` converts the status to a state transition and prints no `TransitionStack` for `NoStack`. SHA-256: `a46791f64767c5a8ce98e9d227603ebdc482fd3b3b4b5f01358d1ee7bd56ecd0`.
- `src/internal/trace/event.go:1088-1091` maps `GoSyscallEndBlocked` to `Syscall->Runnable`; it cannot produce the observed transition.

## Reducer rule

An exact `Waiting->Waiting`, empty-reason, empty-stack transition is a same-state status snapshot. The reducer still closes the preceding waiting interval at the snapshot timestamp and counts the transition, preserving every event ordinal and the state partition. It retains the already-active wait's reason and stack for the following interval rather than treating the snapshot as a new wait.

The output adds:

- `waiting_status_snapshot_count`
- `waiting_status_snapshot_carried_ns`

The second field is the exact duration after such snapshots for which active wait metadata was retained. No interval is dropped or coalesced.

A normal `Running->Waiting` transition without a stack still creates missing wait metadata and remains rejected by the existing `missing_stack_ns` gate. A snapshot after such a missing transition preserves missing metadata; it does not invent a stack. Old-state disagreement remains an immediate malformed-trace rejection.

## Focused source contracts

`run_generation_status_contracts.py` defines four bounded in-memory reducer controls, not yet executed:

1. One valid status snapshot preserves the prior wait reason/stack, exact partition, transition count, snapshot count, and carried nanoseconds.
2. Multiple consecutive snapshots retain one active wait and conserve all intervals.
3. A normal missing-stack wait followed by a snapshot remains missing and incomplete.
4. A `Waiting->Waiting` snapshot inconsistent with current state is rejected.

After source review, focused verification should run these controls and the existing compact 17-case reducer/format controls. Corrected reductions, if later authorized, must be written to a new offline root against the same immutable H1 canonical inputs and must not overwrite or reclassify the original failed reductions.

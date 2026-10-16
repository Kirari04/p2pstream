# Source-only design: tagged sender wait-duration observer

Status: design only. No patch, build, test, endpoint, or traffic is authorized.
The matched H2 arm must close before architecture decides whether to implement
this observer.

## Scope and invariants

Extend the existing `p2pstream_qualification` aggregate send counters rather
than create a new service, map, poller, or trace format. Production builds keep
the current empty disabled counter type and must have the same `Stream`,
`Session`, `aggregateWindow`, and `sendReady` sizes and the same generated
ordinary `Stream.write`, aggregate reservation, send-wait, and send-loop
instructions. Any implementation must prove those layout and object-code
checks; a compile-time-disabled branch is insufficient if it leaves ordinary
instructions or stack growth.

Tagged builds may add fixed scalar/array fields only to
`qualificationAggregateSendCounters`, whose disabled build remains an empty
type. They may use monotonic `time.Time` values and existing mutexes. They must
not allocate per DATA frame, retain payload/header bytes, add a map, change a
queue, frame size, lock order, timeout, wake condition, credit decision, or
request schedule.

## Credit-wait measurement

The unit is one pass through the existing WAIT select in
`third_party/yamux/stream.go:331-399`, not one zero-credit observation and not a
whole request.

At the exact decision that branches to WAIT, capture:

* stream ID and a monotonically increasing diagnostic token;
* a monotonic start time;
* one exact entry-cause value: `byte_zero`, `stream_allocation_zero`,
  `session_allocation_zero`, `stream_and_session_allocation_zero`.

The byte cause is all that was observed when the atomic byte window was zero;
the code must not inspect or infer aggregate ledgers that normal execution did
not reach. Aggregate causes are the `a.sendWindow == 0` and
`stream.allocationSendWindow == 0` values already observed together while
holding `aggregateWindow.mu` in `reserveAggregateSend`. A cause is an exact
combination, so overlapping bits are counted once and duration is not summed
once per bit.

Record the exact selected outcome after the existing select returns:
`byte_notify`, `aggregate_notify`, `shutdown`, or `timeout`. Selection is an
observation, not proof that the selected notification resolved the entry cause;
normal code retries `START` and a later WAIT becomes a new interval. Store
count, total nanoseconds, and maximum nanoseconds by exact cause and by exact
outcome, plus total wait count/time. Require the following conservation in the
snapshot:

```
total_wait_count == sum(exact_cause_count) == sum(outcome_count)
total_wait_ns    == sum(exact_cause_ns)    == sum(outcome_ns)
```

The tagged helper should timestamp the decision while its existing ledger lock
is held, then transfer a fixed handle to the single sending goroutine. Do not
change `reserveAggregateSend`'s production ABI merely to return diagnostics. A
single fixed pending-decision slot in the tagged session counter is enough for
the intended sequential workload: a different stream or second outstanding
decision marks `ambiguous=true` and invalidates the diagnostic rather than
allocating a map or merging durations. The ordinary disabled helpers and
handle type remain zero-sized and must compile away completely.

Shutdown and timeout intervals are retained under their natural outcomes.
They are never normalized to a notification or success. The workload accepts
only zero shutdown/timeout outcomes, zero ambiguity, and zero active waits.

## DATA queue and completion measurement

`waitForSendErrCommitOn` has two distinct intervals
(`third_party/yamux/session.go:484-535`):

1. **enqueue wait:** entry at the first select until `queue <- ready` succeeds,
   or until shutdown/timeout wins;
2. **post-enqueue completion wait:** successful enqueue until `errCh`, shutdown,
   or timeout wins. This includes queue residence, send-goroutine scheduling,
   body gathering, the physical `conn.Write`, and error publication. It must not
   be labeled physical-write time.

For DATA only (`body != nil`), record count/total/max and exact outcome for both
intervals. The send loop may additionally time its existing physical
`s.conn.Write(frame)` call (`third_party/yamux/session.go:636-680`) and record
count/bytes/total/max with success/error/short-write outcome. This yields a
proper containment relationship: physical-write time is one component of
post-enqueue completion, not a synonym for it. Header-only control, ping,
growth, and refund frames remain excluded, so background control cannot pollute
DATA timing.

Timestamping and scalar accounting occur only in tagged builds. The physical
send-loop hook identifies DATA before `ready.Body` is detached; it retains no
reference to the body and performs no per-frame allocation.

## Drain boundary and identity contract

Keep the existing destructive, post-transfer per-session drain. Add:

* observed stream ID, first/last diagnostic token, and `multiple_streams`;
* active credit-wait and active DATA-send counts;
* ambiguity/overflow flags;
* the credit-wait and DATA timing conservations above;
* existing session identity, lane, local/remote tuple, reserved frame/payload/
  charged-byte totals, zero-credit observation counts, and capture time.

A drain with any active wait/send is incomplete and must return that condition
without claiming a closed interval or resetting live state. Pre-transfer drain
must be complete and empty. Post-transfer drain must be complete, have one
stream ID, no ambiguity/overflow, exact expected payload bytes, natural
success outcomes, and zero active state. Any concurrent background DATA on the
same bulk session makes `multiple_streams` or payload conservation fail. Control
traffic is ignored by construction. There is no in-transfer polling.

## Bounded controls before any WAN use

Use existing adapter/build infrastructure and deterministic, non-WAN Yamux
fixtures. Independently force: byte credit zero; stream allocation zero with
session credit; session allocation zero with stream credit; both allocation
credits zero; enqueue blocking; and a blocked fake physical writer. Release
each with an explicit barrier and require the exact entry cause, selected wake
outcome, positive duration, conservation, exact DATA bytes, natural goroutine
joins, and zero active state. Separate shutdown and timeout controls require
those outcomes to remain visible and rejected. A concurrent second-stream
control must set ambiguity/multiple-stream invalidation without allocating or
deadlocking.

Then build the untagged predecessor and proposed source with the pinned Go
toolchain and compare struct sizes plus disassembly of the affected functions.
Any ordinary layout, instruction, stack-size, or escape/allocation difference
rejects the observer before endpoint testing.

## Decision rule

For a predeclared selected transfer, seconds in credit-wait buckets support
sender credit starvation at the named gate. Small credit waits with large
post-enqueue time require the physical-write subtotal to distinguish queue/
scheduler delay from the underlying connection write. Small values in both
leave a measurement gap. These observations do not authorize a growth, refund,
queue, DATA-emission, TCP, allocation, or timeout policy change.

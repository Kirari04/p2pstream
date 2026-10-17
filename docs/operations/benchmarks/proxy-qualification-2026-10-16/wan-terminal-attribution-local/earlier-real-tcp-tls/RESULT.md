# Local discriminator result

Both predeclared modes passed once: normal and race, each with exactly 15 RUN/PASS names (the parent test, two schedules, twelve negative predicate controls). Both exited naturally with status 0; owned process groups were absent afterward. No timeout or forced cleanup path was needed. Source and imported production Yamux hashes were identical before/after each run. Source remains at `cancellation_test.go` SHA-256 `91d92e5c6efbdf48c69bae406899532711a5ae0a6add6887b61528f71f2100e1`.

The real loopback TCP/TLS/aggregate-Yamux cancellation schedule recorded:

- Exact outgoing GoAway(normal) plaintext, 12 bytes, one plaintext Write and one raw Write.
- Locally owned stop intent event 3, physical Close enter/complete events 4/5, real TCP Write return event 6 with 0/34 bytes and errors.Is(net.ErrClosed), actual post-close RawConn.Control event 7 with errors.Is(net.ErrClosed) and no callback invocation, plaintext return event 8, joined Session.Close event 9.
- No active raw/plaintext work, ambiguity, changed owner, missing join, or remaining fixture worker. The proposed classifier accepted this specific local cancellation while retaining its raw error.

The completed-control schedule recorded successful 34/34 real raw bytes at event 3, exact peer plaintext receipt, then stop event 4, physical close events 5/6, unavailable real after-capture event 7, plaintext return event 8, and joined event 9. It was correctly not classified as an errored cancellation. Both modes produced the same two event orders.

The twelve negative controls rejected DATA/unknown classification, absent or late intent, non-local error, positive partial write, nil-error short write, changed generation, absent join, post-retirement activity, overlap, active write, and invalid capture ordering. These are predicate checks using the real positive record, not reproductions of all those socket failures.

This establishes that a narrow, typed terminal-control cancellation can be distinguished locally using real TCP and TLS and an actual closed RawConn provider. It fills the prior net.Pipe/synthetic-provider limitation for these forced schedules. The barrier deliberately pauses before the TCP syscall; it is not evidence of a kernel-blocked write. The exact plaintext is chosen GoAway, not an attribution of tiny2's 34-byte ciphertext. The one-owner fixture is not evidence of complete five-owner stop coverage in the WAN runner.

No production defect, shutdown-policy change, throughput fix, original-WAN causal claim, or gate clearance follows. Tiny1/tiny2 remain rejected; the WAN observer/runner/validator are unchanged. No new observer infrastructure or WAN traffic was started. The next implementation, if authorized, is the prospective narrow observer contract described in `REVIEW.md`; it still needs actual complete owner coverage and provenance rather than guessing from raw size or `outside` counters.

`normal.result.json` and `race.result.json` retain exact commands, PID/group closure, timestamps, and source hashes. Their stdout/stderr files preserve the single executions. `RESULT.json` independently checks the exact expected names and summarizes the records. New artifacts are small loose files; no archive or source-tree copy was created.

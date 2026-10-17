# Adapter source checkpoint 10

Checkpoint 10 preserves checkpoint 9 and corrects one stale helper assertion
found by audited build attempt 3. The actual authenticated terminal HTTP test
and sticky terminal failure test passed. Only the helper-level replay expectation
changed: after a terminal transition has been consumed, `drainPhase` returns its
fail-closed default phase `pre` with `valid=false`, while the actual HTTP replay
continues to return 409 before a second destructive snapshot.

Runtime code, terminal semantics, the nine-request HTTP fixture, session cleanup,
Python validators, timed transfer code, and protocol source are unchanged. No Go
test, build, endpoint, or traffic command ran from this checkpoint.

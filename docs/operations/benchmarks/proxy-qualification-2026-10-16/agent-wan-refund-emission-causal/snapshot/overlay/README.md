Aggregate refund physical-emission causal diagnostic
====================================================

Purpose
-------
This diagnostic compares physical emission of validated aggregate refund control
batches. It does not change the protocol, credit selection, ownership, queueing,
commit timing, DATA emission, retry policy, or resource accounting.

Common source
-------------
Both arms start from p2pstream commit:
  5eeccaaa797254d7bde4db9854a337a02c3bc28e

Both apply the exact common scaffold recorded in meta/common-scaffold.patch.
The scaffold retains the existing sendReady layout. It validates aggregate
refund batches as one to seventeen ordinary 12-byte protocol headers, with
sorted allocation-window updates followed by the global window update. Both
arms run the same classifier in the send loop.

Arms
----
refund-gathered-control:
  diagnosticSplitAggregateRefundWrites = false
  A validated aggregate refund batch uses the existing single physical Write.

refund-split-control:
  diagnosticSplitAggregateRefundWrites = true
  A validated aggregate refund batch containing two to seventeen headers uses
  one physical Write per 12-byte header. Singleton batches remain one Write.

The exact arm difference is meta/split-arm-toggle.patch. No field is added to
sendReady. Both arms preserve one bounded priority item and one joint Commit
before the first physical byte. Any short write or error stops immediately,
reports once, and is never retried. DATA remains gathered into one Write.
Unrelated and malformed concatenated controls remain one Write.

Scope and interpretation
------------------------
This is a diagnostic-only causal arm, not a production candidate. The earlier
historical/current runtime bisection had exact payload/event conservation and a
consistent phase-shape difference, but failed the predeclared pair-two download
gate. It prioritizes this experiment and is not causal proof or a performance
acceptance result.

Validation
----------
Each arm passed:
  focused normal tests, count=10
  focused race tests, count=5
  full third_party/yamux normal suite, count=1

The focused tests cover singleton, paired and maximum 17-header batches; exact
wire bytes and order; ordinary controls and DATA remaining unchanged; injected
short writes and errors without retries; selected timeout ownership; queued
timeout plus Close; and existing terminal/global/conservation behavior.

All endpoint binaries use ordinary tag-off builds. driver-origin.test is copied
byte-for-byte from refund-gathered-control-server.test.

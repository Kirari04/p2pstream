# Attempt 3 proof-scope supplement

This supplement preserves `REPORT.md` (SHA-256 `ff7c2d72a73009259c66c778ae39e5fb5c760f732f67893cf277d81839517547`) and `ATTEMPT-SHA256SUMS.txt` (SHA-256 `0c6d8c1198a4b1d53c5726b702dd209fed7aa6fd72a4c1b03dac81a3eff02594`) unchanged.

Two limitations in the attempt-3 proof tooling are explicit:

1. `ordinary-proof.json` reports the escape-decision difference, but its programmatic `accepted` expression uses machine-code equality and frame/symbol equality only. It does not include escape-decision equality. This does not alter attempt 3's result because machine-code equality was already false and the report independently rejects the changed `Stream.write` inliner cost. A later validation must include per-function escape/classification equality in the mandatory acceptance predicate.
2. `TestQualificationAggregateSendCountersDoNotChangeOrdinaryLayout` proves the qualification counter type has zero ordinary size and checks the existing `aggregateWindow` qualification/protocol-field offset relationship. It does not compare complete baseline and candidate layouts for `Stream`, `Session`, `aggregateWindow`, and `sendReady`. Attempt 3 therefore does not claim full four-type layout identity. A later validation needs an identical test-only baseline/candidate probe (or equivalent DWARF inspection) for each type's size, alignment, and every field offset.

No attempt-3 code, test, proof output, status, or conclusion is changed by this supplement.

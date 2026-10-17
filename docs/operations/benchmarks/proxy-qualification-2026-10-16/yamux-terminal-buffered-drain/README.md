# Terminal buffered-drain correction

The normal Yamux suite in [CI 38909137527](https://github.com/Kirari04/p2pstream/actions/runs/38909137527)
failed `TestHalfCloseSessionShutdown`: a reverse TCP credit write reset interrupted
a read of already accepted payload. The affected runtime and test files were
byte-identical to the preceding green `248bad0` checkpoint. Repeating that TLS
test locally did not reproduce the CI failure; the passing repetitions do not
invalidate the observed failure.

The deterministic baseline independently exposes the contract discrepancy.
Three of four initial cases fail: credit publication interrupts a buffered read
after FIN, FIN observed while a credit write fails still returns that error,
and later reads after a pre-FIN error publish obsolete credit. The live-stream
error control passes. Original failure output and exact baseline sources remain
under `ci-failure/` and `baseline/`.

## Change and limits

`Stream.sendWindowUpdateMode` now skips per-stream credit calculation and
publication once peer FIN or terminal session state is visible. A buffered
`Read` that encounters a credit-write error rechecks terminal state before
returning that error. Aggregate storage and session-wide credit refunds still
occur before this per-stream check. Live streams, local half-close, and reset
semantics retain their existing error behavior.

The original TLS test now uses `io.ReadFull`, checks every payload byte and then
requires EOF. New deterministic tests also cover delayed FIN/error ordering,
pre-FIN error propagation, established and local-half-close controls, and actual
aggregate receive storage with a physical session-only refund.

The first full local suite failed an older live-credit fixture across its five
connection transports: its writer sent FIN before the credit assertion. The
corrected fixture keeps that writer live through the **unchanged exact credit
assertion**, then closes and joins it. Its wait is bounded rather than an
unconditional two-second sleep. The initial failure and both mechanical fixture
corrections remain in `validation/`.

This fix qualifies buffered drain after observed FIN or terminal session state.
A credit-write failure before FIN can still return bytes with an error. It does
not establish graceful draining for every physical-failure ordering, identify
the original WAN slowdown, or change its acceptance gates.

## Validation

All commands use pinned Go 1.26.6, `GOTOOLCHAIN=local`, `GOWORK=off`, empty
`GOFLAGS`, and all four temporary-directory variables on owned ext4 storage.
Each test phase has an outer process-group timeout and a Go test timeout.

| Check | Result |
| --- | --- |
| Five new top-level regressions | Each passes normal ×50 and race ×20 |
| Original TLS shutdown test | Normal ×100 and race ×20 pass |
| Corrected live-credit fixture, five transports | Normal ×5 and race ×5 pass |
| Full Yamux normal suite | 129 top-level tests pass; no skips |
| Full Yamux race-short suite | 127 top-level tests pass; two deliberate stress-test skips |

No race report occurs. Counts above are top-level functions; nested subtests
are not added to those counts. The deliberately skipped tests are
`TestSendData_Large` and `TestSendData_VeryLarge`. Task children are joined;
the two pre-existing user development processes remain untouched.

`root-review.json` independently verifies all 55 validation-manifest entries,
final source identities, actual named pass counts, complete-suite results and
the retained failures. The final three-file diff SHA-256 is
`ca526d02f1baca179663ac634863cd2e20cf7d973788248efb811977008dd2bd`.
The original validation-manifest SHA-256 is
`dfff903ff090ac0a7b1a1af8b825e65aef7694c75269af579b20ac9b8e26dd0b`.

The four directories preserve 90 original files byte-for-byte. Go source copies
have a `.go.txt` suffix to avoid creating test packages in the documentation
tree. Their original paths and hashes appear in the root review; nested source
indexes retain their original paths. `SHA256SUMS` binds the files actually
exported here. This new module check supersedes the old byte-identity carry for
Yamux; final agent/tunnel/server integration races and current-head CI remain
separate requirements. Frozen native-Go and HTTP-stage diagnostic binaries
retain their earlier runtime provenance.

## Live growth-publication contract after terminal-drain correction

CI on `4477c0b` exposed a second stale fixture in `internal/tunnel`: it closed the session before expecting a window-update write error. Terminal sessions now intentionally skip obsolete per-stream credit. The corrected test gates only a positive WINDOW_UPDATE for the accepted live stream, passing unrelated RTT probes through. It verifies the exact receive-storage reservation while publication blocks and after the injected failure, then verifies idempotent release. No runtime behavior changes. Focused normal ×100/race ×50, complete tunnel normal/race and complete Yamux normal/race suites all pass on Go 1.26.6. `live-growth-publication/` preserves the original CI failure, test-only patch, exact commands, raw logs and source hashes. Full remote CI is still a separate check.

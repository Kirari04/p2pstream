# Tunnel CI contract correction

Source checkpoint: `4477c0b607f197ff3e558858903fdfebe4a0aa3e`

Failing CI run: `https://github.com/Kirari04/p2pstream/actions/runs/38918084533`

Failing CI log: `tmp/ci-4477c0b-failed.log`

Failing CI log SHA-256: `0f141670cbf76befc6398731deaa3f103d87312e981dbdb5973196ae36b76905`

## Finding

`TestGrowingConnRetainsReservationWhenWindowUpdateFails` explicitly closed the
server session before its measured growth attempt. Commit `4477c0b` makes a
per-stream credit update a successful no-op once peer FIN or terminal session
state is observable, because that sender can no longer consume per-stream
credit. The test's closed-session error expectation was therefore obsolete.
The reservation-retention invariant remains required for a live publication
whose physical write fails or is ambiguous.

## Test-only correction

Only `internal/tunnel/growing_conn_test.go` changed. The existing
`gatedWriteFailureConn` gained an optional frame predicate; a nil predicate
retains the prior fail-all behavior used by the aggregate publication test.

The corrected legacy growth test:

1. establishes a live client/server stream and lets the constructor's initial
   RTT probe complete;
2. enables a predicate that gates only a 12-byte Yamux `WINDOW_UPDATE` header
   for the accepted stream with a positive delta, so an unrelated Ping cannot
   consume the gate;
3. drives deterministic measured growth from 512 KiB to 1 MiB;
4. proves the server remains live and the exact receive-memory delta is already
   reserved while the physical write is blocked;
5. injects the write failure and proves the exact reservation remains retained;
6. invokes `Release` twice and proves cleanup returns the reservation exactly
   once.

The writer gate uses a deferred unblock so any test failure releases the
physical writer before Yamux cleanup joins its send loop.

No production file or runtime behavior changed.

## Validation

All commands used the absolute Go 1.26.6 executable, `GOTOOLCHAIN=local`, and
CPU affinity `4-15`. Exact commands are in `commands.txt`; raw output is in the
named log files.

- Toolchain: `go version go1.26.6 linux/amd64` (`toolchain.log`), exit 0.
- Focused normal, 100 repetitions: PASS in 0.135s (`focused-normal.log`).
- Focused race, 50 repetitions: PASS in 1.098s (`focused-race.log`).
- Complete `internal/tunnel`: PASS in 0.074s (`internal-tunnel-full.log`).
- Complete nested Yamux module: PASS in 21.820s (`yamux-full.log`).
- Complete `internal/tunnel` race: PASS in 1.242s (`internal-tunnel-full-race.log`).
- Complete nested Yamux module race: PASS in 84.845s (`yamux-full-race.log`).
- `git diff --check`: PASS.

No test or race errors remain in the owned scope.

## Artifact hashes

See `sha256sums.txt`. Key hashes:

- Corrected test source: `9591accdc3c7d58631ea84e360ab0f52d0830adfa5d22e4af696b239fc4dcf81`
- Test-only patch: `2d175d5d99101e97c223023df40d128c5d1caf0a7581ca1616b8eedc3b16f41b`
- Focused normal log: `d86772961103e6e4c74c852e6d4a1a0d497bc2e4c10a6dca6c051f53bbca6763`
- Focused race log: `b803b1544ea6b0997efc71299871bbccf4c99d06397e7255dc9ce3da48acec37`
- Full Yamux race log: `9183982c934ef36d4df5adf2189fe1087c6abca80a2362f67633787f7571ced5`

## Remaining risk

The tunnel package cannot access Yamux's unexported header constants, so the
test predicate decodes the stable wire header shape directly: length 12,
message type byte 1, stream ID at bytes 4-7, and positive delta at bytes 8-11.
Yamux's own constant/header tests cover that protocol contract. If the vendored
wire format changes, this predicate must change with it; failure is closed and
obvious because the test times out waiting for the intended frame.

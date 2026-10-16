# Matched verified H1/H2 WAN diagnostic

This package records a matched public HTTPS HTTP/1.1 and HTTP/2 diagnostic on
source `8ac4c8df8237e37777ba2fac0da6f949da17bdea`. Both arms use the same current server and
aggregate agent binaries, verified certificate, RootCAs,
`ServerName=public.test`, independently bound leaf DER fingerprint, topology,
schedule, payload, shaping, and lifecycle controls. The public protocol is the
only intended arm difference, and every response records the observed HTTP
major version and negotiated ALPN. The build manifest records `go1.26.6 linux/amd64`.

The complete 32-row sustained diagnostic **failed** its own acceptance
contract. It contains 16 exact rows per arm, with 256 MiB download payloads,
HTTP 200 responses, verified TLS/protocol observations, exact event
conservation, trace correlation, and natural joined cleanup.

Endpoint and namespace processes exited naturally and were joined before the affected arm runner deliberately returned 1 for its independent download-floor failure (HTTP/2: retained-warmup, steady). The nonzero status is a diagnostic rejection, not forced cleanup.

| Arm | Diagnostic result | Warmup minimum download | Retained-warmup minimum | Steady minimum | Expected reuse |
| --- | --- | ---: | ---: | ---: | --- |
| HTTP/1.1 | pass | 33.592 MB/s | 29.729 MB/s | 30.034 MB/s | pass |
| HTTP/2 | fail | 33.586 MB/s | 14.242 MB/s | 27.327 MB/s | pass |

For each arm, sequences 9-12 are the first visits to four distinct lane keys
after the explicit warmup closes. Sequences 13-16 are the corresponding
retained same-key revisits. Only sequences 13-16 can satisfy the sustained
reuse observations; a repeated physical lane address in sequences 9-12 is not
itself retained origin-connection reuse.

This is a protocol diagnostic, not the original release qualification. It does
not clear or weaken the current-production original-H2 WAN blocker. That
separate 96-row qualification failed its download gate in pairs 1 and 3 at
0.8109937458 and 0.8110017765; pair 2 was 1.0197867707. No causal conclusion
about protocol, source commit `4477c0b`, or the production regression follows
from this diagnostic alone. HAProxy, pressure, capacity, integration, release,
and deployment gates remain separate.

## Preserved checkpoint history

- The initial focused normal tests failed twice while the diagnostic fixtures
  were corrected. The third normal run and the focused race run passed. The
  two superseded helper snapshots were reconstructed by reversing only the
  documented fixture edits; both exactly match their recorded pre-test SHA-256
  values. The reconstruction program and provenance note are retained beside
  the original failed status/output logs. No test was rerun during that
  reconstruction. The corrected source and passing checkpoints are also
  retained and hash-bound.
- Tiny smoke 1 failed before workload traffic because relative binary paths
  became invalid after the runner changed directory. Both arms still recorded
  natural joined cleanup. The original failure and the path-fix overlay are
  retained.
- Tiny smoke 2 produced 24 exact verified responses and natural cleanup, but
  its original classifier reported `inconclusive-possible-idle-expiry` and
  exited nonzero. The original output remains unchanged. The separate offline
  audit shows that warmup sequences 1-8 explicitly closed idle transports and
  sequences 9-12 were first visits to four post-close lane keys, so there was
  no retained same-key reuse opportunity in that schedule. This explains the
  classifier result; it does not rewrite it into a passing attempt.
- Tiny smoke 3 added sequence 13. Both arms completed 13 exact rows, and the
  sequence-13 trace records same-key, same-origin connection reuse after the
  retained sequence-9 PutIdleConn, followed by natural cleanup. The smoke only
  validates the corrected observation contract; it is not sustained evidence.

## Evidence boundaries

`export-map.json` maps every copied file to its immutable source path and hash.
`SHA256SUMS` binds this tracked package. The source overlays, generated runner,
wrapper, validator, original failed attempts, raw result and lifecycle rows,
summaries, attestations, status chains, contracts, and pre/post hashes are
included. Binaries, source archives, build trees, private keys, certificates,
databases, readiness files, credentials, per-request scratch files, and raw
endpoint logs are excluded.

The diagnostic patches are replayable against the recorded source checkpoint;
the tracked package does not include the binaries or source tree. Any future
claim must remain bound to the exact hashes and raw artifacts in this package.
`build/source-diff.txt`, `build/production-byte-identity.diff`, its status, and
the independent build manifest retain the source report separately from the
traffic outcome.

`review/HTTP-STAGE-SETUP-COMPARISON.md` is a source-only comparison with the
earlier native HTTP integration diagnostic. Its integration configuration and
relay shape match relevant parts of this setup, but its underlying Yamux
`stream.go` predates the `4477c0b` terminal-drain runtime and it omits the live
adaptive owners and management carrier. It is therefore not a same-source
causal control and adds no performance claim.

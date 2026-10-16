# Current-production original H2 WAN qualification

This package records the original public HTTPS/H2 aggregate-versus-legacy WAN
workload against current production source `8ac4c8df8237e37777ba2fac0da6f949da17bdea`. The current
server and aggregate agent use that source; the compatible legacy agent uses
`3ec137c3ad3d07028e867042fb00d3933912d7ab` plus the previously preserved qualification overlay.
The current source includes the `4477c0b` terminal-drain fix; this comparison
does not attribute its outcome causally to that change.
All three binaries were built with pinned Go 1.26.6. Their recorded hashes are
preserved in `provenance/build-manifest.json`; binaries and source archives are
intentionally excluded from this tracked package.

The fresh three-pair cohort repeats the current-production WAN failure and does not clear the original gate. Failed every-pair checks: pair 1 download 81.10%, pair 3 download 81.10%.

| Pair | Order | Download ratio | Upload ratio |
| ---: | --- | ---: | ---: |
| 1 | legacy, aggregate-v1 | 81.10% | 98.57% |
| 2 | aggregate-v1, legacy | 101.98% | 99.73% |
| 3 | legacy, aggregate-v1 | 81.10% | 99.51% |

The stability cohort is exactly three fresh alternating endpoint pairs and 96
transfers. Each pair independently requires aggregate/legacy median throughput
of at least 90% in both directions. All rows must be HTTP/2 200 responses with
exact payload hashes, exact persisted-event conservation, required receive-pool
growth, and natural joined endpoint and namespace exits. The exporter verifies
the attested raw-result and lifecycle hashes before copying them.

The mature runner's pooled result also fails download at
`81.67%` and passes upload at
`99.03%`. It exits before writing its canonical
summary when that pooled download gate fails; `stability/offline-analysis.json`
recomputes the pooled and every-pair values from the immutable raw rows and
records the canonical stderr. Runner, validator, wrapper, launch shell and tool
envelope all consistently report exit 1. This is an expected qualification
failure, not a lifecycle or tool-envelope discrepancy.

The earlier 32-transfer single pair remains separate and is not pooled into the
stability cohort. It recorded download ratio
`1.000423586` and upload ratio
`0.993876129`, with exact HTTP/2 200, payload,
event, and lifecycle checks. One aggregate timed download was
28.865994 MB/s. A single pair cannot clear the three-pair gate or supersede the
prior repeated failure.

The initial pair's outer tool call reported exit 1 even though the persisted
shell supervisor and runner/validator recorded status 0, and all seven endpoint,
origin and namespace-helper processes exited naturally with status 0.
`single-pair/tool-envelope-discrepancy.md`
retains the discrepancy without changing the raw outcome. The validator
contract's failed attempts 1 and 2 are also retained alongside the corrected
passing contract run. The exact generator versions for attempts 1 and 2 were
superseded before preservation, so their status and traceback logs document the
failed attempts but do not make those two attempts independently replayable.
The final contract generator and all of its fixed synthetic inputs and outputs
are preserved for review and replay of the corrected contract set.

## TLS scope

This is a faithful reproduction of the historical load setup. The client uses
`InsecureSkipVerify` with an IP URL and no SNI. `req.Host=public.test` is an HTTP
Host value, not TLS SNI. The cohort therefore does not establish matched,
verified certificate handshakes. A future verified TLS diagnostic must apply
RootCAs, `ServerName=public.test`, observed ALPN, and an independently bound
certificate DER hash symmetrically to all compared arms.
A matched verified H1/H2 diagnostic is separate and is not part of these
qualification results.

## Evidence boundaries

`export-map.json` maps each copied file to its original immutable path and hash.
`SHA256SUMS` binds the exported package. The explicit exporter allowlist excludes
binaries, source archives, private certificate keys, certificates, databases,
readiness files, raw endpoint logs, and raw sensitive endpoint state. The
runner is only a lifecycle/validation derivative of the mature
`scripts/qualify-proxy-agent-growth.sh`; timed request semantics and current
production source are unchanged.

Rejected SQL64 and blocked public HTTP-stage source experiments remain rejected
and are not inputs to this result. Pressure, cancellation, capacity, integration,
HAProxy, verified-TLS, release, and deployment qualifications remain separate.
This result does not support marking the draft pull request ready, merging it,
promoting a release, deploying it, or claiming broad modern-proxy parity.

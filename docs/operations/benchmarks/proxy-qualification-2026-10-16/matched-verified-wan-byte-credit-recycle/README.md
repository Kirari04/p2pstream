# Matched verified WAN byte-credit recycle diagnostic

This package preserves the source, failed attempts, focused checks, audited
builds, runner provenance, and matched H1/H2 evidence for the byte-credit
recycle diagnostic. It extends the frozen below-TLS/server-growth package by
hash; it does not revise that package or clear the original WAN blocker.

## Result

The 32-transfer sustained cohort is complete and naturally closed. Payload,
TLS, requested protocol, trace conservation, endpoint ownership, resources,
sender terminal closure, raw terminal closure, and all eight selected interval
validators pass. Both arms remain rejected and are classified
`inconclusive-upstream-reuse`:

- H1 passes the retained-warmup download floor at 29.911232 MB/s but fails the
  steady floor at a 22.408983 MB/s minimum. Sequences 15 and 16 start after the
  fixed 30-second origin-idle limit and do not retain the required origin.
- H2 fails the retained-warmup floor at 27.455443 MB/s and the steady floor at
  an 18.800107 MB/s minimum. Sequences 13 and 14 do not retain the required
  origin.

The reuse transition is consistent with the configured 30-second expiry: all
four revisits at 25.089–27.628 seconds retain the origin, while all four at
30.916–31.252 seconds create a new origin. The prior-cohort comparison shows
that the four failed lanes were already at 30.834–31.171 seconds at request
start; longer intervening transfer walls explain the expiry timeline, not the
transfer slowdown.

The independent trace analysis joins 645 receiver records to 645 peer records
and validates 138 complete byte-zero waits without drops, ambiguity, overflow,
terminal waits, or retry at zero credit after an applied advance. In the two
conditioning-clean comparisons, H2 exceeds H1 byte-zero wait by 714.781 ms at
sequence 9 and 873.756 ms at sequence 11. The excess is concentrated in ranges
containing normal-read credit. Individual receiver eligibility-to-completion,
peer handler-to-notify, and wake-to-retry observations are microsecond-scale;
receiver totals across each selected interval are about 2–6 ms.

The original trace-analysis report is preserved byte-for-byte. Its table mixes
units in one cell: the first value is a total in milliseconds and the second is
a maximum in microseconds. The separately hashed append-only erratum states
that unit correction without changing any result.

Those observations exclude the measured post-eligibility local sections and a
growth-only explanation for the clean H2 excess. They do not measure the time
before receiver eligibility or permit cross-process clock subtraction. They do
not establish whether consumption cadence, send scheduling, TLS/socket transit,
network transit, receive, or dispatch causes the remaining delay. No production
runtime fix, flow-control policy change, 30-second timeout change, causal claim,
modern parity claim, or 90% WAN gate clearance follows.

The bounded source/spacing review finds a real feedback path: the serial proxy
copy loop returns receive storage while reading, and the normal byte-credit
update is considered once accumulated credit reaches one quarter of the current
receive window. Time between reads can therefore delay the next eligibility
threshold. The retained per-write maxima stay below 0.577 ms, while the traced
local update/send/wake sections stay in microseconds. This establishes neither a
correctness defect nor that the threshold causes the transfer slowdown.
Its append-only numerical erratum distinguishes the 264.0 µs clean-row
receiver maximum from the 315.1 µs maximum across all eight selected rows; the
original review remains unchanged.

The frozen cadence supplement compares adjacent normal-update labels only
within each owning process. On the two clean sequences, the H2-minus-H1 cadence
change at receiver completion closely matches the change at peer handler entry;
it shows no material post-publication cadence expansion under this coarse
wall-label comparison. It places the remaining increase before eligibility but
cannot split that time between inbound DATA availability and public-consumer or
HTTP/2 backpressure. It is not a one-way transit measurement or a causal bound.

## Earlier attempts

Tiny smoke 1 completes 16 H1 rows and four selected joins, then fails its first
terminal agent request with HTTP 409; the ninth snapshot and raw closure are
missing, so H2 is withheld. Tiny smoke 2 corrects that fixture and accepts both
16-row arms, all eight selected joins, zero-drop growth capture, raw temporal
conservation, natural ownership, and both five-session terminal closures. It is
a 4 MiB observational check, not a throughput result.

The strict ordinary proof remains rejected on one compiler-generated symbol
name. The separate accepted supplement proves the narrower exact code-and-
referent equivalence. Both are retained. Audited build attempt 4 passes its
checks but is permanently provenance-invalid because frozen checkpoint metadata
was briefly rewritten during execution. Its binaries and receipt are never
reused; the incident bytes and accepted attempt 5 are both retained.

## Package layout and verification

`PACKAGE-MAP.tsv` is the one-to-one source map. Repeated source, contract, and
proof detail is in `ancillary-byte-credit-recycle-evidence.tar.gz`; primary run
rows, attestations, lifecycle/status records, public test certificates, and
review reports remain readable as loose files. `EXCLUDED-RUN-ARTIFACTS.tsv`
binds every omitted closed-run artifact to the immutable run ledger.
`HASH-ONLY-BINARIES.tsv` records excluded binary hashes.
`ACCEPTED-BINDINGS.tsv` provides the principal accepted build, runner, tiny and
sustained spine; the package map and embedded manifests remain the exhaustive
artifact index.

Run:

```bash
python3 verify-evidence-package.py --package-root .
```

The verifier checks the root manifest, archive hash and deterministic metadata,
every mapped archive and loose entry, and the one-to-one allowlist/map relation.

## Security and scope

Private keys, recoverable authentication values, readiness/configuration files,
endpoint logs/state, binaries, caches, full build/source trees, and runtime
directories are excluded. The nonprinting exact-value scan found no recoverable
secret in the selected files. A process-local trace bearer that was never
persisted is unavailable for exact comparison; this limitation is explicit in
`SECRET-SCAN.json`.

This diagnostic is separate from release qualification. The original WAN,
HAProxy, pressure, capacity, broader integration, release, and merge gates
remain open.

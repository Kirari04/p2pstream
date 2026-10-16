# Matched verified WAN copy-path attribution

This evidence package records a bounded diagnostic on source
`8ac4c8df8237e37777ba2fac0da6f949da17bdea`. It does not clear the original
current-production H2 WAN release blocker, establish protocol or runtime
causality, qualify a retained fix, or support HAProxy parity, readiness, merge,
release or deployment.

## Frozen source and runner checkpoint

The replayable source overlay is `4da0889228f80407806ac111187366ab06abd0ffd5d130d08d41407a8a02ff5f`.
The initial historical build retained status and empty logs but no exact build
command; no command is inferred or backdated. A later audited compile used the
pinned Go executable `29e6e0b8be61beb1489ceae62b304343566de8a1dc700af74bde7aeb9c80ad45`
and produced server binary hash
`59eb8af1e6c767776f639b014074d542abb23d4ad019c7558182670372ea92a7`,
byte-identical to the preserved historical output. It did not rerun tests or
traffic. The pre-build `go env` record was captured before changing to the
compile directory; the preserved manifest explains that discrepancy and binds
the later inspection from the actual build directory.

The superseded attempt-3 runner/validator checkpoint and the repaired final
attempt-5 checkpoint remain separate. All source-test failures and the failed
runner-analyzer contract remain intact. Earlier validator/static contract logs
are retained as superseded checkpoints; the immutable manifests bind the exact
attempt-3 and attempt-5 fixture artifacts.

## Measurement limits

Read duration, recorder-write duration and inter-read gaps are separate.
Inter-read gaps overlap downstream writes and copy-loop bookkeeping and cannot
be added to read/write totals. `agent_tunnels_initial` is a shallow outer copy,
so its nested lane values may reflect later high-water mutations. High-water
fields are componentwise maxima sampled over time, not one coherent transient
state and not minima. They cannot establish starting credit or prove the cause
of a stall. Final pool state supports cleanup and conservation conclusions only.

## Failed smoke history

Smoke 1 failed in the parent shell before the wrapper executed because the
runs directory was absent. Its command, status, error and explicit
`traffic_started:false` record remain preserved.

Smoke 2 ran both H1 and H2 arms and remains rejected. Each arm has 13 exact
protocol/TLS/result rows, retained reuse, event and pool conservation, and
natural cleanup. The original strict validator compared monotonic duration
totals with wall-clock `UnixNano` spans; single-call 32-byte POST rows exceeded
those spans by 1–10 ns. Both arms, the wrapper and the original attestations
remain nonzero/`accepted:false`. The independent offline audit identifies the
clock-domain defect without waiving the validator or reclassifying the run.

A corrected monotonic implementation is frozen separately under
`wan-copy-monotonic-source`. Checkpoint `82d93b17...` binds its source, focused
tests, contracts, static integration and audited build. Four preparatory tool
invocations have unexplained outer-envelope status 1 while their saved inner
status is 0; both statuses and the limitation remain preserved.

Corrected monotonic smoke 1 passes both 13-row arms. H1 uses verified
HTTP/1.1; H2 uses verified H2. Both retain matching reuse, complete monotonic
copy conservation and natural cleanup. The worker audit and independent Astra
raw audit agree. This is a bounded smoke result, not sustained qualification,
causal attribution or proof of a runtime fix.

## Sustained result

The single sustained corrected cohort completed 16 exact rows per arm and was
rejected without retry. H1 is observationally complete for reuse and natural
cleanup, but fails the 29.7 MB/s floor at retained-warmup sequence 9
(29.451921 MB/s) and steady sequence 11 (29.673001 MB/s). H2 fails its steady
floor at sequence 14 (14.550217 MB/s). That row reused the exact physical key
and origin connection after 23.982 seconds idle; its 18.305-second handler
interval contains 17.333642 seconds in upstream `Read`, 0.802516 seconds in
downstream `Write`, and a non-additive 0.809880-second inter-read gap.

The slow H2 sequence 14 delayed the intended sequence-15 and sequence-16 lane
revisits until 34.587812 and 35.000628 seconds after their prior PutIdleConn,
beyond the unchanged 30-second idle deadline. Both visits used their expected
lane keys but opened new origin connections with `Reused=false` and
`WasIdle=false`. H2 is therefore `inconclusive-upstream-reuse`. The preserved
schedule map describes the intended order; its separate amendment records the
observed expiry-confounded trace. Intended warmth is not observed reuse.

Both arms preserve exact protocol/TLS/payload/event/copy conservation and five
natural process exits. The arm runners, validators, pair wrapper and outer tool
envelope return status 1 for the diagnostic failures, with no discrepancy.
The worker audit and independent Astra raw audit agree on all 32 rows, reuse,
certificate fingerprint, copy conservation and natural cleanup.

These measurements localize most of the slow H2 sequence-14 response-copy time
inside upstream `Read`. They do not establish why `Read` blocked, identify a
credit, scheduler or Yamux mechanism, prove a runtime fix, or clear the original
90% production WAN gate. A narrower inside-`Read` direction remains pending
architecture review; no rerun is authorized.

## Evidence boundaries

The package includes only explicit, hash-bound source, provenance,
contracts, failed attempts, smoke artifacts and sustained artifacts. Each
traffic cohort's public self-signed `public.test` certificate is included so reviewers can
recompute its DER fingerprint and SAN. The package excludes binaries, complete
source/build trees, caches, all private keys, non-public certificate material,
databases, readiness files, credentials, per-request scratch directories and
raw endpoint logs/state.

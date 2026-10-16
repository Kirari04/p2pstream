# Compact read-wait WAN diagnostic evidence

This package preserves a bounded offline stack-interning diagnostic, its
focused source and contract history, and the completed sustained H1 and
standalone H2 observations.
Production source remains `8ac4c8df8237e37777ba2fac0da6f949da17bdea`.
The prior bounded read-wait package remains immutable at commit
`0895fdd8323708612f2137a63e743b79d6410665`; this package references its six
tiny raw traces and predecessor reports by exact hash rather than duplicating
them.

The stack-intern format preserves parsed trace bytes through a streamed
dictionary representation. It keeps the 64 MiB raw-trace and 512 MiB physical
canonical-file limits and introduces a distinct 4 GiB logical expanded-text
budget. That new budget is an offline diagnostic bound. It is not equivalent to
the rejected 512 MiB full-text path and is not a process RSS bound.

## Focused source and validation history

Source checkpoints 3 through 8, four failed focused attempts, successful
attempt 5, and the separate 14-case adapter lifecycle mock corpus are retained.
The failures remain part of the result:

- attempt 1 fails an exact timezone expansion check; its original driver is
  available only by hash and log, so the attempt is not source-replayable;
- attempt 2 wrongly requires `Wall=` on every Sync line;
- attempt 3 completes all six real tiny comparisons, then exposes an adjacent
  `TransitionStack=`/`Stack=` producer-boundary bug;
- attempt 4 revalidates all six records, then fails a duplicate status-file
  write in the test driver;
- attempt 5 verifies attempt 4's six artifacts by manifest without rerunning
  those parsers/reducers, then passes 17 executed focused contracts.

The adapter lifecycle corpus passes 14 explicit Python fixture-child cases
with reduced test-only deadlines, stderr limit and raw-size limit. It exercises
the frozen adapter's ownership, status, TERM/KILL, join and cleanup paths. It
runs no endpoint, Go build or network traffic and does not substitute for
production-duration lifecycle evidence.

Compact integration checkpoints 1 and 2 and both static attempts are retained.
The second static attempt reports direct and saved status 0 while its exact
tool result reports 1. The first attempt's outer discrepancy is preserved in
its original records. These unexplained tool-envelope differences were not
erased by rerunning the checks.

## Sustained H1 result

The single sustained launch completes all 16 H1 rows and withholds H2 after the
original H1 gate fails. H1 preserves exact TLS, payload, event, copy, reuse,
resource and raw natural-lifecycle evidence. The retained download is
15.781 MB/s against the unchanged 29.7 MB/s diagnostic floor. Warmup downloads
are 33.831–34.100 MB/s; steady downloads are 29.884, 29.980 and
34.361 MB/s. Same-key revisits 13–16 reuse the exact origins with idle ages
25.787–26.142 seconds, below the unchanged 30-second timeout.

All four selected trace captures have adapter, parser and canonicalizer status
0. Their original analyzers return 1 because positive waiting intervals of
12,032, 4,224, 7,168 and 3,392 ns lack stack records. Independent partition
recomputation has zero duration delta. Five endpoint/helper processes and the
owned namespace/resource lifecycle close naturally; the composite attestation's
outer-group wording includes analyzer failure and does not override the raw
cleanup records.

The original sustained run, reductions, status 1 and H2-withheld status remain
unchanged. Large canonical streams and parsed text are referenced by their
manifests and ledgers rather than copied. A separately reviewed 41-entry safety
allowlist approves the four H1 raw traces, totaling 9,079,043 bytes, and the
selected safety source, statuses and reports. The generated process-local trace
bearer was not persisted, so exact comparison against that value remains
impossible; the safety report preserves this limitation.

## Generation-status follow-up

Pinned Go source permits a `Waiting` to `Waiting` generation snapshot with no
new stack. The generation-status derivative carries existing wait metadata only
across that exact same-state empty snapshot. Its four focused in-memory cases
return direct and saved status 0 while the exact outer tool record reports 1.
The original missing `Running` to `Waiting` and old-state mismatch negatives
still reject.

Offline corrected-H1 attempt 1 fails before analyzer execution because it uses
the wrong format-module provenance path. Attempt 2 runs all four existing
canonical inputs with status 0 and preserves every marker, state duration,
transition count, input hash, ledger hash and status hash. It carries
12,032/4,224/7,168/3,392 ns across 16/8/8/7 snapshots. Unknown handler wait
stacks remain unattributed, and every corrected result remains attribution
incomplete. This separate derivative does not rewrite the original rejected
reductions or either failed performance result.

The standalone H2 complement has completed once. Its 16 rows preserve exact
correctness, TLS, copy and reuse checks. Download floors fail at sequences 9,
11 and 14 (27.543945, 27.497200 and 27.571749 MB/s); sequence 15 passes at
34.104193 MB/s and the warmup minimum is 33.5649589 MB/s. Parser,
canonicalizer and adapter statuses are zero, while all four original analyzers
return one for missing `TransitionStack` records. Five inner processes exit
zero and the outer join completes; the composite attestation's
`outer_group=false` is its analyzer-gate wording and does not override those
raw lifecycle records. The separately frozen generation-status correction
returns status 0 for all four reductions and its comparison audit returns 0.
It preserves input identity, markers, state totals and complete handler
partitions while carrying 4,676/5,505/3,776/3,904 ns across valid same-state
snapshots. Every corrected reduction remains attribution incomplete. Across H1
and H2, all eight corrected reductions are structurally accepted while both
original performance arms and all eight original analyzer failures remain
unchanged.

H2 raw traces and large canonical artifacts remain omitted by exact manifest
hash while safe rows, reductions, statuses, lifecycle and source metadata are
preserved. The approved H1 raw traces already provide reproducible compact-
format evidence. No H2 wire decode or raw export-safety scan was performed, so
no H2 wire dump exists.

The H2 full-run manifest is
`1b4d5150817824bbc3649b4287993ecb65286eab75ea0054f8ffa6fcaf90e070`;
the original reduction index is
`55eeff59e816a7a4972638df3ca091915d3f1f784cafef534e7d2ac89b41a250`.
The corrected manifest is
`c0558d3155b07ffc393bf4950605ffbfc6a538ef9ff3986dbf30f9f6b3dab4e5`
and its comparison audit is
`97346a410d866b559997a35311de7a06def3f4cc1449d29934c929cc9917c116`.

The original H2 tool envelope returns 1 consistently with the rejected arm.
The corrected offline tool envelope returns 0. These records are distinct from
the earlier direct/saved-zero versus tool-one focused-contract discrepancies.

## Read-only source diagnosis

A current-source review distinguishes fresh logical-stream credit from warmed
physical-session state and maps byte-window, stream-allocation and
session-allocation gates. The sustained H1 trace places most observed handler
delay in receive-data and `recvLock` waits rather than receiver-side credit
publication. That observation does not distinguish sender credit starvation
from transport pacing and establishes no cause.

The included tagged sender wait-duration design is a proposed source-only
checkpoint. A later work-in-progress implementation is outside this package;
this package includes no build, test, execution, runtime-fix or performance
result for it.

## Limits and qualification status

The primary and generation-status analyzers share the stack-intern decoder.
The independent partition audit applies to H1. The H2 corrected comparison
preserves original inputs, markers and states, but it is neither an independent
decoder nor an independent partition audit. The H1 independent comparator's
first whole-scope transition-count comparison fails and is preserved; its
corrected descriptive comparison and missing-wait context are separate review
evidence.

This diagnostic does not establish a runtime cause or fix. It does not clear
the original current-production WAN failure, the 90% floor, HAProxy parity,
pressure, capacity, integration, release or merge gates.

`PACKAGE-MAP.tsv` assigns every selected source exactly once to a readable
loose artifact or the deterministic ancillary archive. `SHA256SUMS`,
`ANCILLARY-BUNDLE.sha256`, and `verify-evidence-package.py` verify the package.

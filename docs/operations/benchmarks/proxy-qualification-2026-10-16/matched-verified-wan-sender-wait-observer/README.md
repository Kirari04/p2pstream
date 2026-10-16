# Sender wait observer validation evidence

This package preserves the bounded offline source and validation history for a
tagged sender wait-duration observer. It contains no endpoint traffic,
production runtime result, public listener change, performance qualification,
or release result. Production source remains unchanged by this evidence work.

The observer validation starts from source commit
`8ac4c8df8237e37777ba2fac0da6f949da17bdea`. The final frozen observer source
checkpoint is checkpoint 10, whose `SHA256SUMS.txt` is
`606931a1640153a7317eee7e3eeedd2d039d0720841e6bf62aaf22c65215b5a1`.
Validation attempt 5 binds that checkpoint before and after execution and
reports status 0.

## What the successful proof establishes

Attempt 5 passes the bounded offline proof for the tagged and ordinary source
split. The proof covers 19 exact normal tests, 19 exact race tests, two agent
schema tests, two server schema tests, one layout test, and nine persistent
extractor fixtures. Seven deliberately invalid extractor fixtures each return
status 1. The ordinary proof accepts six gates: code, compiler data, compiler
decisions, escape analysis, type layout, and symbol cardinality and size.

The proof compares nine selected functions, their machine instructions,
referenced stack/compiler metadata, five parent-method escape records, and four
type layouts. Attempt 5 reuses only the successful attempt-4 baseline layout
output. Its raw JSONL hash, original direct status, source archive, commit,
tree, overlays, body bindings, and probe are checked before the output is
copied. Candidate work and the remaining proof phases run in fresh roots.

This establishes the selected offline source/compiler equivalence claims. It
does not establish that the observer integration, authenticated control path,
runner, supervisor, server build, or WAN workload works. Those are separate
work with separate evidence.

## Preserved failed attempts

Every failed validation attempt remains part of the package:

- Attempt 1 materializes empty baseline roots because `git archive` is invoked
  from an ignored prefixed directory. The overlay alone cannot compile. The
  first comparison also uses a wrong relative path; its corrected read-only
  comparison is preserved separately.
- Attempt 2 reaches a real compile and fails because three typed `uint32`
  constants are placed in an `[]int` literal. No test runs.
- Attempt 3 passes the focused normal, race, schema, layout, build, export and
  compiler phases. Its first strict comparator fails to consume blank objdump
  separators. The corrected comparator then finds five real instruction
  differences in `(*Session).sendLoop` and a `(*Stream).write` inliner-cost
  change from 1227 to 1231, so checkpoint 4 is rejected for exact ordinary
  identity.
- Attempt 4 runs the baseline layout Go test successfully, then its extractor
  fails because Go splits the layout JSON across eleven ordered `Output`
  fragments. Candidate and later phases do not run.
- Attempt 5 preserves those failures, fixes the bounded proof machinery, and
  completes with status 0.

The detailed sequence is also summarized in `ATTEMPT-HISTORY.md`. Original
statuses, stdout, stderr, reports, manifests, input inventories, comparator
records and source checkpoints remain byte-for-byte unchanged.

## Package layout

`PACKAGE-MAP.tsv` maps every selected immutable source artifact to exactly one
destination and labels it `loose` or `archive`. Fifteen short reports, statuses
and final summaries are readable directly in the package. The remaining 1,120
entries are stored once in `ancillary-observer-evidence.tar.gz` to avoid adding
more than a thousand loose Git files.

The deterministic archive normalizes only tar metadata: member ordering, uid,
gid, owner names and timestamps. Member bytes and selected modes are unchanged.
`ALLOWLIST.tsv` is the explicit selection. `ANCILLARY-BUNDLE.sha256` binds the
archive, `SHA256SUMS` binds every package file, and
`verify-evidence-package.py` independently verifies each loose and archived
entry without extracting the archive.

The source corpus is ignored tmp evidence and is not reconstructable from a
normal checkout. `build-evidence-package.py` therefore requires an explicit
`--source-root`; the published builder records how this package was created,
but cannot recreate absent source artifacts by itself.

## Exclusions and limits

Full repositories, materialized baseline and candidate trees, build caches,
temporary directories, source tar archives, compiled archives, test binaries,
and the 87,479,482-byte checkpoint-1 whole-repository patch are omitted. Their
bytes are not claimed to be published. Retained checkpoint manifests, attempt
manifests, inventories and source-archive hashes preserve the available origin
bindings. `EXCLUSIONS.tsv` records the exclusion classes and retained bindings.

This offline corpus contains no generated endpoint credentials, trace bearer,
TLS private key, readiness document or authenticated request fixture from
which a relevant secret value can be recovered. The builder applies strict
path/name guards. The exact-value secret scan therefore has zero available
values and can establish only zero hits for that empty relevant set; it does
not claim a scan against unrelated workspace secrets.

The observer evidence does not establish a runtime cause or fix and does not
clear the original current-production WAN failure, the 90% throughput floor,
HAProxy parity, pressure, capacity, integration, release or merge gates.

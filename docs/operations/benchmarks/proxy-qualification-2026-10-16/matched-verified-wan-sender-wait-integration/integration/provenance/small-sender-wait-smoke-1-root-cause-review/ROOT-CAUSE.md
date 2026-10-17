# Small sender-wait smoke 1: pre-endpoint failure root cause

This is a read-only source and artifact analysis of
`integration-source/runs/small-sender-wait-smoke-1`. It does not amend the
failed run, checkpoint 7, its generated runner, or the accepted audited Go
binaries.

## Established facts

* Frozen sender integration checkpoint 7 has manifest SHA-256
  `fa0f2237aef78d5253882eef1fc45942446f0d96627efa8e0234161858eedb34`.
  Its generated runner is
  `integration-source/generated/qualify-proxy-agent-growth-wan-sender-wait.sh`,
  SHA-256 `404f2edcfdaf39620de3b75195c4e9317b83782565318d3c3ca17bda023fdc1c`.
  Its arm supervisor is SHA-256
  `c80e29332efa74ac725b5a89a5867e052adc50e2f8cbdc50fd1d014bc49dfd4e`.
* The runner bootstrap at lines 7-8 executes
  `cd "$(dirname "$0")/.."` before argument parsing and namespace re-exec.
  At lines 160-164 it re-executes itself through `unshare` without another
  working-directory change. The inside phase therefore inherits the directory
  selected at line 8.
* The inside phase at lines 174-175 requires
  `./validate-copy-record.py`. It failed with the exact stderr text
  `copy record validator is required`.
* The mature capacity runner, SHA-256
  `e9e453206cd479e1d29b78c05cf4d0eaf0cbaefe3c6ff13f0ba9594235c3bea0`,
  has the same bootstrap and relative validator lookup. The mature arm points
  to the runner under `wan-read-wait-capacity-source/generated` (arm line 5),
  so the bootstrap selects `wan-read-wait-capacity-source`. That directory
  contains executable `validate-copy-record.py`, SHA-256
  `f1c70a2f347a77975a00330d4d37929700b1a38bac5fa195c3dff02b3f773c26`.
* The sender arm instead points to the relocated runner under
  `wan-sender-wait-integration-source/integration-source/generated` (line 8).
  Its identical bootstrap selects
  `wan-sender-wait-integration-source/integration-source`, which contains no
  `validate-copy-record.py`.
* The sender arm already resolves the correct helper as the absolute
  `$capacity_root/validate-copy-record.py` at line 19, requires it executable at
  lines 93-95, and records its hash in `pre-hashes.json` through lines 312 and
  352. The runner command assembled at lines 419-438 passes the sender snapshot
  validator and hash but does not pass the copy-record validator or its hash.

## Why changing the outer working directory is not a fix

The reviewed outer invocation uses absolute coordinator and artifact paths and
contains no `cd`. Regardless of the caller's initial directory, runner line 8
changes to the parent of the generated runner before parsing arguments. A
supervisor-side `cd` before `exec` would be overwritten by that bootstrap. The
failure is a relocated dependency-binding discrepancy, not an accidental
choice of the tool or coordinator working directory.

## Narrow correction recommended for review

Give the generated runner an explicit copy-record-validator path and expected
SHA-256, following the existing sender snapshot-validator pattern:

1. The arm passes its already canonical absolute `$copy_record_validator` and
   the computed SHA-256 in two dedicated arguments.
2. The runner validates the absolute executable and lowercase hash before the
   first namespace re-exec, canonicalizes the path, and exports both values.
3. Both variables are included in the `sudo --preserve-env` fallback list.
4. The inside phase reads only those exported values and rechecks executable
   status and exact hash before use; it no longer uses `./validate-copy-record.py`.
5. Reconstruction and binding contracts cover the new arguments and both
   checks. The timed `run_transfer` body must remain byte-identical.

This avoids copying or symlinking the validator, deriving it from an unrelated
helper path, or weakening the existing fail-closed check. The correction is
shell harness provenance only; the already-audited server and agent binaries
remain unchanged. A new integration checkpoint and reviewed integration-source
manifest should bind the corrected shell corpus, while a separate compatibility
receipt can bind those new harness hashes to the existing immutable build
manifest and exact unchanged binary hashes. The original build manifest should
not be rewritten because its checkpoint-7 harness bindings remain historical
facts.

## Failure boundary and preserved status

The preserved failure audit records zero transfer rows, zero endpoint logs,
zero endpoint readiness files, and zero endpoint processes. H1 returned 1; H2
was not started and retained status 125. The runner process group joined with
wait status 1, supervisor cleanup was not forced, the scope launcher exited 0,
the sentinel exited naturally, and the unit ended `not-found/inactive/dead`.
The failed sample remains invalid and must not be reclassified as traffic or a
performance result.

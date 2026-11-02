# Evidence storage and handoff

Benchmark packets, raw validation receipts, and investigation archives belong in
the private [Kirari04/p2pstream-evidence](https://github.com/Kirari04/p2pstream-evidence)
repository. Access requires repository permission. Keep the application
repository focused on source, regression tests, and concise operator guidance.

## Start with the current handoff

Read the evidence repository's
[README](https://github.com/Kirari04/p2pstream-evidence/blob/main/README.md) and
[AGENTS.md](https://github.com/Kirari04/p2pstream-evidence/blob/main/AGENTS.md)
before preparing or extending a packet, then consult
[CHECKPOINTS.md](https://github.com/Kirari04/p2pstream-evidence/blob/main/CHECKPOINTS.md).
They define the canonical layout, publication procedure, and investigation
checkpoints. Check the current application repository instructions too.

Before reusing a result, verify:

- The recorded source/base commits against the current branch and HEAD.
- Exact source and effective overlay hashes, including test-only changes.
- The tested binaries, toolchain, command, workload, and resource limits.
- Outcome, cleanup evidence, limitations, and unresolved follow-up work.

A stored result applies to its recorded inputs. A new source revision does not
inherit runtime validation from an earlier packet. Keep the earlier result, state
the correspondence and any untested delta, and obtain new validation where the
claim requires it. A successful command does not prove every planned experiment
completed or that an architectural blocker was resolved.

## Prepare an immutable packet

Use a unique packet name that identifies its scope and attempt. Record source,
runtime and test identities and hashes alongside exact commands, raw output, exit status,
outcomes and limitations. Include failed or incomplete attempts and distinguish
them from accepted results. Keep original evidence intact; publish corrections
as separate, linked addenda.

Provide SHA-256 checksums for uploaded archives and an inventory mapping retained
files to their hashes. If compression or deduplication changes storage layout,
retain the mapping to original paths and bytes so reviewers can reconstruct the
evidence. Include only the materials needed to review the stated claim.

Keep raw environment captures, private host details, and other sensitive evidence
out of the public application repository. Review private packets for credentials,
keys, database contents, and unrelated process or network data before uploading;
private storage is not permission to collect or publish those contents.
Public summaries should describe outcomes and limits without local paths,
hostnames, or environment dumps.

## Publish and verify

Store large raw archives as **GitHub release assets** in the evidence repository,
not as Git blobs in either repository. Use the canonical private guide to select
the evidence release and update its small index or checkpoint.

1. Create the packet and checksum manifest locally; retain a local backup.
2. Upload using new, uniquely named release assets. Never replace an existing
   asset with `--clobber` or reuse its name for changed bytes.
3. Verify uploaded state, size, and the API's SHA-256 digest against the local
   originals and manifest. If no digest is available, download into a separate
   verification location and compute it there.
4. Confirm the remote inventory is complete, then record the release/asset links,
   verified hashes, source correspondence, outcomes, and remaining work in the
   private handoff. Keep the local backup until remote verification succeeds.

On upload or verification failure, preserve the local packet and failure record.
Resolve the discrepancy before treating the evidence as remotely preserved.
Append a new packet or addendum when inputs or conclusions change.

Evidence assets are research and validation records. Never use them as production
updater releases, update channels, or installable release substitutes. Application
release promotion continues through `dev → staging → main` and the existing
release/updater verification workflow.

The private `pr-329-20261020` release contains historical PR #329 evidence.
Its existence is an archive reference, not proof that a later source revision or
an unresolved follow-up has passed. Future agents should begin with the current
private checkpoint and verify its source binding before continuing work.

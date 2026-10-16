# Split DATA emission fails the causal retention gate

This diagnostic compares two aggregate-v1 agents that differ only in how an
already-owned Yamux DATA frame reaches the physical tunnel. The control writes
the gathered header and body once. The diagnostic overlay retains the same
copy, buffer capacity, admission, ownership and commit timing, then writes the
immutable header and body separately. The predeclared causal threshold is not
met, so the split-write overlay is rejected and no production emission change
is justified.

This is cause-isolation evidence, not a release qualification result. The
preserved final-runtime WAN acceptance failure remains authoritative. Both
variants use the same fixed server binary from production runtime
`dc738e9000e60cd7ce3a0279127486f3bc05067a`. Both ordinary, tag-off agent
binaries come from base
`5eeccaaa797254d7bde4db9854a337a02c3bc28e`, negotiate aggregate-v1, and use
one legacy control lane plus four aggregate bulk lanes. The split variant adds
only the diagnostic overlay in
`snapshot/overlay/agent-data-gather-revert.patch`; the overlay is not committed to
production source.

## Predeclared workload and result

The fixed cohort contains three fresh endpoint pairs in alternating order:
gather then split, split then gather, and gather then split. Each variant runs
four 256 MiB download warmups, four 256 MiB upload warmups, one retained
transport warmup per direction, and three timed 256 MiB transfers per
direction. The path is configured for 80 ms RTT and 1 Gbit/s. Every receiving
bulk lane must exceed 8 MiB. There are no added live diagnostic polls or
profiles, and there are no selective retries. The unchanged qualification
sampler still collects its normal runtime and resource snapshots.

The diagnostic gate requires every pair's split/gather download median to be
at least 1.10 and every upload median to be at least 0.90. Pair 1 passes the
download threshold, but pairs 2 and 3 fail it. All upload pairs pass.

| Pair / order | Gather download | Split download | Split / gather | Download gate | Gather upload | Split upload | Split / gather | Upload gate |
| --- | ---: | ---: | ---: | --- | ---: | ---: | ---: | --- |
| 1 / gather, split | 27.365 MB/s | 33.752 MB/s | 1.23342 | pass | 35.119 MB/s | 35.052 MB/s | 0.99808 | pass |
| 2 / split, gather | 33.198 MB/s | 25.071 MB/s | 0.75519 | fail | 35.380 MB/s | 35.370 MB/s | 0.99971 | pass |
| 3 / gather, split | 27.569 MB/s | 25.195 MB/s | 0.91389 | fail | 35.801 MB/s | 35.395 MB/s | 0.98868 | pass |

Across all nine timed samples per variant, the split/gather ratio of medians is
0.87637 for downloads and 0.99971 for uploads. Download samples remain widely
variable: the control ranges from 15.552 to 34.825 MB/s and the split variant
from 24.777 to 34.583 MB/s. The direction and size of the pairwise effect are
not stable enough to support a causal improvement.

All 96 transfers complete with verified payloads, HTTP status and protocol.
They persist exactly 96 request events with zero client errors, zero dropped
events and no queue residue after drain. The runner exits 1 after 833.355
seconds because the predeclared timing gate rejects the overlay. The nonzero
exit is the intended result of a complete timing rejection, not a fixture or
correctness failure.

## Build and overlay provenance

`manifest.json`, `build-provenance.txt`, `clean-build-manifest.txt` and the two
saved SHA-256 lists retain the source refs, commands, build IDs and hashes. The
authoritative binaries used by this cohort are:

| Binary | SHA-256 |
| --- | --- |
| Fixed server | `c7950a1bd250233b395eb8c19d913ffb48d511656d7c41a1ad446baefb6bbaf5` |
| Gather-control agent | `0402a49543908e2a0bd00d8c43b93cfdf48177992ce0f4d90c75d5d48aaff92f` |
| Split-write agent | `24ba03fe97f69bed60ae3230fe598789aa00e359e5cd32bc5fc4c468562c8914` |

The final full overlay SHA-256 is
`ca680a7062735c3d1f236fc5d9d53c66b92724fd5b0dd1d82eef53f299b60d4f`.
The resulting production `session.go` file SHA-256 is
`e84c4de1351c78bcefecaac8701cae48b886e306c74118426872bfff402d942d`.
The full overlay differs from the initially reviewed patch only in its
diagnostic test helper: an explicit integer clamp avoids a package-level
`min` name collision. The runtime code is byte-identical. Focused normal and
race tests each pass 20 repetitions. The optional local server relink failed
because that temporary build area hit a disk quota; the cohort intentionally
uses the already-frozen server binary above for both variants, so this did not
change either comparison arm. The failed optional build log is retained.

## Artifacts and limits

- `results.jsonl` preserves all 96 raw rows; `results.summary.json` preserves every
  sample, pairwise gate and pooled diagnostic.
- `predeclared-gates.json`, `manifest.json`, `exit.json`, `preflight.json`,
  `launch-command.json`, `run-command.txt` and the saved runner/wrapper sources
  make the finite cohort reviewable.
- `snapshot/` retains the exact runner, wrapper, overlay and overlay description.
- `logs/` retains build, focused-test and server/agent runtime logs for all six
  endpoint-pair runs.
- `SHA256SUMS` covers every committed file other than itself.

Generated certificates, keys, management CAs, readiness files, SQLite
databases and binaries are intentionally omitted. They contain ephemeral
fixture credentials or are reproducible large artifacts. The committed raw
results and retained logs were scanned for credential-bearing fields before
packaging.

Writing a header and body separately can change TCP segmentation and syscall
behavior, but this cohort does not show the stable download improvement needed
to retain that overlay. It does not establish that the original gather change
cannot contribute through copy timing or another endpoint difference, and it
cannot identify the remaining intermittent receive-window growth delay.
Subsequent work must preserve the original WAN failure and test a different,
measured mechanism rather than selecting the one favorable pair.

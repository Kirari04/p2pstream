# Split refund emission fails the causal retention gate

This diagnostic compares two ordinary, tag-off aggregate-v1 endpoint pairs built
from the same frozen application and Yamux scaffold. The gathered-control arm
keeps a validated refund batch in one physical write. The split-control arm
changes only a private boolean initializer so batches containing 2–17 ordinary
12-byte refund headers use one physical write per header inside the same bounded
priority item and the same joint pre-wire commit. DATA emission, singleton and
unrelated controls, admission, policy, observability, and the fixed driver/origin
are unchanged.

The complete 96-transfer cohort **fails its primary causal retention gate**.
Split/gathered median download ratios were `0.882892`, `1.072481`, and
`1.231369`; every pair had to reach `1.10`. Upload ratios were `0.984737`,
`0.981628`, and `0.988686`, all above their `0.90` guard. Payloads and recorder
events were exact, no non-timing failure occurred, and the wrapper exited 1 as
designed. Splitting physical refund writes is therefore not retained as a
production change.

The independent transition screen passed, but only narrowly. All 24 split-arm
download samples were at least 29.7 MB/s, while every gathered-control pair had
a sub-threshold download. The split minimum was `29,702,647.115` bytes/s, only
`2,647.115` bytes/s or `0.0089%` above the predeclared floor. The gathered arm's
minimum was `27,185,014.028` bytes/s; this cohort did not reproduce the earlier
12–15 MB/s stalls. The summary's
`transition_screen.consistent_with_stall_elimination=true` means only that this
secondary screen passed. It does not override the failed primary gate, establish
robust stall elimination, or justify the split implementation.

## Frozen design

- Immutable runner checkpoint: `e934a182d4644ffa79d0df9a837d21ec0fb0d09f`.
- Application and harness base: `5eeccaaa797254d7bde4db9854a337a02c3bc28e`.
- Common scaffold patch SHA-256:
  `e78b384e8cf2fa28339ca2794dd32aaf57053a2b07aeca873fe4f2430a40c6f7`.
- Split-arm boolean patch SHA-256:
  `28f4c3d5ac08d32c0e146dde3b1502a2cd8250be200da6c4b7e002eeb02386ed`.
- Both arms used aggregate-v1 with one legacy control lane and four aggregate
  bulk lanes. The fixed driver/origin is the common-scaffold gathered-control
  server binary.
- Three fresh endpoint pairs alternated gathered→split, split→gathered,
  gathered→split. Each arm ran four 256 MiB download warmups, four 256 MiB
  upload warmups, one retained transfer per direction, and three timed transfers
  per direction at configured 80 ms RTT and 1 Gbit/s shaping.
- The run completed all 96 transfers in 791.039 seconds. The nonzero exit is the
  predeclared timing rejection, not a payload, event, cleanup, or infrastructure
  failure.

## Results

| Pair | Order | Download split/gathered | DL gate | Upload split/gathered | UL gate |
| ---: | --- | ---: | --- | ---: | --- |
| 1 | gathered → split | 0.882892 | fail | 0.984737 | pass |
| 2 | split → gathered | 1.072481 | fail | 0.981628 | pass |
| 3 | gathered → split | 1.231369 | pass | 0.988686 | pass |

The pooled timed-download ratio of medians was `1.078639`, which is descriptive
only because the gate required every fresh pair to pass. `all-phases.csv`
retains all conditioning, retained-transport, and timed samples so first-use and
reuse behavior remain visible instead of being hidden by pooled medians.

## Artifact map

- `results.jsonl` contains all 96 immutable result rows.
- `results.summary.json` is the executed analyzer output, including both the
  primary rejection and the independent transition screen.
- `all-phases.csv` is a lossless tabular projection of the summary's 96
  `all_phase_rows` entries.
- `manifest.json`, `predeclared-gates.json`, `run-command.txt`, `command.json`,
  `exit.json`, `actual-binary-build-info.json`, `launcher.py`, and `launcher.log`
  preserve invocation and binary identity.
- `snapshot/` contains the exact runner, wrapper, common scaffold, boolean
  toggle, source hashes, build manifest, and build provenance used by the run.
- `build/` retains the direct control/candidate focused normal, focused race,
  and full-normal PASS logs, the build environment, and the exact source diff.
- `validation/preflight/` preserves the no-workload structured preflight.
- `validation/external-smoke/` preserves the fixture-only exact 4 MiB download
  and upload through each arm, exact event conservation, five-lane readiness,
  and natural agent/server/origin shutdown.
- `validation/synthetic/` preserves the five analyzer inputs and outputs showing
  that wrong order, malformed identity, and event-conservation errors cannot be
  classified as a timing-only rejection.
- `validation/negative-manifest/` retains the malformed structured manifest and
  its fail-closed preflight result.

Runtime ready files, fixture credentials, certificates and keys, databases,
binaries, endpoint logs, stop files, temporary source trees, and unrelated
environment state are deliberately excluded.

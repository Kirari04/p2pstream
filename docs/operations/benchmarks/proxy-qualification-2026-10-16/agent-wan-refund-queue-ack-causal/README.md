# Separate refund queue items fail the primary causal retention gate

This diagnostic compares two ordinary, tag-off aggregate-v1 endpoint pairs
built from the same frozen application and Yamux scaffold. The batched-item arm
queues a validated batch containing 2–17 ordinary 12-byte refund headers as one
priority item and waits for one acknowledgement. The separate-items arm changes
only a private boolean initializer so the same headers use sequential priority
items with acknowledgement between them. Both arms use one shared deadline and
the same joint accounting commit, carried only by the first item. DATA gather,
singleton and unrelated controls, admission, policy, observability, and the
fixed driver/origin are unchanged.

The complete 96-transfer cohort **fails its primary causal retention gate**.
Separate/batched median download ratios were `1.088060`, `1.111491`, and
`1.056268`; every pair had to reach `1.10`. Upload ratios were `1.017355`,
`0.997241`, and `1.003924`, all above their `0.90` guard. Payloads and recorder
events were exact, no non-timing failure occurred, and the wrapper exited 1 as
designed. Sequential refund queue items with per-item acknowledgement are
therefore not retained as a production change.

The independent transition screen passed. All 24 separate-items download
samples were at least 29.7 MB/s, while every batched-item pair had a
sub-threshold download. The separate-items minimum was `29,779,034.200` bytes/s.
The batched-item minimum was `14,257,828.201` bytes/s, reproducing a severe
first-direction stall in pair 1. Separate-items first-direction timed downloads
were about 29.8–30.6 MB/s and reused-direction downloads were about
34.1–34.6 MB/s. This remains a transition penalty rather than restoration of
the historical 33–35 MB/s range on every transfer.

The summary's `transition_screen.consistent_with_stall_elimination=true` means
only that the separately predeclared floor screen passed in this cohort. It
does not override the failed primary gate, establish a production fix, or
isolate queue acknowledgement from the separate arm's intrinsic per-item
allocation and scheduling changes.

## Frozen design

- Immutable runner checkpoint:
  `b5d369db38fbd941b8def1c4368d679572b70963`.
- Application and harness base:
  `5eeccaaa797254d7bde4db9854a337a02c3bc28e`.
- Common scaffold patch SHA-256:
  `dd1b8a0ef22a7e29fc112bdd201bb758031eaafbaff8e912f90b0e8e50fef53c`.
- Separate-items boolean patch SHA-256:
  `b50b28cbafdb4b7609a547f781154446c484efe381b623dfac4524d5e01a4431`.
- Both arms used aggregate-v1 with one legacy control lane and four aggregate
  bulk lanes. The fixed driver/origin is the common-scaffold batched-item server
  binary.
- Three fresh endpoint pairs alternated batched→separate, separate→batched,
  batched→separate. Each arm ran four 256 MiB download warmups, four 256 MiB
  upload warmups, one retained transfer per direction, and three timed transfers
  per direction at configured 80 ms RTT and 1 Gbit/s shaping.
- The outer launcher completed in 796.258 seconds. The nonzero exit is the
  predeclared timing rejection, not a payload, event, cleanup, or infrastructure
  failure.

## Results

| Pair | Order | Download separate/batched | DL gate | Upload separate/batched | UL gate |
| ---: | --- | ---: | --- | ---: | --- |
| 1 | batched → separate | 1.088060 | fail | 1.017355 | pass |
| 2 | separate → batched | 1.111491 | pass | 0.997241 | pass |
| 3 | batched → separate | 1.056268 | fail | 1.003924 | pass |

The pooled timed-download ratio of medians was `1.107181`, which is descriptive
only because the gate required every fresh pair to pass. `all-phases.csv`
retains all conditioning, retained-transport, and timed samples so the severe
first-direction sample and later reuse behavior remain visible.

## Artifact map

- `results.jsonl` contains all 96 immutable result rows.
- `results.summary.json` is the executed analyzer output, including both the
  primary rejection and independent transition screen.
- `all-phases.csv` is a lossless tabular projection of the summary's 96
  `all_phase_rows` entries.
- `manifest.json`, `predeclared-gates.json`, `run-command.txt`, `command.json`,
  `exit.json`, `actual-binary-build-info.json`, `launcher.py`, and `launcher.log`
  preserve invocation and binary identity.
- `snapshot/` contains the exact runner, wrapper, common scaffold,
  separate-items toggle, source hashes, build manifest, and build provenance
  used by the run.
- `build/` retains the endpoint compile logs, patch replay proof, and both arms'
  focused normal, focused race, and full Yamux normal PASS logs.
- `validation/preflight/` preserves the no-workload structured preflight.
- `validation/external-smoke/` preserves the fixture-only exact 4 MiB download
  and upload through each arm, exact event conservation, five-lane readiness,
  and natural agent/server/origin shutdown.
- `validation/synthetic/` preserves the five analyzer inputs and outputs showing
  that wrong order, malformed provenance, and event-conservation errors cannot
  be classified as a timing-only rejection.
- `validation/negative-manifest/` retains missing-identity and malformed
  structured manifests with their fail-closed preflight results.

Runtime ready files, fixture credentials, certificates and keys, databases,
binaries, endpoint logs, stop files, temporary source trees, and unrelated
environment state are deliberately excluded.

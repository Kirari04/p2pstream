# Deferred-global refund epochs fail both diagnostic retention screens

This diagnostic compares two ordinary, tag-off aggregate-v1 endpoint pairs
built from the same frozen application and Yamux scaffold. The control arm
publishes the current complete refund batch. The deferred arm changes only a
private boolean initializer: each epoch selects at most 16 serial stream
refunds, including requeues and terminal refunds, then flushes their accumulated
matching global refund even if the stream queue remains nonempty. If selection
empties that queue, the same locked observation may also include bounded
global-only credit. Both arms retain one epoch I/O deadline, current ownership
and accounting, the selected-stream registry, DATA handling, lazy wake,
shutdown behavior, application policy, observability, and the fixed
driver/origin.

The single epoch deadline bounds queue and acknowledgement I/O waits. Acquiring
`controlHdrLock` and `publishMu` remains synchronous and is not interruptible,
so the deadline is not an absolute wall-clock bound against an arbitrarily
blocked lock holder.

The complete 96-transfer cohort **fails its primary causal retention gate**.
Deferred/control median download ratios were `1.081332`, `0.895806`, and
`1.113080`; every pair had to reach `1.10`. Upload ratios were `1.000092`,
`0.984962`, and `1.002254`, all above their `0.90` guard. Payloads, HTTP status
and protocol, and recorder events were exact. No non-timing or cleanup failure
occurred, and the wrapper exited 1 as designed.

The independent transition screen also failed. All 24 deferred-arm download
samples had to reach 29.7 MB/s, but the minimum was
`29,488,343.751` bytes/s. The current-control arm had a sub-threshold download
in all three fresh pairs, satisfying only that half of the screen. Its minimum
was `27,335,534.294` bytes/s; this cohort did not reproduce the earlier
12–15 MB/s severe stall. The result does not justify a production change or
show that deferred-global epochs eliminate the directional transition penalty.

The causal `1.10` threshold belongs to this diagnostic. Production acceptance
remains the separately defined aggregate-versus-legacy non-regression work; a
failure or pass here does not replace that criterion.

## Frozen design

- Immutable runner checkpoint:
  `557bfa2970e21166530cf216bd5ef57ccd4b54c9`.
- Application and harness base:
  `5eeccaaa797254d7bde4db9854a337a02c3bc28e`.
- Common scaffold patch SHA-256:
  `3fa10c967b69d4d371c64dd6bb2ca175ffbd9f98d80193d3d41b2722809bfe59`.
- Deferred-global boolean patch SHA-256:
  `d07cac9d6f5dac0311416282c0c0e19ece2d3369e1d372abc43b406b3e33b81d`.
- Build manifest SHA-256:
  `dd606c6e0478a6e6c46e4d765122803b7ab20b3dfc23fb4d14dd0ceb54da7f19`.
- Runner and wrapper SHA-256 values are
  `a34a8c7c4c059b9f0cc7985e62edd9ae388b8ce189367747bd0ba3e0f148b9a1`
  and
  `4300d079c004bd00467b376460375d96c52eced6f6c9d6b06af266cec096688c`.
- Both arms used aggregate-v1 with one legacy control lane and four aggregate
  bulk lanes. The fixed driver/origin is the common-scaffold current-control
  server binary.
- Three fresh endpoint pairs alternated control→deferred, deferred→control,
  control→deferred. Each arm ran four 256 MiB download warmups, four 256 MiB
  upload warmups, one retained transfer per direction, and three timed transfers
  per direction at configured 80 ms RTT and 1 Gbit/s shaping.
- The outer launcher completed in `796.932` seconds. The nonzero exit is the
  predeclared timing rejection; `forced_group_cleanup=false`.

## Results

| Pair | Order | Download deferred/control | DL gate | Upload deferred/control | UL gate |
| ---: | --- | ---: | --- | ---: | --- |
| 1 | control → deferred | 1.081332 | fail | 1.000092 | pass |
| 2 | deferred → control | 0.895806 | fail | 0.984962 | pass |
| 3 | control → deferred | 1.113080 | pass | 1.002254 | pass |

The pooled timed-download ratio of medians was `1.064624`, which is descriptive
only because the primary gate required every fresh pair to pass.
`all-phases.csv` retains all conditioning, retained-transport, and timed samples.

## Artifact map

- `results.jsonl` contains all 96 immutable result rows.
- `results.summary.json` is the executed analyzer output, including the primary
  rejection, independent transition-screen rejection, and complete phase table.
- `all-phases.csv` is a lossless tabular projection of the summary's 96
  `all_phase_rows` entries.
- `manifest.json`, `predeclared-gates.json`, `run-command.txt`, `command.json`,
  `exit.json`, `actual-binary-build-info.json`, `launcher.py`, and `launcher.log`
  preserve invocation and binary identity.
- `snapshot/` contains the exact runner, wrapper, common scaffold,
  deferred-global toggle, source hashes, build manifest, and build provenance
  used by the run.
- `build/` retains the endpoint compile logs, patch replay proof, focused normal,
  focused race, conservation, and full Yamux normal PASS logs.
- `validation/preflight/` preserves the no-workload structured preflight.
- `validation/external-smoke/` preserves the fixture-only exact 4 MiB H2
  download and upload through each arm, HTTP 200/origin HTTP/1 checks, event
  conservation, five-lane readiness, and natural agent/server/origin shutdown.
- `validation/synthetic/` preserves analyzer source plus seven input/output
  cases. Wrong order, event conservation, provenance, status, and protocol
  errors fail as non-timing results; a timing-only rejection remains distinct.
- `validation/negative-manifest/` retains missing-identity and malformed
  structured manifests with their fail-closed preflight results.

Runtime ready files, fixture credentials, certificates and keys, databases,
binaries, endpoint logs, stop files, temporary source trees, and unrelated
environment state are deliberately excluded.

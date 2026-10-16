# A 32 KiB tunnel LOWAT does not retain a causal improvement

This diagnostic compares two ordinary, tag-off aggregate-v1 endpoint pairs
built from the same frozen application and Yamux scaffold. The control records
the physical tunnel socket settings without changing them. The candidate
changes only a private boolean initializer so both authenticated physical
tunnel endpoints request `TCP_NOTSENT_LOWAT=32768`. Yamux, DATA and refund
behavior, accounting, buffers, admission, shutdown, application behavior, and
the fixed driver/origin are otherwise identical.

The complete 96-transfer cohort **fails its primary causal retention gate**.
Candidate/control median download ratios were `0.987288`, `0.991449`, and
`0.998383`; every fresh pair had to reach `1.10`. Upload ratios were `1.007950`,
`0.998544`, and `0.988641`, all above their `0.90` guard. The result is roughly
neutral rather than a download improvement. Payloads, status codes, public H2,
origin H1, and recorder events were exact, no non-timing failure occurred, and
the wrapper exited 1 as designed.

The independent transition screen also failed. All 24 candidate download
samples had to reach 29.7 MB/s, but the minimum was
`27,245,277.967` bytes/s. Every control pair also had a sub-threshold sample, so
the screen's control-slow prerequisite passed without changing the rejection.
The control minimum was `27,132,680.802` bytes/s. This cohort did not reproduce
the earlier 12–15 MB/s severe download stall; it therefore provides no evidence
that LOWAT eliminates that transition. The earlier aggregate/legacy WAN
qualification remains unresolved, and this temporary LOWAT option is not
retained for production.

## Frozen design

- Immutable runner checkpoint:
  `d02beed278d5d8a9733b617f3343a18d882c2546`.
- Application and harness base:
  `5eeccaaa797254d7bde4db9854a337a02c3bc28e`.
- Common temporary scaffold SHA-256:
  `1f5406709d4347975f6110830fbaf24ef06f96e6a22280071ac8ab76eaba24d8`.
- LOWAT boolean patch SHA-256:
  `076c8dfc22212296e30b9430c9654625dd312b03cfaa19a55b1faa4c1fb69379`.
- Build manifest SHA-256:
  `1c3cc0f51ca0cbabc9d45b3b6d52cdba5334ef8170d870112b02dfcd83e34d06`.
- Both arms used aggregate-v1 with one legacy control lane and four aggregate
  bulk lanes. The fixed driver/origin was byte-identical to the control server.
- Three fresh endpoint pairs alternated control→LOWAT, LOWAT→control,
  control→LOWAT. Each arm ran four 256 MiB download warmups, four 256 MiB
  upload warmups, one retained transfer per direction, and three timed transfers
  per direction at configured 80 ms RTT and 1 Gbit/s shaping.
- The outer launcher completed in 804.457 seconds. The nonzero exit is the
  predeclared timing rejection, not a payload, event, socket-observation,
  cleanup, or infrastructure failure.

Each endpoint exposed a dedicated bounded socket-observation endpoint, which
the runner queried only at startup and after all 16 transfers. Every arm and
pair had exactly five
conclusive socket records, server lanes 0–4 matched the reverse agent tuples,
no records were dropped, and the final records were identical to the initial
records. The namespace baseline was `4294967295`; agent raw pre-setting values
were zero and therefore inherited that baseline, while server raw values were
already `4294967295`. Both roles had an effective baseline above 32768. The
candidate's five sockets per endpoint reported an applied value of exactly
32768; the control made no setter call. Send and receive buffer values had to
be readable and positive, while their equality and evolution were intentionally
not acceptance gates.

## Results

| Pair | Order | Download LOWAT/control | DL gate | Upload LOWAT/control | UL gate |
| ---: | --- | ---: | --- | ---: | --- |
| 1 | control → LOWAT | 0.987288 | fail | 1.007950 | pass |
| 2 | LOWAT → control | 0.991449 | fail | 0.998544 | pass |
| 3 | control → LOWAT | 0.998383 | fail | 0.988641 | pass |

The pooled timed-download ratio of medians was `0.991449` and the pooled
timed-upload ratio was `0.998544`. Both are descriptive only because the causal
gate was evaluated independently for every fresh pair. `all-phases.csv`
preserves all conditioning, retained-transport, and timed samples.

## Artifact map

- `results.jsonl` contains all 96 immutable result rows.
- `results.tunnel-lowat-observations.jsonl` contains all 12 complete before/after
  socket snapshot records used by the cohort gate.
- `results.summary.json` is the executed analyzer output, including the primary
  and independent transition-screen rejections.
- `all-phases.csv` is a lossless tabular projection of the summary's 96
  `all_phase_rows` entries.
- `manifest.json`, `predeclared-gates.json`, `run-command.txt`, `command.json`,
  `exit.json`, `actual-binary-build-info.json`, `launcher.py`, and `launcher.log`
  preserve the invocation and exact binary identities.
- `snapshot/` contains the exact runner, wrapper, temporary common scaffold,
  LOWAT toggle, source hashes, build manifest, build provenance, toolchain
  metadata, and build/test validation used by the run.
- `build/` retains a second portable copy of the reviewed patches, source and
  build identities, patch replay proof, endpoint compile logs, and both arms'
  focused normal, focused race, and full related-suite PASS evidence.
- `validation/preflight/` preserves the no-workload structured wrapper
  preflight.
- `validation/external-smoke/` preserves the exact adapter and fixture-only
  4 MiB H2 download and upload through both arms, exact events and payloads,
  stable five-socket observations, correct LOWAT contrast, and natural
  agent/server/origin shutdown.
- `validation/synthetic/`, `validation/online-validator/`, and
  `validation/contract-negatives/` preserve the valid cases and fail-closed
  missing-field, malformed-type, dropped/extra/replaced socket, tuple mismatch,
  wrong-setting, cross-arm mismatch, and ineffective-contrast cases. Those
  failures are non-timing failures and cannot produce the secondary claim.

Runtime ready files, fixture credentials, certificates and keys, databases,
binaries, endpoint logs, stop files, temporary source trees, and unrelated
environment state are deliberately excluded.

# WAN allocation counters refute the frame-amplification hypothesis

This is diagnostic evidence, not an acceptance run. The unchanged
[three-pair stability cohort](../final-runtime-wan-stability/README.md) remains
the production qualification result: every fresh pair fails the 90% download
gate. This tagged replay captures the slowdown again, with a 42.43% download
ratio and a 97.97% upload ratio. It measures successful DATA reservations and
receive-window growth after each transfer, and refutes the proposed mechanism
that 4 KiB allocation rounding multiplies DATA allocation cost by roughly
eight for the slow downloads.

Production behavior remains based on
`dc738e9000e60cd7ce3a0279127486f3bc05067a`. Tagged diagnostic source
`5eeccaaa797254d7bde4db9854a337a02c3bc28e` adds bounded counters and drain
endpoints behind `p2pstream_qualification`; ordinary binaries do not expose
them. Current server and agent test binaries were built sequentially from a
clean archive of that exact ref. The compatible legacy agent stays frozen at
`3ec137c3ad3d07028e867042fb00d3933912d7ab` with its preserved qualification
overlay.

## Workload and result

The original 32-transfer order is unchanged: configured 80 ms / 1 Gbit/s,
four fresh 256 MiB warmups per direction, retained warmups, then three timed
transfers per direction, legacy followed by aggregate. The receiving-pool
assertion remains 8 MiB. There are no live polls or native profiles. Server and
current-agent growth/counter responses are drained only after the original
transfer validation, result append and runtime query. Frozen legacy-agent
drains are explicit skips.

All 32 transfers return verified HTTP 200 payloads with exactly 32 persisted
events, zero client errors, zero event drops and no queue residue. The runner
exits 1 after 283.757 seconds because the original timing gate fails. The
runner exits before emitting its summary on this path, so
`derived-timing-summary.json` derives the same ratio-of-medians from the
complete raw rows:

| Direction | Legacy median | Aggregate median | Ratio | 90% gate |
| --- | ---: | ---: | ---: | --- |
| Download | 33.776 MB/s | 14.333 MB/s | 42.43% | fail |
| Upload | 36.023 MB/s | 35.291 MB/s | 97.97% | pass |

The slow aggregate timed downloads are 14.310 and 14.333 MB/s; the reused
lane's next download is 33.598 MB/s. The result is diagnostic and observer
affected. It reinforces the preserved uninstrumented failure but does not
replace it.

## Allocation result

The analyzer selects the directional sender's enabled bulk lane by its largest
reserved payload total. Across eight aggregate downloads it records
67,098 DATA frames, 2,147,485,488 payload bytes and 2,156,371,968 charged
bytes:

| Direction | Mean payload/frame | Mean charge/frame | Charge/payload | Byte-credit zero observations | Stream-allocation zero observations | Session-allocation zero observations |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Download | 32,005 B | 32,138 B | 1.00414 | 266 | 24 | 0 |
| Upload | 32,720 B | 32,722 B | 1.00006 | 159 | 22 | 0 |

The two captured slow timed downloads reserve 31,945 and 32,283 payload bytes
per DATA frame and charge 1.00059 and 1.00043 times their payload.
This rules out eightfold allocation amplification from small fragmented DATA
frames in these cases. No aggregate allocation-unit or growth-policy change is
justified by that hypothesis.

The slow downloads have 71--75 byte-credit-zero observations and need about
16.0--16.2 seconds from their first retained growth sample to a successful
16 MiB proposal. The later healthy download has 17 byte-credit-zero
observations and reaches 16 MiB in 1.307 seconds. These counters are
overlapping send-attempt observations, not waits, durations or time fractions.
Their correlation with delayed demand-based receive-window growth does not by
itself establish the remaining cause or a safe fix. No reservation denial or
publication error is present.

Agent Yamux addresses in the tagged registry are intentionally opaque
`yamux:local` / `yamux:remote` values. Download linkage therefore uses the
sender lane and the receiving server registry session identity. Server-side
records retain a physical tunnel tuple when available. Opaque tuples are
reported as unavailable evidence, never treated as a reverse physical tuple.

## Completeness and observer limits

`growth/` retains all 64 responses: 48 real drains and 16 frozen-agent skips.
The growth analyzer validates 2,432 unique epochs, 220 successful growth
outcomes, 2,212 no-proposal outcomes, and zero dropped, unmapped, duplicate or
inverted epochs. `allocation-analysis/aggregate-send-counters.csv` retains all
224 session rows, including disabled lane 0 and enabled bulk lanes 1--4 for
each aggregate drain. Sixteen aggregate primary-sender transfers have positive
frame, payload and charged totals at least as large as the configured body.

Successful DATA reservation counters reuse the already-held aggregate mutex;
the byte-credit-zero observation helper adds an acquisition of the existing
aggregate mutex. Growth events retain bounded references until the destructive
post-transfer drain. The two drain subprocesses add a median 72.595 ms between
transfers, with a maximum of 93.047 ms and a total of 2.364 seconds. These
effects make this a cause diagnostic only. Zero-observation totals cannot be
converted into blocked time.

## Preserved validator failure

The first run stopped after 17 complete result rows and 33 drains because the
root-side helper incorrectly required all five sessions in a negotiated group
to report aggregate counters. Lane 0 deliberately uses legacy Yamux and must
report `enabled:false`; bulk lanes 1--4 report `enabled:true`. The helper also
updated result metadata only after validation, so the decoded response that
triggered the exception was not saved. The complete partial artifact is under
`partial-validator-failure/`, including the failing helper, traceback, 17 raw
rows and 33 saved drains. It is fixture-failure evidence only and supports no
runtime or timing conclusion.

`validator-correction.patch.txt` preserves the narrow correction: validate
lane 0 disabled and lanes 1--4 enabled, then save the decoded response before
performing later assertions. The successful cohort uses the same clean
binaries and workload.

## Artifacts and replay

- `results.jsonl` and `derived-timing-summary.json` preserve the complete raw
  transfers and failed gate.
- `growth/`, `analysis/growth-events.csv`, `growth-per-stream.json` and
  `growth-summary.json` preserve and validate every receive-growth event.
- `allocation-analysis/aggregate-send-counters.csv`,
  `allocation-per-transfer.json` and `allocation-summary.json` preserve all
  per-session counters, directional selection and derived totals.
- `manifest.json`, `clean-build-manifest.txt` and
  `clean-build.SHA256SUMS` record exact source, toolchain, commands, archive
  and binary hashes. Binaries are intentionally omitted.
- The saved original/diagnostic runners, exact patches, drain and analyzer
  sources, logs and launch command make the finite diagnostic reviewable.
- `SHA256SUMS` covers every committed evidence file other than itself.

The authoritative current binary hashes are:

| Binary | SHA-256 |
| --- | --- |
| Tagged server | `bdeec7bdc9b381237c194de7b99c1126841f427618d24213847a1f8c280d99c7` |
| Tagged aggregate agent | `3c81510fd39b6c341a106bbc06e7fa58fc3db8179d843a58c60648ec4e881e0c` |
| Frozen compatible legacy agent | `e0e2f9e1db4b19d0032b5df06c6d311ccaf9500f4879f85f4ca8798bf94b44ec` |

The tagged source archive SHA-256 is
`9462183f979ff47b3bbbb81573bfb6a31bfa04015a31312c8872726582028c76`;
the clean build manifest SHA-256 is
`1369f4d5c83c83ec3c297df9002f5dbc5ec2e8c714980a464f353f5fd430c6d2`.
Recreate the archive with
`git archive 5eeccaaa797254d7bde4db9854a337a02c3bc28e`, make a `bin`
directory, then run the recorded `go test -tags=p2pstream_qualification -c`
commands. Rebuild the frozen legacy fixture with the preserved overlay named
in its manifest. Restore `.txt` script suffixes, replace the saved absolute
diagnostic root consistently with a fresh directory, and invoke
`run-command.txt`. Run both analyzers only after `exit.json` exists. Do not
convert this tagged diagnostic into an acceptance result.

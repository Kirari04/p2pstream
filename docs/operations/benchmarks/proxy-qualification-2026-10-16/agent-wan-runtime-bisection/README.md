# Historical Yamux runtime bisection is a complete timing rejection

This diagnostic compares two aggregate-v1 endpoint pairs built from the same
application and qualification harness at
`5eeccaaa797254d7bde4db9854a337a02c3bc28e`. The control uses the Yamux
production files from that same source tree. The historical arm transplants
exactly `aggregate_window.go`, `session.go`, and `stream.go` from
`56336cb212ecd280c926084f43d60a4365f6b843` into the common tree. Both the
server and agent change together. A single current driver/origin binary is used
for both arms.

The result is a **complete timing rejection**. All 96 transfers have exact
payload, status, protocol, and event conservation, but the predeclared
every-pair download gate fails in pair 2. The historical runtime is also
diagnostic-only because it predates the bounded shutdown-waiter correction.
This evidence does not support restoring it.

## Predeclared workload and result

Three fresh endpoint pairs ran in fixed alternating order: current then
historical, historical then current, and current then historical. Each endpoint
ran four 256 MiB download conditioning transfers, four 256 MiB upload
conditioning transfers, one retained-transport transfer per direction, and
three timed 256 MiB transfers per direction. The path was configured for
80 ms RTT and 1 Gbit/s. Every receiving bulk lane had to exceed 8 MiB. There
were no selective retries.

The diagnostic required every pair's historical/current download median to be
at least 1.10 and every upload median to be at least 0.90.

| Pair / order | Current download | Historical download | Historical / current | Download gate | Current upload | Historical upload | Historical / current | Upload gate |
| --- | ---: | ---: | ---: | --- | ---: | ---: | ---: | --- |
| 1 / current, historical | 28.854 MB/s | 33.092 MB/s | 1.14687 | pass | 35.524 MB/s | 35.538 MB/s | 1.00041 | pass |
| 2 / historical, current | 33.333 MB/s | 33.342 MB/s | 1.00026 | **fail** | 35.837 MB/s | 35.651 MB/s | 0.99482 | pass |
| 3 / current, historical | 27.618 MB/s | 33.644 MB/s | 1.21818 | pass | 35.784 MB/s | 36.198 MB/s | 1.01158 | pass |

The pooled timed diagnostic is 1.16053 for download and 0.99630 for upload.
It cannot override the every-pair gate. Pair 2 also demonstrates why the phase
shape matters: the current timed download samples are 33.333, **15.338**, and
33.356 MB/s. The median hides the 15.34 MB/s stall. Pair 3 has a separate
12.431 MB/s current retained-transport download followed by timed downloads of
27.499, 27.618, and 34.490 MB/s.

The recurring retained, first-direction, and reused-direction pattern
prioritizes a phase- or direction-sensitive hypothesis. It is not a broad
historical-runtime improvement and does not localize a cause. `all-phases.csv`
keeps all 96 rows in execution order, including conditioning, retained, and
timed transfers. `summary.json` retains the per-pair phase distributions and
the first-direction/reused-direction grouping.

All 96 attempts completed with verified payloads and 96 persisted events.
There were zero client errors, dropped events, or queue residue after drain.
The runner intentionally exited 1 after 806.513 seconds because the timing gate
rejected the historical arm.

## Build and fixture provenance

`manifest.json`, `predeclared-gates.json`, `actual-binary-build-info.json`,
`build/`, and `snapshot/` preserve the exact source trees, three-file overlay,
ordinary tag-off commands, binary hashes, Go build IDs, runner, and wrapper.
The executed endpoint binaries were:

| Role | SHA-256 |
| --- | --- |
| Current agent | `d838d430881498cb19d969770a5af451398d037e045eb48c01f6af81e2d39334` |
| Current server and fixed driver/origin | `363833f3d9c07d436ee7f1d523f39de662eda678f59e3e23cdc52a7d10a3b642` |
| Historical agent | `c2f51cae23addadc3c1285f0f7597859d7d8596763e7435e5277f9f92d31b85f` |
| Historical server | `0f205cc1aeef1b7d6d8ef0407740959e7b42be0194a664bf965f8b8db60063ab` |

The historical overlay SHA-256 is
`c400c60b4b20c7a2be3c695481970ae5d43e99446de4afde2a9180b644bcf8cf`.
The fixed driver/origin is byte-identical to the current server binary.

The bounded fixture validation in `validation/` checks fail-closed provenance,
row order, exact event conservation, owned process-group cleanup, and real
external current/historical startup and shutdown. The successful smoke sends
one exact 4 MiB download and upload through each five-lane aggregate-v1 pair,
then observes natural server, agent, and origin exit. The first smoke stops
after one current download because its test resource-envelope name omitted the
required `p2pstream` prefix, so event counters were not emitted. That preserved
attempt is an instrumentation failure, not a runtime failure or performance
sample. The reconstructed first adapter is explicitly labeled as reconstructed
and its recorded SHA-256 matches the executed source.

## Artifact scope

- `results.jsonl` and `all-phases.csv` contain the complete 96-transfer record.
- `summary.json` contains every pair gate and all phase distributions.
- `snapshot/` contains the executed runner, wrapper, historical overlay, and
  structured build metadata.
- `build/` contains an explicit allowlist of clean-build metadata and logs.
- `validation/` contains an explicit allowlist of source, results, and logs for
  the bounded preflight, synthetic, process-group, and external smoke checks.
- `SHA256SUMS` covers every committed file other than itself.

Ephemeral certificates, private keys, management CAs, readiness files, stop
files, SQLite files, binaries, build trees, and raw endpoint logs are omitted.
The selected result, build, and validation files were scanned for
credential-bearing values before packaging.

The preserved raw failure remains the decision: the historical arm passes two
download pairs and all uploads, but fails the fixed download threshold in the
second fresh pair. A later causal experiment must retain the entire phase shape
and cannot treat the isolated slow sample as a disposable warmup.

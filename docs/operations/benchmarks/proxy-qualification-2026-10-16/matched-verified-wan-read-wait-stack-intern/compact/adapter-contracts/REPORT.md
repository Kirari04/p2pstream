# Adapter lifecycle contract report

## Frozen inputs

- Production adapter: checkpoint 7 `run_interned_trace_parser.py`, SHA-256
  `719eac5dac37a0dc686283025e7005aba3cbf5408436b6257e6fde8811a50df8`.
- Checkpoint 7 `SHA256SUMS.txt`: SHA-256
  `8a642d8cd435700f95d8016d8c526fcd178cd5197860228e5db12d275086e6b0`.
- Test-only contract driver: `run_adapter_contracts.py`, SHA-256
  `9f3b778aeeefa2fd4eb52803beeef68566ae8d9605206e36dceed4920762dd96`.
- Launch wrapper: `runs/run-attempt-1.sh`, SHA-256
  `d273c4ab560a1a6c072437bd8b7b20ad3502d1fd786f31f8fec96cc7ef6a53c8`.

The driver imports the frozen adapter. It does not edit or copy its source. It
replaces only `subprocess.Popen` with tiny Python fixture children after the
adapter completes its normal preflight. The adapter's own ownership,
registration, drain, status capture, TERM/KILL, join, group-absence, artifact
invalidation, and publication paths execute unchanged.

Test-only constants were `NORMAL_DEADLINE_SECONDS=0.20`,
`TERM_GRACE_SECONDS=0.20`, `KILL_GRACE_SECONDS=0.50`,
`POLL_SECONDS=0.005`, and `MAX_STDERR_BYTES=1024`. The TERM-ignoring fixture
waited 0.05 seconds after spawn so its single process installed `SIG_IGN`
before the shortened deadline. Child commands otherwise received the
adapter's exact clean environment. No endpoint, Go build, or network traffic
was used.

## Attempt 1

The one authorized execution returned direct wrapper status 0 and emitted the
marker `ADAPTER_CONTRACT_ATTEMPT_1_STATUS=0`. Its redirected stdout and stderr
files are both empty. `adapter-contracts-1/summary.json` reports
`accepted:true`, 14 cases, and 14 direct case-child statuses of 0.

- Natural completion: parser and canonicalizer both returned 0, joined, and
  had absent process groups without TERM or KILL.
- Signal during parser registration and signal during canonicalizer
  registration: both owned children returned -15, joined, and had absent
  groups. The adapter retained signal 15, returned 1, and rejected the result.
  A second signal during status publication did not prevent complete evidence.
- TERM-ignoring deadline: both children recorded TERM and KILL attempts,
  returned -9, joined, and had absent groups. The adapter retained the timeout
  and returned 1.
- Ordinary deadline: both children recorded TERM, returned -15, joined, and
  had absent groups without KILL.
- Second-child startup failure: the parser recorded TERM, returned -15,
  joined, and had an absent group. The canonicalizer retained PID null and
  status 127. The startup error was retained and the adapter returned 1.
- Stderr overflow: both children naturally returned 0, the parser retained
  exactly the 1024-byte cap with `stderr_overflow:true`, and both generated
  artifacts were recorded then removed. The adapter returned 1.
- Parser nonzero: parser status 7 and canonicalizer status 0 were retained;
  both joined naturally with absent groups, and both partial generated
  artifacts were recorded then removed. The adapter returned 1.
- Empty, oversized, and nonregular raw inputs; pinned Go hash mismatch; unsafe
  working-directory mode; and duplicate output paths all rejected before any
  child spawn, process record, or adapter output.

Each case's invocation, direct child status, stdout, stderr, complete result,
and adapter-owned sidecars are preserved under `runs/adapter-contracts-1/`.
The exact outer command is preserved as `runs/run-attempt-1.sh`; its preflight
hashes are in `runs/attempt-1.preflight-sha256.txt`.

The post-run process listing contained no fixture child. This observation is
secondary to the adapter's per-child `joined:true` and `group_gone:true`
records for every child that started.

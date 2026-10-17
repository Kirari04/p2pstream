# TCP-state diagnostic attempt history

## Source and validator history

- Integration checkpoints 1–3 are preserved superseded designs. Checkpoint 4
  was announced frozen and then its manifest/diff metadata was rewritten in
  place. It is permanently rejected. The incident and append-only erratum bind
  the initially declared `3de07e...` manifest, observed later bytes, and clean
  successor lineage without treating the rewritten checkpoint as usable.
- Checkpoint 5 recreated the reviewed payload in a fresh root with clean
  provenance. Checkpoint 6 changed one test expectation only: close during an
  active raw write stays incomplete and sticky invalid. Its runtime/schema
  payload is identical to checkpoint 5 and it is the accepted build input.
- Validator checkpoints 1–2 record the field-level corrections. Checkpoint 3
  is retained with its withdrawn checkpoint-4 producer binding. Checkpoint 4
  binds clean producer checkpoint 5; its schema remains valid for checkpoint 6
  because the latter is test-only.
- The bounded validator run accepted exactly 25 named cases, with direct and
  outer status zero and natural owned-group closure. It performed no Go build,
  endpoint execution, or traffic.

## Audited build history

- Build attempt 1 failed in the tagged agent fixture because the test expected
  a completed snapshot after closing during an active raw write. The runtime
  correctly kept the record incomplete and invalid. No binaries or traffic
  resulted. Its nonzero status and complete execution records remain primary
  evidence.
- Build attempt 2 used integration checkpoint 6 and passed. All selected Go
  list, ordinary compile, focused normal/race tests, binary builds and source
  postchecks completed with natural group ownership. The accepted receipt is
  `bcca6079b5de32635701b4a1d5195cd9b9f9e8976d76e48956cea7d396cb7b97`.
  Binary bytes are omitted and retained by SHA-256 only.

## Runner, materialization and contract history

- Runner source 1 preserved the mature generated workload and exact timed
  `run_transfer` body, added TCP-state validation, and retained all byte-credit,
  raw-write, server-growth, TLS, correctness and lifecycle gates. Its first
  materialization/publication completed. Final contract attempt 1 failed before
  child execution because the output parent directory did not exist. Attempt 2
  then rejected the stale expected provenance cardinality 86 versus actual 93.
- Runner source 2 changed only that contract cardinality to 93. Its fresh
  materialization/publication completed. Final contract attempt 3 rejected two
  test assertions that pointed at the wrong source files; the actual 4 MiB
  bounded read and omission fields were present in the generated runner and
  producer source.
- Runner source 3 changed only those test locations. Its fresh materialization
  and publication completed. Final contract attempt 4 accepted TCP source
  checks 11/11, source bindings 12/12, self-gates 19/19 and exact producer and
  validator key sets 93/93, with direct status zero and natural closure.
- The generated qualification runner remains
  `a672ab078d424a59d795c522a40f96da815b8aaa55af6f21fce4eed07a63b1ad`;
  the timed body remains
  `b86593361c76c3d2903d722b1b0292fa1bc833210d5efb2438dea1aac08ad798`.

## WAN execution

The tiny run completed 16 H1 and 16 H2 rows with both arm attestations accepted.
It observed 212 H1 and 196 H2 provider capture calls, exact five-owner terminal
closure per arm, and maximum authenticated response sizes 196,419 and 198,534
bytes. All five owned processes per arm exited naturally. The run is a 4 MiB
schema/join/lifecycle smoke and carries `performance_result:false`; it clears no
throughput, parity, release, or merge gate.

The sustained process closed once with native status 1 and no retry. Its
immutable launcher is `5d933f62...`, source preflight `22563d39...`, full run
ledger `2d17e16e...` (271 files / 28,213,160 bytes), review ledger
`a39ed1f3...`, and outer envelope `3bcdba2f...`. Integrity and all five owned
process exits per arm passed naturally with no forced cleanup. H1 was accepted:
all reuse expectations and fixed download floors passed. H2 was rejected as
`inconclusive-upstream-reuse`: retained-warmup and steady floors failed, while
sequences 15 and 16 expired expected origin reuse after 35.149 and 35.487
seconds. Conditioning-clean H2 sequence 14 retained the same origin after
28.478 seconds and measured 16.810103 MB/s. The nonzero outer status preserves
the cohort rejection; it is not an integrity or lifecycle failure.

The subsequent read-only kernel/Yamux source review and its append-only erratum
are preserved beside exact official-source excerpts and fetch/hash commands.
The correction rejects DATA-only transition sampling and namespace-counter
owner attribution. It is future diagnostic design evidence, not an executed
change, runtime fix, or traffic authorization.

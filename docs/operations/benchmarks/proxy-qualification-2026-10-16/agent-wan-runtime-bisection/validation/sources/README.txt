Runtime-bisection bounded validation sources

- external-smoke-b2c987b6.sh is the byte-for-byte final executed adapter.
- external-smoke-34a29763-reconstructed.sh reconstructs the first executed adapter by reversing only the resource-envelope prefix correction. Its SHA-256 is verified below against the contemporaneously recorded value. The first run stopped after the current download because event statistics are intentionally emitted only for p2pstream-prefixed envelopes; it is an instrumentation failure, not a runtime result.
- synthetic96-summary-analyzer.py is extracted byte-for-byte from the runner's final summary heredoc.
- synthetic96-pass.jsonl is the generated 96-row passing fixture; the missing-hash, malformed-hash, wrong-order and conservation mutations remain beside this source directory.
- runner-stop-process-exact.sh is the exact stop_process function extracted from the runner.
- process-group-validation.sh covers graceful exit, forced termination rejection and child-survives-parent group cleanup.

Executed validation commands and output are represented by ../static.log, ../synthetic.log, ../process-groups-final.log, ../wrapper-preflight-v2.log, ../wrapper-negative.log, ../external-smoke.stdout.log, ../external-smoke.stderr.log, ../external-smoke-v2.stdout.log and ../external-smoke-v2.stderr.log. The wrapper preflight snapshots the exact runner, wrapper, overlay, build provenance and structured build manifest under ../wrapper-preflight-v2/snapshot/.

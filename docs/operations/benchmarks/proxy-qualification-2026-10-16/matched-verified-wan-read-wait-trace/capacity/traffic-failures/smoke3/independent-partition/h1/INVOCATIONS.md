# Invocation record

Working directory for every command:

`/home/user/.codex/worktrees/astra-proxy-performance/p2pstream`

The final audit invocation was:

```text
python3 tmp/continuation-20261016/wan-read-wait-capacity-source/review/h1-smoke3-handler-partition-independent-1/audit_handler_partitions.py --index tmp/continuation-20261016/wan-read-wait-capacity-source/runs/tiny-read-wait-capacity-smoke-3/h1/read-wait-reductions/index.json --output tmp/continuation-20261016/wan-read-wait-capacity-source/review/h1-smoke3-handler-partition-independent-1/audit.json
```

Direct process status: `0`. Stdout and stderr were empty.

An earlier invocation of that same Python command exited `1` before creating
`audit.json`. The initial parser assumed every `StateTransition` resource was a
goroutine and stopped at the first non-Go transition with:

```text
ValueError: expected one GoID= field, got []
```

The final source keeps only StateTransition records that actually carry a
Resource `GoID`, while still checking global event ordering. This is a parser
scope correction; no acceptance gate was removed for handler goroutine
transitions. The failed status is retained in `attempt-1.status`, and the
successful status in `attempt-2.status`.

A preceding composite cleanup-plus-audit shell command was rejected by the
execution wrapper before launch because it contained `rm -rf`. It did not run
the audit or alter the closed inputs.


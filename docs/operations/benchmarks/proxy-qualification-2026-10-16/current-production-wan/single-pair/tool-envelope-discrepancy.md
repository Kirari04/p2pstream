# Tool-envelope status discrepancy

The sole traffic attempt was launched by one `exec_command` shell whose body was:

```sh
set -uo pipefail
prep=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/continuation-20261016/current-wan-prep
run=$prep/runs/original-h2-pair-1
mkdir -p "$prep/runs"
start_utc=$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)
printf '%s\n' "$start_utc" > "$prep/runs/original-h2-pair-1.start.utc"
set +e
"$prep/runner/run-owned-wan-pair.sh" --output-dir "$run"
status=$?
set -e
end_utc=$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)
printf '%s\n' "$end_utc" > "$prep/runs/original-h2-pair-1.end.utc"
printf '%s\n' "$status" > "$prep/runs/original-h2-pair-1.supervisor.status"
exit "$status"
```

The initial tool response returned session ID `58099`. Four later empty-input
`write_stdin` calls reported the session still running. The fifth and final
empty-input call returned `exit_code: 1` with no stdout or stderr.

This conflicts with every status captured inside the shell and supervisor:

- sibling `original-h2-pair-1.supervisor.status`: `0`;
- run `runner.status`: `0`;
- run `validation.status`: `0`;
- supervisor lifecycle: runner status `0`, joined `true`, forced `false`;
- all seven endpoint/origin/namespace exits: status `0`, natural and joined;
- runner and validation stderr: empty.

There was no command after `exit "$status"`, and the recorded `status` was `0`.
No shell-level nonzero command explains the tool envelope's `1`. The discrepancy
is retained without retry and does not alter the immutable raw result.

# Validation attempt 1 diagnosis

Attempt 1 is preserved as a harness materialization failure. It is not an
observer compile or contract result.

The source-preparation shell ran `git archive 8ac4c8d...` while its current
working directory was the untracked/ignored path
`tmp/continuation-20261016/wan-sender-wait-source`. Git applied that current
prefix to the archive operation; because the prefix does not exist in the
pinned tree, both extracted archive roots were empty. The candidate overlay
then added only the 12 changed checkpoint files. There was no root `go.mod`,
Yamux `go.mod`, or unchanged Yamux source. Consequently `go test` selected the
ambient parent module path and failed to compile with `undefined: Config`,
`Logger`, `header`, and related errors.

Direct statuses are `yamux-normal=1` and focused outer `1`; no later command
ran. The candidate's relative post-attempt file hashes exactly equal the
checkpoint overlay hashes (`outputs/candidate-vs-checkpoint.status` is 0).
The earlier absolute-path pre/post hash-list `cmp` status 1 is a comparison
method artifact: one list stored paths prefixed by `validation-attempt-1/` and
the other stored paths relative to the attempt root; the displayed content
hashes are unchanged.

A corrected materialization, if separately authorized, must execute
`git -C /home/user/.codex/worktrees/astra-proxy-performance/p2pstream archive
8ac4c8df...` from the repository root explicitly, require nonempty `go.mod`,
`third_party/yamux/go.mod`, and an exact pinned-tree manifest before overlay,
then copy and verify the same immutable cp3 overlay. It must use a fresh
attempt root and may not overwrite or reinterpret this failed attempt.

# Tiny capacity smoke 1: selected-memory-source failure

This is an immutable offline explanation of the failed diagnostic attempt at
`runs/tiny-read-wait-capacity-smoke-1`. It does not revise the run's raw record,
statuses, or `accepted:false` result.

The H1 arm completed its eight conditioning transfers. Before starting the
sequence-9 execution trace, the authenticated server-capacity query succeeded
and returned an adaptive, healthy, non-rejecting snapshot with an empty sample
error. Its selected usage was:

- source: `host`
- used bytes: `6948503552`
- limit bytes: `32309121024`
- headroom to the minimum hard boundary: `3813602918`

The independent server-leaf snapshot simultaneously reported the expected
4 GiB `memory.max`, zero swap, and zero high/max/OOM events. The runner's exact
pre-health predicate nevertheless required `memory_source == "cgroup_v2"`, so
`pre_healthy_status` was 1. No trace Start request was issued. The inner runner
then emitted five forced cleanup rows, all with TERM status 0, wait status 143,
no KILL, and absent final process groups. The outer supervisor and delegated
scope joined naturally. H2 was not started.

The production source explains why `host` is valid. `MemoryUsage.Valid` accepts
nonnegative used bytes and a positive limit (`internal/sysmetrics/memory_pressure.go:45-59`).
The system sampler includes finite cgroup candidates, always includes the host
candidate, and optionally includes the finite Go limit (`:554-608`). The only
emitted source values are `cgroup_v2` and `cgroup_v1` (`:735-745`), `host`
(`:943-951`), and `go` (`:598-606`). The controller selects the candidate with
the highest usage percentage while independently retaining minimum hard-limit
headroom across all candidates (`:394-412`). Therefore selected manager usage
need not be the 4 GiB server leaf. The independent leaf-cgroup gate remains the
proof of the diagnostic server's exact 4 GiB/no-swap environment.

The referenced production file SHA-256 is
`b59b818e5ab968f4a97b60603a8a9ec6978562c48e5e48fd6c9828ab89fb9972`.

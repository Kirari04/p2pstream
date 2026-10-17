# Rejected unselected-shaper allocation guard

The current direct-path profile attributed 111.54 MiB of flat allocation to
passing an unselected traffic-shaper decision into the owned-copy helper. This
experiment moved the selection check to the caller and retained the original
helper for selected decisions. It changed only the three-line call site in
`public_proxy_pipeline.go`; policy ordering and selected-decision ownership
were preserved. The production worktree file was never modified.

The candidate used a clean `3ec137c3ad3d07028e867042fb00d3933912d7ab` archive
with only `candidate.patch`. The baseline and driver used the same frozen
3ec137c executable as the contemporary direct-path profiles. Both used the
same frozen harness and runner, native TLS/H1, tiny verified GET responses,
full policy/recorder path, four logical CPUs on two physical cores (0–3),
load/origin CPUs 4–15, GOMAXPROCS=4, and a 4 GiB/no-swap candidate cgroup.
The HAProxy control remained a host process. Each of three alternating
baseline/candidate pairs used a two-second warmup and ten-second timed interval
at 128 clients, followed by HAProxy. Root owned exclusive benchmark CPU after
direct source-only acknowledgments from both workers and Astra.

| Metric | Baseline median | Guard median | Change |
| --- | ---: | ---: | ---: |
| Useful requests/s | 25,901.34 | 26,189.58 | +1.11% |
| p95 ms | 11.439 | 11.406 | -0.29% |
| p99 ms | 16.629 | 16.439 | -1.14% |

Paired throughput changes were -1.59%, +1.04%, and +1.68%; paired p99 changes
were +1.26%, -3.14%, and +0.75%. The mixed direction and small median change do
not establish a repeatable sustained benefit. The predeclared retention rule
required request-level improvement; an allocation-only result was insufficient.
Astra and root therefore rejected the production overlay without further
sampling to chase a favorable result.

All nine timed cells completed their expected status/protocol/payload checks.
The six production cells persisted every timed event, with zero client errors,
drops, recorder backlog, or drain/query errors. Candidate and baseline completed
790,618 and 787,775 verified timed responses respectively. Existing selected
shaping, WebSocket, fail-closed policy and context-lifetime regressions passed
normal and race tests, each repeated three times.

A separate diagnostic allocation profile showed zero flat allocation at the
active-stage guarded call site, confirming the narrow allocation effect. Its
timing is excluded from the paired throughput decision. The earlier baseline
profile and this diagnostic have different request counts, so their total
allocation volumes are not a paired per-request comparison. The original
baseline listing is preserved in `../post-batch-profile/active-stage-alloc-list.txt`.

`provenance.json` records executable, overlay, harness and runner hashes,
orders, local raw/profile directories and timestamps. `run-experiment.sh` is
the exact local command script, including its original absolute paths; replay
requires adapting those paths to clean archives at the recorded source. No
profile binaries, databases, keys or executable artifacts are checked in.

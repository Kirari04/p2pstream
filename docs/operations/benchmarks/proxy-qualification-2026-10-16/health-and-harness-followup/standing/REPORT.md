# Independent standing attempt 1 — passed

Immutable source: `27c8e31f3826ab5326d941b20644c1f56159d99d`. This cohort was separately authorized after the capacity comparison failed during the legacy baseline's shutdown. Capacity remains rejected; its raw evidence is unchanged. This standing result is not a capacity retry, capacity continuation, or successful capacity result.

The wrapper ran exactly one 16-agent cohort followed by one 32-agent cohort using the already-built candidate server and agent binaries from capacity-attempt-1/builds. The server binary also served as load/origin driver. Both binary SHA256s still match capacity manifest.json. The clean-archive manifest verifies the final candidate source tree and binds the executable inputs; there was no rebuild or source mutation between stages. Exact commands, separate authorization and launch/exit statuses are recorded in the parent launches.jsonl. Toolchain is the cached Go 1.26.6 binary directory with GOTOOLCHAIN=local, CGO_ENABLED=1 and GOMAXPROCS=4.

Both cohorts produced exactly one result row, with the exact fixed 19-key acceptance set and every value true. Each agent established one control plus four aggregate bulk sessions. Initial/final samples confirm exact fixed session promises, stable session/cgroup identities, idle owners before/after health, separate readable 4 GiB/no-swap server/agent envelopes, and zero OOM deltas. There were exactly 16 and 32 reserved health roundtrips with every requested agent index represented once.

| Agents | Health roundtrips | Acceptance fields true | Natural exit receipts | Server session resource bytes | Agent session resource bytes |
|---:|---:|---:|---:|---:|---:|
| 16 | 16 | 19/19 | 19/19 | 231251968 | 374382592 |
| 32 | 32 | 19/19 | 35/35 | 462503936 | 748765184 |

Wrapper and both runner statuses are zero; runner emergency_cleanup is false. All 54 process receipts explicitly record forced_cleanup=false, shell_exit_status=0 and natural_exit_verified=true. Every constrained-container receipt records Running=false, ExitCode=0 and OOMKilled=false. Receipts cover server, all agents, host origin and HAProxy for each cohort. Final read-only audit found none of the recorded endpoint/client PIDs and no qualification containers remaining. The independent audit manifest records every receipt, its SHA256, reused binaries, source/frozen-runner hashes and cohort identity/count checks.

No source changes, commits, pushes, automatic reruns or weakened gates occurred. This result establishes the two standing-pool cohorts only; it does not establish current candidate capacity, WAN behavior, grown-pool pressure, or throughput/performance parity.

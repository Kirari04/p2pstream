# Remote-agent H2 capacity with one established connection

This separate finite diagnostic confirms 3,072 simultaneous held origin
requests twice, with 1 GiB server ballast, through the actual public HTTPS
listener and one real remote agent. Server and agent have distinct 4 GiB/no-swap
cgroups, independent production ledgers and four bulk lanes. It does not claim
that both processes fit in one 4 GiB deployment.

The immutable source is `e92162962e7bebe9e5a8d28a2efe6f9373ea9db9`. Compared
with the earlier cold-client `fb1cad92` cohort, no non-test Go production file
differs. Both test binaries come from a clean archive, with exact hashes and
toolchain in `manifest.json` and `build-provenance.txt`.

Before each capacity wave, the same client transport completes exactly one
HTTP/2 GET to the controlled delay target, without the concurrency barrier. The
response must be HTTP/2 204 with an empty body. Its single request event must
persist, with no loss or backlog, before the measured event baseline is taken.
The prewarm request is excluded from the 3,072 measured requests. The origin
barrier and all original capacity, ownership, response, event, OOM and recovery
checks remain unchanged.

| Measurement | First pass | Confirmation |
| --- | ---: | ---: |
| useful / attempted | 3,072 / 3,072 | 3,072 / 3,072 |
| simultaneous origin / request owners | 3,072 / 3,072 | 3,072 / 3,072 |
| separate prewarm requests / persisted events | 1 / 1 | 1 / 1 |
| public connection-owner peak | 198 | 827 |
| TCP connections used by measured requests | 16 | 23 |
| sampled server cgroup peak | 1,340,256,256 B | 1,354,825,728 B |
| sampled agent cgroup peak | 136,630,272 B | 135,393,280 B |
| subsequent useful recovery / events | 512 / 512 | 512 / 512 |

Both capacity rows persist exactly 3,072 measured events, with zero client
errors, admission misses, agent rejections, recorder drops/backlog and readable
zero OOM/OOM-kill deltas. Each recovery row also conserves all 512 events.
Resource peaks are sampled per process and must not be added as simultaneous
deployment peaks. Immediate snapshots retain 3,072 warm origin stream leases;
this diagnostic establishes functional recovery, not their idle release time.

One established H2 connection does **not** mean the entire connection pool is
warm. Surplus TLS connection attempts remain visible, particularly in the
confirmation's 827 accepted public owners versus 23 request-used TCP
connections. The result therefore does not prove that prewarming eliminates
dial amplification or explain the earlier 4,096 instability causally.

The predeclared follow-up tried 3,072 plus one confirmation, with 2,048 plus a
confirmation available only as fallback. Both 3,072 points pass, so no fallback
or new 4,096 attempt runs. This confirms the tested level, not an exact maximum
or universal H2 capacity. The original cold-client 4,096 pass/fail pair remains
unchanged and unqualified in the
[cold boundary evidence](../capacity-boundaries-cold/README.md).

`points/` preserves every invocation, decision, JSONL row and runner log,
including direct HAProxy controls. The HAProxy processes have no matching
ballast/cgroup or agent hop and are not a memory-capacity parity gate. The
copied runners and fixture sources use `.txt` suffixes; `SHA256SUMS` covers the
checked-in evidence. This held-empty-response workload does not qualify large
bodies, hostile headers, sustained throughput or broad proxy parity.

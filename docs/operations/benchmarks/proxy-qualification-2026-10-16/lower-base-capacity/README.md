# Lower-base constrained capacity qualification

This directory preserves the finite constrained-capacity cohort run on 16
October 2026. The candidate was built from clean commit
`1e52b45ee7a5db8cac21168e9a5f7d6e5235531a`; the historical control was built
from `3ec137c3ad3d07028e867042fb00d3933912d7ab` plus the two frozen external-role
overlays copied here. `manifest.json` records the exact source, harness and
binary hashes, toolchain, kernel, HAProxy version and logical-CPU topology.

The candidate passed the original workload gate in all three constrained
topologies. Each row established 1,536 simultaneous H1 origin requests, held
the released barrier for 200 ms so the 20 ms owner sampler could see the live
wave, completed 1,536 valid 2xx responses with no client or admission errors,
persisted exactly 1,536 request events, and recorded readable zero OOM and
OOM-kill deltas. A subsequent 512-request functional recovery row also
completed and conserved its events.

| Topology | Candidate useful | Origin peak | Server cgroup peak | Server socket peak | Historical useful/errors |
| --- | ---: | ---: | ---: | ---: | ---: |
| combined server + colocated agent, one shared 4 GiB cgroup | 1,536 | 1,536 | 1,391,091,712 B | 5,128,192 B | 1,171 / 365 |
| server 4 GiB + one remote-agent process 4 GiB | 1,536 | 1,536 | 1,322,135,552 B | 4,845,568 B | 1,286 / 250 |
| server 4 GiB + four remote-agent processes, each 4 GiB | 1,536 | 1,536 | 1,330,044,928 B | 5,378,048 B | 1,433 / 103 |

Every server row used a 1 GiB server ballast. Agent processes had no ballast.
The combined fixture samples one process and cgroup but still runs independent
server and agent admission controllers and reservation ledgers. The separate
topologies use distinct 4 GiB cgroups; their process peaks are not summed as if
they occurred at one instant and do not claim that this deployment fits one 4
GiB machine.

The historical control reached origin peaks of 1,161 in the combined topology
and 1,177 in each external topology. Its failures were classified as
agent-stream resource-pressure rejections. The candidate recorded zero such
rejections. These are held-tiny-request capacity results, not throughput or
large-body memory-parity claims.

Immediate post-wave external-agent samples retained all stream reservations:
1,536 on the one-agent process and 384 on each of the four agent processes.
The 512-request row therefore proves functional and recorder recovery, but it
does not prove prompt stream-reservation release. Retained FDs, RSS and current
cgroup values remain in the raw rows for follow-up lifecycle qualification.

The current agent also passed two old-server fallback bursts. One and four
configured legacy physical lanes each completed 128 simultaneous held opens
and the 512-request recovery row without errors. Admin telemetry observed zero
and three live bulk lanes respectively; the lower-base legacy session envelope
was 181,586,560 bytes for one lane and 726,346,240 bytes for four, an exact
1:4 ratio.

`summary.json` contains the strict gate decision and full candidate rows.
`results/` retains all baseline, candidate and HAProxy JSONL rows. The copied
runner, orchestrator and overlays are the exact inputs identified by the
manifest. `SHA256SUMS` covers the checked-in evidence.

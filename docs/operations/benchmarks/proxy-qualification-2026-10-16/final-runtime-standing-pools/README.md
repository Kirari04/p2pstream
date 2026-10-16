# Final-runtime standing aggregate pools

This bounded `dc738e9000e60cd7ce3a0279127486f3bc05067a` cohort measures
16 and 32 idle external aggregate agents. The server and every agent run in a
distinct 4 GiB cgroup with swap disabled. Each agent establishes one negotiated
control lane and four aggregate bulk lanes. The runner verifies exact lane and
session identities before and after a real health roundtrip to every agent,
zero active stream/control owners after bounded cleanup, exact fixed session
promises, stable cgroup identities, readable memory/FD/runtime snapshots and
zero OOM deltas.

Both cohorts pass all gates.

| Agents | Server session promise | Agent session promises | Initial simultaneous cgroup current | Final simultaneous cgroup current |
| ---: | ---: | ---: | ---: | ---: |
| 16 | 231,251,968 B | 374,382,592 B | 170,983,424 B | 181,481,472 B |
| 32 | 462,503,936 B | 748,765,184 B | 318,763,008 B | 335,626,240 B |

The simultaneous sums use one bounded sampling pass; they do not sum
independently observed peaks. In the 32-agent final sample, server RSS was
78,802,944 bytes (75.15 MiB) and median agent RSS was 21,387,264 bytes
(20.40 MiB). The 335,626,240-byte (320.08 MiB) cgroup-current sum has cgroup
and shared-page accounting semantics and is not interchangeable with RSS.

The agent runtime can report `memory_source:"host"` because that field names
the constraint with the highest usage percentage. Admission independently uses
the minimum absolute headroom across all constraints. Across all 48 agent
initial/final samples, the reported allowance matched the 4 GiB leaf-cgroup
headroom calculation within -2 to +1 slots, consistent with the 100 ms sampler
interval and integer rounding; a host-only calculation would have produced
roughly 7,000 slots rather than about 4,873. This is a diagnostic-field
interpretation, not a sampler fix.

The result qualifies kernel-constrained idle scaling with a separate 4 GiB
allowance for every process. It does not show that a server plus 32 agents fit
inside one 4 GiB deployment envelope, and it does not sum nonsimultaneous
process peaks. `standing-16.jsonl` and `standing-32.jsonl` retain the complete
process, lane, promise, cgroup and health records. The saved wrapper and runner
are the exact executed snapshots.

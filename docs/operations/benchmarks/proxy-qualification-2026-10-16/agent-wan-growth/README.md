# Aggregate agent WAN growth and large-transfer regression

This bounded namespace cohort compares the aggregate agent in candidate
`e2df1b110283886f529b438f549a8da32a1fd3ce` with a frozen
`3ec137c3ad3d07028e867042fb00d3933912d7ab` legacy agent and its checked-in
external-role overlay. Both use the same current server binary, TLS public
listener, origin, load driver and shaped path. The comparison isolates the
negotiated transport more closely than a full historical server/agent pair,
but it still includes the four-physical-lane legacy topology versus one
control plus four aggregate bulk lanes. It is not a HAProxy comparison.

The routed path was configured for 80 ms RTT and 1 Gbit/s; five pings measured
96.547 ms RTT. Server, agent/origin and load generation used disjoint logical
CPU sets. CPUs 0-3 and 4-7 each comprise two physical cores with two SMT
siblings per core.

Before timed transfers, the runner makes four fresh 256 MiB downloads and then
four fresh 256 MiB uploads, closing only idle origin transports between each
request. It refuses to begin timing until every server receiving lane and every
agent receiving lane exceeds 8 MiB and is quiescent: receive availability
matches its limit, every allocation unit is free, and buffered and pending
refund values are zero. It then runs one retained-origin-transport 256 MiB
warmup in each direction. Growth and retained warmup rows are preserved but
excluded from the throughput summary.

The timed workload uses three 256 MiB transfers per direction rather than the
runner's 1 GiB default. This keeps the complete all-lane warmup plus repeated
comparison bounded. The observed ratios were not near the 90% gate and sample
variation was modest, so no selectively chosen 1 GiB confirmation was added.

| Direction | Legacy median | Aggregate median | Aggregate / legacy | Aggregate sample range |
| --- | ---: | ---: | ---: | ---: |
| Download | 34,336,677 B/s | 33,083,273 B/s | 96.35% | 33,037,412-34,652,568 B/s |
| Upload | 35,734,270 B/s | 36,003,184 B/s | 100.75% | 35,150,331-37,071,222 B/s |

Every timed download observed at least 90,701,824 bytes (86.5 MiB) on every
server receiving lane. Every timed upload observed at least 95,944,704 bytes
(91.5 MiB) on every agent receiving lane. Final pool snapshots were fully
available with zero buffered units and zero pending refunds. Every transfer
also passed its incremental payload hash/length check and persisted exactly
one event with no drop, queue residue or drain error.

These are large-transfer results with prewarmed session pools. Later requests
can still include per-stream byte-window ramp because origin transports rotate
and have a 30-second idle lifetime; the result is not labeled a fully warmed
per-stream ceiling. The run used one cohort order, legacy then aggregate, with
three direction-level repetitions. It does not exercise cgroup pressure. The
roughly 90-172 MiB retained server pools and 91-102 MiB retained agent pools
make idle pressure and recovery a separate required qualification before any
pool-reclamation decision.

`results.jsonl` contains all 32 machine-readable growth, retained-warmup and
timed rows. `summary.json` contains the median gate. `manifest.json` records
source, binary, overlay, runner, CPU, path and workload provenance.

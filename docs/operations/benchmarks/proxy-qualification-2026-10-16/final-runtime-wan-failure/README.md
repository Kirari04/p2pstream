# Final-runtime WAN large-transfer failure

This is the unchanged failed large-transfer cohort for final runtime
`dc738e9000e60cd7ce3a0279127486f3bc05067a`. It compares the current
aggregate agent with the frozen `3ec137c3ad3d07028e867042fb00d3933912d7ab`
compatible legacy agent through the same current server, TLS public listener,
origin, load driver and namespace path configured for 80 ms RTT and 1 Gbit/s.
Five pings measured 96.335 ms.

The bounded cohort used four 256 MiB lane-growth transfers per direction, one
retained-origin-transport 256 MiB transfer per direction, and three timed
256 MiB transfers per direction. All receiving lanes exceeded the declared
8 MiB threshold and were quiescent before timed work. Every one of the 32
transfers passed payload length/hash and event-conservation checks with zero
client errors, drops or drain errors.

The predeclared aggregate-versus-legacy useful-throughput gate is 90%. Upload
passes; download fails.

| Direction | Legacy median | Aggregate median | Aggregate / legacy | Aggregate samples |
| --- | ---: | ---: | ---: | ---: |
| Download | 34,030,177 B/s | 27,406,061 B/s | 80.53% | 27,350,766; 27,406,061; 34,622,121 B/s |
| Upload | 35,424,402 B/s | 36,025,803 B/s | 101.70% | 34,806,631; 36,025,803; 36,530,495 B/s |

The runner exited nonzero at the download gate before writing its normal
summary. `results.jsonl` is the complete original output and remains decisive.
`derived-summary.json` was generated afterward to make the failed gate and
diagnostic fields reviewable; it does not alter the rows or convert the failed
exit into a pass.

The aggregate retained download ran at 11.87 MB/s. Timed downloads then ran at
27.35, 27.41 and 34.62 MB/s. The slow rows show no server CPU, GC, FD or socket
memory saturation, while final receive-pool snapshots are fully available.
Those after-row snapshots cannot exclude transient stream/session credit
starvation. The four physical bulk TCP lanes are also long-lived under CUBIC
with `net.ipv4.tcp_slow_start_after_idle=1`; the runner warms every download
lane before all upload lanes, leaving the agent-to-server direction idle for
tens of seconds before measurement. Lane-specific TCP warm state is therefore
a real confounder, not a finding that the aggregate credit path alone caused
the result.

This cohort is preserved without a selective retry. A separate diagnostic must
map every Yamux lane to its physical socket and sample TCP congestion/window,
qdisc and in-flight Yamux byte/allocation-credit state before any production
change or acceptance rerun. The prior passing WAN cohort remains available at
[`../agent-wan-growth/README.md`](../agent-wan-growth/README.md); it does not
override this final-runtime failure.

# Final-runtime WAN stability failure

This is the predeclared three-pair stability check for production runtime
`dc738e9000e60cd7ce3a0279127486f3bc05067a`. It follows the unchanged
[final-runtime WAN failure](../final-runtime-wan-failure/README.md) and uses
the same current server, aggregate agent, frozen `3ec137c` compatible legacy
agent, TLS public listener, origin and network-namespace path. The configured
path is 80 ms / 1 Gbit/s; the runner recorded 96.122 ms RTT.

Each fresh endpoint pair ran the original frozen workload with four 256 MiB
receiving-pool warmups per direction, one retained-transport 256 MiB warmup per
direction, and three timed 256 MiB transfers per direction. Pair order was
legacy then aggregate, aggregate then legacy, legacy then aggregate. The gate
was fixed before execution: every pair's aggregate/legacy median must be at
least 90% in each direction. All 96 transfers were retained; there were no
selective retries.

The check fails. Download misses the gate independently in all three pairs;
upload passes in all three.

| Pair | Order | Legacy download median | Aggregate download median | Download ratio | Upload ratio |
| ---: | --- | ---: | ---: | ---: | ---: |
| 1 | legacy, aggregate | 33,951,622 B/s | 27,297,446 B/s | 80.40% | 100.51% |
| 2 | aggregate, legacy | 33,630,338 B/s | 27,445,170 B/s | 81.61% | 98.26% |
| 3 | legacy, aggregate | 34,269,685 B/s | 27,320,208 B/s | 79.72% | 98.33% |

The pooled diagnostic ratio is 80.47% download and 98.35% upload. Aggregate
download samples preserve the repeated within-endpoint shape:

- pair 1: 15,055,887; 27,297,446; 34,670,213 B/s;
- pair 2: 14,250,363; 27,445,170; 34,357,353 B/s;
- pair 3: 14,306,704; 27,320,208; 34,363,684 B/s.

The fixed request order rotates the first timed download through a newly used
download direction on bulk lane 1, the second through the first download use
of lane 2, and the third through a second download use of lane 3. Upload use of
a lane does not establish its directional download credit. This sequence is a
diagnostic correlation, not a causal attribution. A separate frozen-binary
[TCP/profile diagnostic](../agent-wan-tcp-diagnostic/README.md) did not
reproduce the stall and remains non-acceptance evidence. The next bounded
diagnostic samples test-only sender byte/allocation credit and the physical TCP
tuple without native profiling or production policy changes.

Correctness and conservation remain exact: 96 attempted transfers produced 96
payload-verified HTTP 200 responses and 96 persisted events, with zero client
errors. All receiving-pool gates passed. The runner and wrapper both exited
nonzero because the download timing gate failed. `results.jsonl` is the
complete raw result; `summary.json` is the wrapper's predeclared pair and
pooled calculation.

## Provenance and replay

`manifest.json` records the exact source and binary hashes. The current server
and aggregate agent binaries are from `dc738e9`; the compatible legacy agent
is from `3ec137c` plus the previously preserved qualification overlay. The
saved runner and wrapper are byte-for-byte copies of the executed scripts.

After rebuilding those exact source trees and overlays into executable
`server.test`, aggregate `agent.test`, and legacy `agent.test` binaries, replay
from a checkout root with:

```sh
mkdir -p /tmp/p2p-wan-stability-replay
cp qualify-proxy-agent-growth.sh.txt /tmp/p2p-wan-stability-replay/qualify-proxy-agent-growth.sh
cp qualify-proxy-agent-growth-stability.sh.txt /tmp/p2p-wan-stability-replay/qualify-proxy-agent-growth-stability.sh
chmod +x /tmp/p2p-wan-stability-replay/qualify-proxy-agent-growth*.sh
/tmp/p2p-wan-stability-replay/qualify-proxy-agent-growth-stability.sh \
  --runner /tmp/p2p-wan-stability-replay/qualify-proxy-agent-growth.sh \
  --server-binary /absolute/path/to/server.test \
  --aggregate-agent-binary /absolute/path/to/aggregate/agent.test \
  --legacy-agent-binary /absolute/path/to/legacy/agent.test \
  --server-source-ref dc738e9000e60cd7ce3a0279127486f3bc05067a \
  --aggregate-agent-source-ref dc738e9000e60cd7ce3a0279127486f3bc05067a \
  --legacy-agent-source-ref 3ec137c3ad3d07028e867042fb00d3933912d7ab \
  --output-dir /tmp/p2p-wan-stability-replay/result
```

The saved wrapper supplies the immutable `--runs 3`, 256 MiB, 8 MiB pool and
80 ms path arguments and verifies all pair gates. The original nonzero outcome
is expected until the download regression is resolved.

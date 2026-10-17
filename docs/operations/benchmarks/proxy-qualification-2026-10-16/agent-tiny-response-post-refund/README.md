# Post-refund aggregate tiny-response qualification

This cohort repeats the exact low-latency tiny-response comparison from
`agent-tiny-response` after the combined candidate checkpoint
`805a8ea9313336908570d3495d97509e342248c9`. That checkpoint includes the
paired aggregate-credit refund in `25018f9550095123dbeea9a263e4d6a46cbb8c9c`
and the subsequent shutdown-waiter correction. It uses the same frozen
`3ec137c` legacy agent, current server, public TLS listener, 256-byte verified
origin response, load driver, CPU sets and predeclared gates as the first
cohort. The routed path was configured for 1 ms RTT as 500 us on each path half
and measured 1.361 ms over five pings.

The qualification still fails. All 3,547,693 attempted requests completed with
the expected payload, status and HTTP version, and all 3,547,693 events
persisted. No event was dropped, no queue residue remained after drain, and no
drain error occurred. The nonzero exit preserves the remaining small-frame
transport regression.

| Workload | Legacy median req/s | Aggregate median req/s | Throughput ratio | p95 ratio | p99 ratio | Result |
| --- | ---: | ---: | ---: | ---: | ---: | --- |
| H1, 16 clients | 11,414 | 11,192 | 98.05% | 1.030 | 1.021 | pass |
| H1, 128 clients | 20,816 | 18,908 | 90.83% | 1.103 | 1.083 | p95 failure |
| H2, 16 streams | 10,901 | 10,446 | 95.83% | 1.092 | 1.104 | p99 failure |
| H2, 128 streams | 18,396 | 16,197 | 88.04% | 1.150 | 1.143 | throughput and tail failure |

The predeclared gates require aggregate median throughput to be at least 90%
of legacy and median p95 and p99 to be at most 1.10 times legacy. H1 at 16
clients now passes all three gates. The H1 128-client p95 and H2 16-stream p99
miss narrowly, at 1.1029 and 1.1036 respectively. H2 at 128 streams remains a
material failure: throughput is 88.04% of legacy, with p95/p99 ratios of
1.150/1.143. Its three paired throughput ratios range from 87.87% to 88.84%,
so the median failure is not a single favorable or unfavorable repetition.

The combined checkpoint improved several medians relative to the first
cohort, but the cohorts differ in measured RTT and ordinary run variation as
well as code. This evidence therefore supports the measured combined-checkpoint
result; it does not assign the change solely to paired refunds or the shutdown
correction.

| Workload | First throughput ratio | Current throughput ratio | First p95 ratio | Current p95 ratio | First p99 ratio | Current p99 ratio |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| H1, 16 clients | 93.97% | 98.05% | 1.145 | 1.030 | 1.203 | 1.021 |
| H1, 128 clients | 90.12% | 90.83% | 1.104 | 1.103 | 1.088 | 1.083 |
| H2, 16 streams | 94.80% | 95.83% | 1.104 | 1.092 | 1.087 | 1.104 |
| H2, 128 streams | 84.71% | 88.04% | 1.185 | 1.150 | 1.158 | 1.143 |

Each workload has three ten-second closed-loop samples after a two-second
warmup. Odd repetitions run legacy then aggregate; the even repetition reverses
the order. Server, agent/origin and load generation use disjoint logical CPU
sets. CPUs 0-3 and 4-7 each span two physical cores with two SMT siblings per
core. Full sample arrays, paired ratios, ranges, population standard deviations
and per-process sampling are retained in `summary.json` and `results.jsonl`.

The current server is identical in both modes. The legacy agent uses one
control plus three bulk physical lanes, while aggregate-v1 uses one control plus
four aggregate bulk lanes. This isolates the negotiated agent transport more
closely than a full historical pair but does not isolate lane-count cost. It is
not an HAProxy comparison and does not change the still-unmet 80% HAProxy
common-path gate. The separate bulk-WAN and constrained-capacity passes remain
workload-specific. The 16/32-agent standing-pool cohort remains blocked behind
this failed tiny-response gate.

`results.jsonl` preserves all 24 raw rows. `summary.json` applies the gates and
records the nonzero result. `manifest.json` records source, binary, overlay,
harness, toolchain, CPU and network provenance. The exact runner and frozen
legacy overlay are copied as text, and `SHA256SUMS` covers every artifact.

## Replay

Build from clean archives and use the snapshotted runner and legacy overlay.

```bash
git archive 805a8ea9313336908570d3495d97509e342248c9 | tar -x -C "$BUILD_ROOT"
(cd "$BUILD_ROOT" && go test -c -o bin/server.test ./internal/server)
(cd "$BUILD_ROOT" && go test -c -o bin/agent.test ./internal/agent)

git archive 3ec137c3ad3d07028e867042fb00d3933912d7ab | tar -x -C "$LEGACY_BUILD_ROOT"
cp proxy_qualification_external_legacy_agent_test.go.txt \
  "$LEGACY_BUILD_ROOT/internal/agent/proxy_qualification_external_legacy_test.go"
(cd "$LEGACY_BUILD_ROOT" && go test -c -o bin/agent.test ./internal/agent)

bash qualify-proxy-agent-growth.sh.txt \
  --server-binary "$BUILD_ROOT/bin/server.test" \
  --aggregate-agent-binary "$BUILD_ROOT/bin/agent.test" \
  --legacy-agent-binary "$LEGACY_BUILD_ROOT/bin/agent.test" \
  --output results.jsonl \
  --server-source-ref 805a8ea9313336908570d3495d97509e342248c9 \
  --aggregate-agent-source-ref 805a8ea9313336908570d3495d97509e342248c9 \
  --legacy-agent-source-ref 3ec137c3ad3d07028e867042fb00d3933912d7ab \
  --tiny-response --rtt-ms 1
```

The expected exit is nonzero because the recorded samples fail the predeclared
gate. The summary and every completed raw row are written before that exit.

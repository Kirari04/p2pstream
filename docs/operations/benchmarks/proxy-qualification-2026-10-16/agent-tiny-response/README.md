# Aggregate agent low-latency tiny-response qualification

This bounded cohort compares aggregate-v1 with the frozen `3ec137c` legacy
agent on the same current server, public TLS listener, 256-byte origin response
and load driver. It uses candidate `15b0354cdbc2c3862fadc68c938f3c3185320e5e`.
The routed path was configured for 1 ms RTT as 500 us on each path half and
measured 1.316 ms over five pings. This low-latency case exposes small-frame
protocol and scheduling cost that the separate 80 ms large-transfer cohort can
hide.

The qualification failed. All 3,510,147 attempted requests completed with the
expected 256-byte payload, status and HTTP version, and all 3,510,147 events
persisted. No event was dropped, no queue residue remained after drain, and no
drain error occurred. The failure is therefore a measured transport regression,
not a payload, recorder or fixture failure.

| Workload | Legacy median req/s | Aggregate median req/s | Throughput ratio | p95 ratio | p99 ratio | Result |
| --- | ---: | ---: | ---: | ---: | ---: | --- |
| H1, 16 clients | 11,481 | 10,789 | 93.97% | 1.145 | 1.203 | tail failure |
| H1, 128 clients | 21,032 | 18,953 | 90.12% | 1.104 | 1.088 | p95 failure |
| H2, 16 streams | 10,869 | 10,303 | 94.80% | 1.104 | 1.087 | p95 failure |
| H2, 128 streams | 18,222 | 15,436 | 84.71% | 1.185 | 1.158 | throughput and tail failure |

The predeclared gates require aggregate median throughput to be at least 90%
of legacy and median p95 and p99 to be at most 1.10 times legacy. Every workload
missed at least one tail gate; H2 at 128 streams also missed throughput. The H1
128-client throughput median narrowly passed, but its paired ratios ranged from
88.33% to 93.29%. Full sample arrays, paired ratios, ranges and population
standard deviations are in `summary.json`.

Each workload has three ten-second closed-loop samples after a two-second
warmup. Odd repetitions run legacy then aggregate; the even repetition reverses
the order. Server, agent/origin and load generation use disjoint logical CPU
sets. CPUs 0-3 and 4-7 each span two physical cores with two SMT siblings per
core. Raw rows include per-process sampling intervals, CPU, peak RSS, heap, GC,
FD and socket observations. Those process counters are retained for diagnosis;
the decisive comparison is useful completions and latency under the matched
load.

The current server is identical in both modes. The legacy agent uses one
control plus three bulk physical lanes, while aggregate-v1 uses one control plus
four aggregate bulk lanes. This isolates the negotiated agent transport more
closely than a full historical pair but does not isolate lane-count cost. It is
not an HAProxy comparison and does not change the still-unmet 80% HAProxy
common-path gate. The separate bulk-WAN and constrained-capacity qualifications
remain valid for their workloads.

`results.jsonl` preserves all 24 raw rows. `summary.json` applies the gates and
records the nonzero qualification result. `manifest.json` records source,
binary, overlay, harness, toolchain, CPU and network provenance. The harness
and frozen legacy overlay are copied as text, and `SHA256SUMS` covers every
artifact.

## Replay

Build from a clean archive and use the snapshotted runner and legacy overlay.

```bash
git archive 15b0354cdbc2c3862fadc68c938f3c3185320e5e | tar -x -C "$BUILD_ROOT"
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
  --server-source-ref 15b0354cdbc2c3862fadc68c938f3c3185320e5e \
  --aggregate-agent-source-ref 15b0354cdbc2c3862fadc68c938f3c3185320e5e \
  --legacy-agent-source-ref 3ec137c3ad3d07028e867042fb00d3933912d7ab \
  --tiny-response --rtt-ms 1
```

The expected exit is nonzero because the recorded samples fail the predeclared
gate. The summary and all completed raw rows are written before that exit.

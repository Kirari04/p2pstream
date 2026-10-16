# Batched-refund aggregate tiny-response qualification

This cohort measures the bounded multi-stream refund-batch checkpoint
`5288994b012c7566999ece140d7bdaa8bbd9d984`. It uses the same current server,
frozen `3ec137c` legacy agent, public TLS path, 256-byte verified response,
CPU sets, configured 1 ms RTT, two-second warmup, ten-second timed interval,
three alternating pairs and predeclared gates as the preceding tiny-response
cohorts.

The predeclared H2/c128 screen passed all three median gates before the full
matrix was run. At a measured 1.320 ms RTT, aggregate-v1 reached 92.17% of
legacy throughput with p95/p99 ratios of 1.088/1.091. Its paired throughput
ratios were 87.24%, 92.28% and 90.21%, so the pass did not erase the recorded
spread. All 1,065,418 requests and events were conserved without errors, drops,
queue residue or drain errors.

The required full 24-row matrix still failed one gate. At a measured 1.336 ms
RTT, H2/c128 p95 was 1.111 times legacy, above the unchanged 1.10 limit. Its
90.08% throughput and 1.085 p99 ratios passed. The other three workload cells
passed throughput, p95 and p99.

| Workload | Legacy median req/s | Aggregate median req/s | Throughput ratio | p95 ratio | p99 ratio | Result |
| --- | ---: | ---: | ---: | ---: | ---: | --- |
| H1, 16 clients | 11,363 | 11,134 | 97.98% | 1.047 | 1.040 | pass |
| H1, 128 clients | 20,501 | 19,972 | 97.42% | 1.041 | 1.034 | pass |
| H2, 16 streams | 10,850 | 10,485 | 96.64% | 1.087 | 1.075 | pass |
| H2, 128 streams | 17,984 | 16,201 | 90.08% | 1.111 | 1.085 | p95 failure |

All 3,564,363 full-matrix requests completed with the expected payload, status
and HTTP version, and exactly 3,564,363 events persisted. There were zero
request errors, event drops, queue residue or drain errors. H2/c128 paired
throughput ratios ranged from 89.27% to 91.70%; paired p95 ratios ranged from
1.095 to 1.121. The nonzero qualification result is retained without changing
the gate or selecting a favorable repetition.

This combined checkpoint materially narrows the earlier tiny-response gaps,
but the evidence does not establish that the refund batch alone caused every
difference between cohorts. Tiny-response qualification remains failed, the
full matrix is not waived by the preceding focused pass, and the 16/32-agent
standing-pool cohort remains deferred. This is not an HAProxy comparison and
does not change the still-unmet 80% HAProxy common-path gate.

`focused-h2c128-results.jsonl` and `focused-h2c128-summary.json` preserve the
screen. `results.jsonl` and `summary.json` preserve the full matrix and its
single failed gate. The original and filtered runners, exact zero-context
filter diff, frozen legacy overlay, manifest and SHA-256 list make both stages
replayable.

## Replay

Build from clean archives and use the saved runners and legacy overlay.

```bash
git archive 5288994b012c7566999ece140d7bdaa8bbd9d984 | tar -x -C "$BUILD_ROOT"
(cd "$BUILD_ROOT" && go test -c -o bin/server.test ./internal/server)
(cd "$BUILD_ROOT" && go test -c -o bin/agent.test ./internal/agent)

git archive 3ec137c3ad3d07028e867042fb00d3933912d7ab | tar -x -C "$LEGACY_BUILD_ROOT"
cp proxy_qualification_external_legacy_agent_test.go.txt \
  "$LEGACY_BUILD_ROOT/internal/agent/proxy_qualification_external_legacy_test.go"
(cd "$LEGACY_BUILD_ROOT" && go test -c -o bin/agent.test ./internal/agent)

bash qualify-proxy-agent-growth-h2c128.sh.txt \
  --server-binary "$BUILD_ROOT/bin/server.test" \
  --aggregate-agent-binary "$BUILD_ROOT/bin/agent.test" \
  --legacy-agent-binary "$LEGACY_BUILD_ROOT/bin/agent.test" \
  --output focused-h2c128-results.jsonl \
  --server-source-ref 5288994b012c7566999ece140d7bdaa8bbd9d984 \
  --aggregate-agent-source-ref 5288994b012c7566999ece140d7bdaa8bbd9d984 \
  --legacy-agent-source-ref 3ec137c3ad3d07028e867042fb00d3933912d7ab \
  --tiny-response --rtt-ms 1

bash qualify-proxy-agent-growth.sh.txt \
  --server-binary "$BUILD_ROOT/bin/server.test" \
  --aggregate-agent-binary "$BUILD_ROOT/bin/agent.test" \
  --legacy-agent-binary "$LEGACY_BUILD_ROOT/bin/agent.test" \
  --output results.jsonl \
  --server-source-ref 5288994b012c7566999ece140d7bdaa8bbd9d984 \
  --aggregate-agent-source-ref 5288994b012c7566999ece140d7bdaa8bbd9d984 \
  --legacy-agent-source-ref 3ec137c3ad3d07028e867042fb00d3933912d7ab \
  --tiny-response --rtt-ms 1
```

The focused command exits zero. The full command writes every row and summary,
then exits nonzero because H2/c128 p95 fails the predeclared gate.

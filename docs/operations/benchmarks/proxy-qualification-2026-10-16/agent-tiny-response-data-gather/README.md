# DATA-gather aggregate tiny-response qualification

This cohort measures DATA gathering checkpoint
`f18c27bc010ce2f9f597866696b4f4c8a699e725`. It uses the same current server,
frozen `3ec137c` legacy agent, public TLS path, 256-byte verified response,
CPU sets, configured 1 ms RTT, two-second warmup, ten-second timed interval,
three alternating pairs and predeclared gates as the preceding tiny-response
cohorts.

The H2/c128 screen passed before the full matrix was run. At a measured
1.314 ms RTT, aggregate-v1 reached 92.83% of legacy throughput with p95/p99
ratios of 1.076/1.060. Its paired throughput ratios were 89.22%, 94.21% and
92.86%, preserving the below-gate first paired sample rather than selecting
only favorable runs. All 1,126,198 requests and events were conserved without
errors, drops, queue residue or drain errors.

The required full 24-row matrix passed all twelve throughput and tail gates at
a measured 1.337 ms RTT.

| Workload | Legacy median req/s | Aggregate median req/s | Throughput ratio | p95 ratio | p99 ratio | Result |
| --- | ---: | ---: | ---: | ---: | ---: | --- |
| H1, 16 clients | 11,550 | 11,256 | 97.45% | 1.061 | 1.053 | pass |
| H1, 128 clients | 21,993 | 21,305 | 96.87% | 1.037 | 1.028 | pass |
| H2, 16 streams | 10,919 | 10,669 | 97.71% | 1.047 | 1.005 | pass |
| H2, 128 streams | 18,748 | 17,469 | 93.18% | 1.057 | 1.067 | pass |

All 3,717,428 full-matrix requests completed with the expected payload, status
and HTTP version, and exactly 3,717,428 events persisted. There were zero
request errors, event drops, queue residue or drain errors. H2/c128 paired
throughput ratios ranged from 92.39% to 94.09%; its paired p95 ratios ranged
from 1.050 to 1.082. H1/c16 has one paired p99 ratio of 1.119, while its
predeclared median p99 ratio passes at 1.053; the raw variation remains in the
summary.

This result qualifies the measured tiny-response matrix for this checkpoint.
It does not establish HAProxy parity, standing-pool scaling, WAN bulk-transfer
non-regression, retained-grown-pool pressure recovery, or constrained capacity.
Those workloads require their own final-runtime repeats. A later publisher
cleanup also requires revalidation before this checkpoint can be treated as the
final integrated runtime.

`focused-h2c128-results.jsonl` and `focused-h2c128-summary.json` preserve the
screen. `results.jsonl` and `summary.json` preserve the full matrix. The
original and filtered runners, exact zero-context filter diff, frozen legacy
overlay, manifest and SHA-256 list make both stages replayable.

## Replay

Build from clean archives and use the saved runners and legacy overlay.

```bash
git archive f18c27bc010ce2f9f597866696b4f4c8a699e725 | tar -x -C "$BUILD_ROOT"
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
  --server-source-ref f18c27bc010ce2f9f597866696b4f4c8a699e725 \
  --aggregate-agent-source-ref f18c27bc010ce2f9f597866696b4f4c8a699e725 \
  --legacy-agent-source-ref 3ec137c3ad3d07028e867042fb00d3933912d7ab \
  --tiny-response --rtt-ms 1

bash qualify-proxy-agent-growth.sh.txt \
  --server-binary "$BUILD_ROOT/bin/server.test" \
  --aggregate-agent-binary "$BUILD_ROOT/bin/agent.test" \
  --legacy-agent-binary "$LEGACY_BUILD_ROOT/bin/agent.test" \
  --output results.jsonl \
  --server-source-ref f18c27bc010ce2f9f597866696b4f4c8a699e725 \
  --aggregate-agent-source-ref f18c27bc010ce2f9f597866696b4f4c8a699e725 \
  --legacy-agent-source-ref 3ec137c3ad3d07028e867042fb00d3933912d7ab \
  --tiny-response --rtt-ms 1
```

Both recorded commands exit zero after writing every row and summary.

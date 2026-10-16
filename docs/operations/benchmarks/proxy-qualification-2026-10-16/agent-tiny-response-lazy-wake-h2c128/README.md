# Lazy-wake H2/c128 tiny-response screen

This bounded screen measures the critical H2/c128 cell after the lazy
aggregate send-wake checkpoint
`79650caf4f028f4680748d33dbe1f038e7c3d109`. It uses the same current server,
frozen `3ec137c` legacy agent, public TLS path, 256-byte verified response,
CPU sets, configured 1 ms RTT, two-second warmup, ten-second timed interval and
three alternating pairs as the preceding 24-row cohorts. Measured RTT was
1.325 ms.

The screen failed, so the full 24-row matrix was not run. Aggregate-v1 reached
16,573.71 median useful requests/s versus 18,378.79 for legacy, a 90.18%
throughput ratio that passes the unchanged 90% gate. Its p95 and p99 ratios
were 1.1151 and 1.1020, both above the unchanged 1.10 tail gate.

| Repetition | Aggregate/legacy throughput | Aggregate/legacy p95 | Aggregate/legacy p99 |
| ---: | ---: | ---: | ---: |
| 1 | 86.70% | 1.162 | 1.122 |
| 2 | 90.18% | 1.120 | 1.104 |
| 3 | 90.40% | 1.111 | 1.085 |

The paired throughput range is 86.70% to 90.40%, with population standard
deviation 0.01693. Every one of the 1,062,699 attempted requests completed with
the expected payload, status and HTTP version. Exactly 1,062,699 events
persisted, with zero request errors, event drops, queue residue or drain errors.
The result is therefore a tail-latency qualification failure rather than a
functional or recorder failure.

This is a screening result for one cell. It does not qualify the full H1/H2,
16/128 matrix, standing-pool scaling, or HAProxy parity. The standing-pool
cohort remains blocked behind tiny-response qualification. The measured
checkpoint result may guide another bounded transport experiment, but normal
run variation prevents attributing every difference from the preceding cohort
to the lazy-wake change alone.

`results.jsonl` preserves all six raw rows and `summary.json` applies the same
gates. The snapshotted runner differs from the full runner only in the saved
four-line `runner-filter.patch.txt`: it selects H2/c128 during collection and
summary instead of the four-cell matrix. `manifest.json` records source,
binary, overlay, runner, CPU, network and command provenance.

## Replay

Build from clean archives and use the saved filtered runner and legacy overlay.

```bash
git archive 79650caf4f028f4680748d33dbe1f038e7c3d109 | tar -x -C "$BUILD_ROOT"
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
  --output results.jsonl \
  --server-source-ref 79650caf4f028f4680748d33dbe1f038e7c3d109 \
  --aggregate-agent-source-ref 79650caf4f028f4680748d33dbe1f038e7c3d109 \
  --legacy-agent-source-ref 3ec137c3ad3d07028e867042fb00d3933912d7ab \
  --tiny-response --rtt-ms 1
```

The expected exit is nonzero because the H2/c128 tail ratios fail the
predeclared gate. The summary and all six raw rows are written before that
exit.

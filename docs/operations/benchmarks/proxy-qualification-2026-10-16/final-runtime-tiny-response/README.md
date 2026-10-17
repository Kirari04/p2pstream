# Final-runtime tiny-response qualification

This is the final `dc738e9000e60cd7ce3a0279127486f3bc05067a`
tiny-response revalidation. It uses the current server and aggregate agent,
the frozen `3ec137c3ad3d07028e867042fb00d3933912d7ab` compatible legacy agent,
the same TLS public path and origin, and a namespace path configured for 1 ms
RTT and 1 Gbit/s. Five pings measured 1.375 ms. Each endpoint pair received a
two-second warmup and ten-second timed interval. Three alternating passes cover
H1 and H2 at 16 and 128 concurrent requests.

All ratio-of-medians gates pass. The predeclared gates require aggregate
throughput at least 90% of legacy and aggregate p95 and p99 no more than 1.10
times legacy.

| Workload | Throughput ratio | p95 ratio | p99 ratio |
| --- | ---: | ---: | ---: |
| H1, 16 clients | 0.9735 | 1.0526 | 1.0538 |
| H1, 128 clients | 0.9506 | 1.0809 | 1.0788 |
| H2, 16 clients | 0.9790 | 1.0175 | 1.0341 |
| H2, 128 clients | 0.9180 | 1.0851 | 1.0548 |

The raw paired values remain material: some individual pairs cross a gate even
though the three-pass ratio of medians passes. This result therefore supports
the predeclared cohort-level gate and does not claim zero run-to-run variance.
Across all 24 rows, 3,749,245 requests were attempted, returned the expected
payload, and were persisted. Client errors, recorder drops, queue residue and
drain errors were zero.

`results.jsonl` preserves every row. `summary.json` contains all samples,
paired ratios, ratio-of-medians acceptance and spread. The manifest records
the exact source, binary, runner, topology and command-option provenance. The
saved runner is text-only evidence; replay should create build directories
before compiling:

```bash
mkdir -p "$BUILD_ROOT/bin" "$LEGACY_BUILD_ROOT/bin"
```

The frozen legacy overlay and full archive build procedure are preserved in
[`../agent-wan-growth/README.md`](../agent-wan-growth/README.md). Verify every
binary and runner against `manifest.json` before replay.

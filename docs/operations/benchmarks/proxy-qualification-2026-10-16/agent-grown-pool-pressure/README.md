# Aggregate agent grown-pool pressure qualification

This finite qualification validates aggregate-v1 after every receiving bulk lane has grown beyond 80 MiB. It uses candidate `f2213cf14c158c62c158f2be3d270354d38a7639`, one server and one real external agent in distinct 4 GiB cgroup-v2 scopes with swap disabled, and the same configured 80 ms / 1 Gbit/s namespace path as the WAN growth comparison.

The candidate passed every predeclared gate. This is workload evidence for the tested topology. Server and agent keep independent production admission ledgers and run in separate 4 GiB cgroups here. Pool credit is a conservative reservation and is reported separately from actual cgroup residency.

## Workload and result

Four consecutive 256 MiB downloads and four consecutive 256 MiB uploads exercised every receiving bulk lane. A retained-transport transfer followed in each direction. Before pressure, each server receiving pool was strictly above 80 MiB at 83.0, 92.5, 96.75, and 161.25 MiB; each agent receiving pool was 99.5, 101.75, 102.5, and 102.75 MiB. The exact byte arrays are in `summary.json`.

- Before pressure, actual cgroup residency was 309,211,136 bytes for the server and 139,059,200 bytes for the agent. Session-resource promises were 572,164,608 and 545,720,832 bytes, respectively.
- The agent reached `critical/memory` at 3,909,738,496 of 4,294,967,296 bytes after 3,584 MiB of incrementally touched ballast. This left 385,228,800 bytes below the hard limit. `oom` and `oom_kill` remained zero for both cgroups.
- Ordinary admission fell to zero. A public request returned a recorded 503, produced zero useful completions, incremented the pressure-rejection counter from 0 to 1, persisted exactly one event, and left no queued or dropped event.
- A genuinely cold one-shot control health request succeeded with HTTP 200 while ordinary admission remained zero. Four reserved control slots owned 3,145,728 base bytes and 8 FDs. The ordinary pressure-rejection counter stayed at 1, and control ownership returned to its zero baseline without replacing any physical session.
- The canceled HTTP/2 upload reached the origin before cancellation: the origin had read 8,929,280 bytes when cancellation was triggered and 9,486,336 bytes at the independent final observation, from a configured 32 MiB body. The client classified `context.Canceled`; exactly one event persisted; all five session identities remained unchanged; all agent allocation units and pending refunds returned to their baselines.
- After releasing ballast, the agent returned healthy at 124,456,960 bytes of actual cgroup usage. The server was at 353,992,704 bytes. Exact 16 MiB download and upload recovery requests completed, and every receiving pool still exceeded 80 MiB. The retained server pools were 83.0–161.25 MiB and agent pools 99.5–102.75 MiB.

The low actual resident usage after recovery, despite retained grown-pool promises, does not show trapped physical memory under this workload. This result does not justify shrinking or reclaiming session pools. It also does not measure tiny-response throughput or HAProxy parity; those remain separate qualification gates.

`smoke-results.jsonl` and `smoke-summary.json` preserve the preceding 4 MiB-per-lane fixture check from the same immutable candidate. That smoke proves the corrected cold-control and origin-cancellation mechanics, but its roughly 9–56 MiB server and 10–43 MiB agent pools are not full-grown evidence. `results.jsonl` and `summary.json` are the decisive greater-than-80-MiB cohort.

## Replay

Build both test binaries from a clean archive and run the snapshotted harness. The legacy binary argument is mandatory at the command surface but unused in pressure mode.

```bash
git archive f2213cf14c158c62c158f2be3d270354d38a7639 | tar -x -C "$BUILD_ROOT"
(cd "$BUILD_ROOT" && go test -c -o bin/server.test ./internal/server)
(cd "$BUILD_ROOT" && go test -c -o bin/agent.test ./internal/agent)

bash qualify-proxy-agent-growth.sh.txt \
  --server-binary "$BUILD_ROOT/bin/server.test" \
  --aggregate-agent-binary "$BUILD_ROOT/bin/agent.test" \
  --legacy-agent-binary "$LEGACY_BUILD_ROOT/bin/agent.test" \
  --output results.jsonl \
  --server-source-ref f2213cf14c158c62c158f2be3d270354d38a7639 \
  --aggregate-agent-source-ref f2213cf14c158c62c158f2be3d270354d38a7639 \
  --legacy-agent-source-ref 3ec137c3ad3d07028e867042fb00d3933912d7ab \
  --pressure --warmup-mib 256 --steady-mib 256 \
  --transfer-repetitions 1 --required-pool-mib 80
```

The decisive command exits zero only after the pressure summary is written and every acceptance field is true.

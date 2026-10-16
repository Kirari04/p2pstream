# Moderate-load latency runner validation

These are synthetic contract checks, not performance measurements. The source
runner passed shell syntax, embedded Python compilation, help output and 19
positive/negative calibration and schedule cases. No Go compilation, proxy,
Docker container or network load ran during these checks.

`results.json` records all cases and the exact runner SHA256. The recorded
`validation-source.py.txt` extracts the actual calibration and result-validation
helpers from that runner with Python AST. It generates explicit synthetic
36-row inputs and plaintext binary-identity placeholders that are never
executed. Its `repo` constant names the original worktree; change that constant
to an equivalent checkout to replay. The full tested runner is preserved as
`runner.sh.txt`.

The positive calibration includes deliberately slower Caddy rates to verify
that only the lower candidate/HAProxy median determines the offered rate.
Negative cases reject source/raw-hash changes, mismatched H2 topology, missing
candidate events, undrained queues, incorrect Caddy payload and wrong reference
warmup/duration. A wide spread is reported without introducing a variance gate.
Result cases reject missing/invalid schedule data, payload/status errors,
nonfinite latency and missed delivery limits.

`preregistered-plan.json` predates all calibration/performance runs. Its
"metadata proposal pending" note records its creation state; commit `5fb6305`
subsequently supplied the required optional offered-schedule timestamps. This
plan fixes three alternating candidate/HAProxy pairs in each H1/H2, c16/c128
cell at half the lower useful-throughput median from the same frozen, warmed
36-row reference. Each cell schedules 20 seconds of requests and counts latency
from scheduled arrival. Delivery requires at least 99% of the offered rate and
completion within 101% of the scheduled duration; exact payload, status,
protocol and candidate-event checks apply. Median p95 and p99 ratios may be at
most 1.25. This qualifies moderate load only, and does not establish latency
near saturation. H1 includes initial client/TLS establishment; H2 reuses one
separately accounted prewarm connection.

The optional mode requires the same source, harness, runner, candidate/driver,
Caddy/HAProxy and CPU/cgroup identities as calibration. It preserves each raw
row before validation and fails closed on a delivery/correctness error. Default
reference behavior remains the 36-row saturation comparison. A real final
calibration and 24-row moderate-load comparison remain pending.

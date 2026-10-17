# Matched native Go attribution diagnostic

The complete fixed 24-row cohort over immutable `248bad099682878fe398c0ebaad235787ef47858` compares the production direct path with a stripped-down `httputil.ReverseProxy` using one ordinary Go 1.26.6 test executable. Public HTTP/HTTPS, H1 concurrency 16/128, three alternating pairs, two-second warmup and ten timed seconds use the same origin, public/origin socket configuration, transport pool/deadlines, 32 KiB response pool, CPU assignment and 4 GiB/no-swap proxy envelope. The four-condition smoke verifies the generated shared certificate, TLS version/cipher/ALPN and actual H1 origin/public protocols.

| Public scheme | Clients | Production median RPS | Native median RPS | Production/native | Paired ratios |
|---|---:|---:|---:|---:|---|
| http | 16 | 30,931 | 44,992 | 68.75% | 0.74151, 0.67322, 0.69307 |
| http | 128 | 29,905 | 46,062 | 64.92% | 0.65600, 0.62369, 0.65631 |
| https | 16 | 29,802 | 42,271 | 70.50% | 0.71862, 0.69531, 0.70587 |
| https | 128 | 28,524 | 42,698 | 66.80% | 0.65387, 0.63336, 0.67635 |

All 8,920,049 timed responses pass exact status, payload and protocol checks with zero errors. All twelve production rows conserve persisted request events exactly. Every cell has all three paired ratios below the preregistered 0.85 profiling trigger. The observed application delta therefore warrants targeted profiling; it does not identify one omitted subsystem as causal.

The native arm intentionally omits admission/leases, route/target selection, retry/cache middleware, WAF/access checks and request-event persistence. Removing these controls is not an optimization proposal. These rows are an application attribution diagnostic, not a release gate, current-HEAD qualification, or matched comparison against the earlier Caddy/HAProxy cohort. The latter used an IP-targeted client without explicit SNI; this diagnostic sends `public.test` in both HTTPS arms.

The runner exits zero with 28 attempted results (four smoke, 24 timed), 24 accepted timed rows, no forced cleanup, no cleanup errors, and no remaining owned process groups or containers. See `raw/{summary,outcome,provenance,smoke}.json` and `raw/{results,attempted-results}.jsonl`.

The earlier `run.9cj69oqt` is retained separately in `history/interrupted`: fourteen accepted timed rows, eighteen attempted rows, one later raw result, no complete summary or terminal outcome. Recovery found its processes absent following host restart. Its rows are not combined with this fresh cohort.

`source/` retains the portable overlay, source checkpoint, plan, effective settings, build manifest and runner/builder/contracts as text. `validation/` retains the corrected SNI build/test/lifecycle evidence. Native binaries, source archive, scratch databases and private fixture keys remain untracked at their recorded local paths; public certificate fingerprints and binary/archive/source hashes remain in the manifests. `export-map.json` maps copied bytes to original paths. `SHA256SUMS` covers tracked artifacts.

For replay, export the immutable base, apply `source/native-go-attribution.patch`, and use the saved builder with the checkpoint/plan/settings to create a fresh owned disk-backed bundle. The exact accepted runner command is in `launch/native-full.command`; a fresh full invocation automatically repeats its certificate smoke and the complete matrix. Do not concatenate an interrupted cohort or use profiled rates as throughput qualification.

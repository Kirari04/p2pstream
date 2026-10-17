# Proposed native Go attribution diagnostic — source plan only

The fresh 248bad0 reference meets/exceeds Caddy throughput in its four HTTPS
cells but reaches only 0.336–0.667 of HAProxy throughput. This diagnostic would
measure application overhead against the same Go HTTP stack. It would not clear
the HAProxy qualification gate or establish the cause of the HAProxy gap.

## Reuse existing source

- `internal/server/proxy_qualification_test.go:472`: the subprocess dispatcher
  already has both `proxy` and `stdlib` roles in the same ordinary test binary.
- `runQualificationStdlibProxy` at line 502 already uses a native ReverseProxy,
  an explicit upstream idle pool, and optional production 32 KiB BufferPool.
  Select one shard, total idle128, and pooled buffers. Do not build a new proxy.
- `runQualificationProxy` at line 996 starts the production App through
  `newRealAgentProxyFixture`, its real listener and the configured direct-route
  pipeline. This is the same simple route fixture as the reference benchmark;
  it does not exercise every Sites/policy configuration.
- Both roles currently serve HTTPS only. A test-only overlay could add one
  explicit plaintext mode to these two entry points: native `srv.Serve` versus
  `ServeTLS`, and production HTTP versus HTTPS listener configuration. Default
  behavior remains HTTPS. Ready URLs must reflect the actual scheme.
- The existing load role accepts both URL schemes and can force HTTP/1.1. It
  already supplies the same checked tiny payload, warmup, fixed-duration work,
  connection pool, runtime observation, and candidate event conservation.
- The current Caddy/HA reference uses HTTPS at the public side, including H1;
  plaintext would be a new attribution condition, not its existing baseline.

## Fixed proposed matrix

One new immutable base248bad0 + test-only overlay, exact Go1.26.6, same binary for
both proxy roles, origin and load. No production .go changes and no ordinary
qualification trace/growth-counter build tag.

Production versus pooled native Go × public HTTP versus public HTTPS × H1
concurrency16/128 × three fresh balanced pairs = 24 rows. Use two-second warmup
and ten-second timing, origin idle pool128, 32 KiB pooled response copying, the
same deterministic small response and HTTP/1 origin, server CPUs0–3,
origin/load4–15, GOMAXPROCS4 and separate4GiB/no-swap proxy cgroups for both roles.
Use the same certificate, client, payload, status, connection-reuse and process
sampling contracts. Preserve ordinary short reads and actual production socket
behavior; explicitly record any network-setting difference instead of tuning it.
For attribution HTTPS only, the H1 load client explicitly sends SNI
`public.test` in both arms so both listeners serve the generated shared fixture
certificate; its default behavior, plaintext mode and ALPN remain unchanged.
Before implementation, compare every effective upstream transport setting. The
existing stdlib role's literal transport has zero idle/header timeouts, unlike
the application transport. Match idle/header/connect deadlines and buffer sizes
in the diagnostic, or preregister each intentional difference; equal pool size
alone is insufficient. Preserve existing default subprocess-role behavior.

Each protocol mode gets a fresh proxy process for each arm; both concurrency
cells run in that process in the same order. Predeclare pair arm order
production→native, native→production, production→native; protocol order
HTTP→HTTPS, HTTPS→HTTP, HTTP→HTTPS. Preserve all24 rows, no selective retries.

Correctness, exact payload/protocol/status, event conservation for production,
complete provenance and natural resource cleanup are prerequisites. Report all
per-pair ratios plus medians. A consistent production/native RPS ratio below
0.85 is a profiling trigger; it is not a release gate or a tuning retention
decision. Saturated tail ratios remain descriptive; moderate latency is handled
by the separate36-row qualification.

## Explicit omissions and boundaries

Native Go has no Sites/policy selection, admission/ownership/health reserve,
WAF/access checks, retry/cache middleware or request-event persistence. Its
runtime sampler remains enabled for comparable observation. Those omissions
are the intended application delta, not equivalent proxy functionality.

The HTTPS and plaintext delta includes the real listener implementations and
TLS state. It is not a cryptography-only measurement. Results cannot be
combined with earlier HA/Caddy rows as a contemporaneous causal comparison.
The earlier reference also used the default IP-targeted H1 client without the
attribution-only explicit SNI, which independently prohibits a direct causal
comparison of those cells.

Before implementation, Astra should review the exact test-only listener switch,
upstream-pool/buffer equivalence, omitted functions, TLS configuration and
process/CPU matrix. Use a fresh isolated overlay and bounded builder/launcher;
never modify the calibration archive while the moderate test uses it. A bounded
four-condition smoke should prove scheme, host routing, payload and cleanup
before any accepted24-row run. No implementation or workload is authorized by
this draft.

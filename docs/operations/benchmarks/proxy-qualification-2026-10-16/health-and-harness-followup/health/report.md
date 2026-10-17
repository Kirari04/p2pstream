# Routing and health focused validation — 2026-10-17

Source HEAD: `c09c1d5bbe8ad4406f7205755a59d8eb6a3fedee`; branch `perf/proxy-owned-budgets`. Toolchain Go 1.26.6 linux/amd64. No commits, pushes, dev server, WAN workload, or bodyless reservation shortcut.

## Health proposal: passed

Applied the reviewed control-admission error mapping without alteration. The child-context error is captured before cancellation; explicit caller cancellation preserves context.Canceled; closed agent.Done preserves errAgentDisconnected; caller deadline and child gate timeout retain context.DeadlineExceeded in server_health_capacity. Existing admission-pressure and passive-health checks retain local-pressure semantics.

All four new regression tests fail the baseline as predicted and pass the combined candidate. In particular, the actual http.Client health attempt with a live parent times out at the 250 ms child gate and is skipped as agent_server_health_capacity. Cancellation and caller-deadline assertions are at dialViaAgentWithCapacity: they do not establish top-level http.Client cancellation classification.

80 focused tests pass in normal and race builds. The list is in focused-test-names.json and exact commands/statuses are in commands.json. Coverage includes all tests from aggregate tunnels, public health, public routing, stream capacity runtime, plus the five additional host consumers named by the proposal.

## Host fast path: reject under predeclared timing gate

The identical expanded expected-output table and downstream host-consumer tests pass both baseline and candidate. Colon-bearing inputs execute the same prior normalization branch. Empty/whitespace, uppercase DNS, trailing/double dots, bracketed DNS, malformed bracketed DNS, host ports, raw/bracketed IPv6, and malformed multiple colons preserve baseline outputs. No idempotence test was added.

Both benchmark binaries contain identical benchmark/test overlays. Runtime baseline public_routing.go has the proposal-base hash; candidate adds the reviewed health and hostname runtime hunks. The health change is outside the benchmark call path. Binary and source hashes are in source-manifest.json. Build/tests use CPUs0-7/GOMAXPROCS4; benchmark uses CPU0/GOMAXPROCS1. Ten samples per arm use 300 ms benchtime and balanced alternating AB/BA order.

| Case | Baseline ns/op | Candidate ns/op | Candidate / baseline | Baseline B/allocs | Candidate B/allocs |
|---|---:|---:|---:|---:|---:|
| DNS | 71.585 | 31.350 | 0.43794 | 32/1 | 0/0 |
| DNSTrailingDot | 73.350 | 33.090 | 0.45112 | 32/1 | 0/0 |
| DNSPort | 42.700 | 45.630 | 1.06862 | 0/0 | 0/0 |
| IPv6Port | 47.465 | 50.635 | 1.06679 | 0/0 | 0/0 |
| MalformedColons | 82.000 | 84.275 | 1.02774 | 32/1 | 32/1 |

Numbers are medians of ten samples. DNS and trailing-dot inputs meet the required allocation reduction (one allocation and32B/op). Colon allocations are unchanged. DNSPort(+6.86%) and IPv6Port(+6.68%) exceed the predeclared <=3% median timing allowance; MalformedColons(+2.77%) passes. Therefore the exact proposed hostname fastpath fails its declared retention protocol. No broader throughput or performance-parity conclusion follows. Raw samples and summaries are in benchmark-results.json and bench-*.log.

## Handoff

CPU-heavy validation is complete; CPUs0-7 released to coordinator. Final tracked runtime retains only the reviewed health mapping. normalizeRequestHost is byte-for-byte identical to HEAD; final-source-manifest.json binds the final health-only tree. The baseline and both combined candidates passed the expected-output tests, and both combined candidates passed 80 focused normal/race tests. Final health-only integration verification is owned by the coordinator. Root source-manifest.json binds the first rejected combined candidate; indexbyte-reordered/source-manifest.json binds the second rejected candidate. Build binaries occupy183MiB; other artifacts are small.

## One follow-up revision: rejected; optimization stopped

The coordinator specified one independent revision, with the same frozen criteria: use IndexByte for the colon-free test and hoist the common SplitHostPort call before the bracket fallback. Both old branches already invoke exactly the same parser; the error fallback remains unchanged. This avoids duplicate dispatch and bracket testing for successful authorities. The identical expected-output and 80 focused tests pass in normal/race builds. A fresh ten alternating pairs use the same CPU0/GOMAXPROCS1, 300ms/sample protocol and baseline binary.

| Case | Baseline ns/op | Revision ns/op | Revision / baseline | Baseline B/allocs | Revision B/allocs |
|---|---:|---:|---:|---:|---:|
| DNS | 71.895 | 30.495 | 0.42416 | 32/1 | 0/0 |
| DNSTrailingDot | 74.840 | 32.095 | 0.42885 | 32/1 | 0/0 |
| DNSPort | 43.005 | 43.480 | 1.01105 | 0/0 | 0/0 |
| IPv6Port | 47.530 | 48.885 | 1.02851 | 0/0 | 0/0 |
| MalformedColons | 82.780 | 85.750 | 1.03588 | 32/1 | 32/1 |

The revised DNSPort(+1.10%) and IPv6Port(+2.85%) meet the <=3% gate, but MalformedColons(+3.588%) fails it. Allocations meet their criteria. The revision is rejected, the original normalization runtime is restored, and no further tuning or benchmark repeats were performed. Semantic cases and the benchmark remain for future measurement. All second-round commands, logs, samples and the exact rejected runtime patch are retained in indexbyte-reordered/.

Final git diff --check passes. No top-level HTTP caller-cancellation claim or whole-application throughput/parity claim is made.

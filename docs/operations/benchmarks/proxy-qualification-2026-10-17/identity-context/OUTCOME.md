# Resolver-owned identity context: accepted allocation screen

The immutable candidate passes every predeclared gate in all 28 cases. It combines the resolver's fresh identity and immutable configured-header list into one request-owned context, preserving the existing shallow request copy and defensive-copy public getters. Actual production files were unchanged during this screen.

Exactly 10 alternating AB/BA pairs use Go 1.26.6, CPU8/GOMAXPROCS1, 150ms per case. All 20 timing invocations finish naturally. All 560 samples are retained, with raw line and log hashes. Across all cases, the candidate saves **48–200 B and 2–6 allocations per ResolveRequest operation**. Per-case median candidate/baseline time ratios range **0.554021–0.819674**; individual paired ratios range **0.516657–0.886376**. Every case meets the unchanged >=32 B/2 allocation savings, <=1.03 median ratio and <=1.10 paired ratio criteria.

Representative background-parent ResolveRequest-only rows follow. Both parent contexts and both operation modes for all seven request/configuration shapes are in `results.json` and independently reconstructed in `independent-verification.json`.

| Shape | Baseline median ns/op | Candidate median ns/op | Ratio | B saved/op | Allocs saved/op |
|---|---:|---:|---:|---:|---:|
| direct | 334.10 | 269.00 | 0.805148 | 48 | 2 |
| invalid-peer | 368.50 | 302.05 | 0.819674 | 48 | 2 |
| configured-untrusted | 441.50 | 282.15 | 0.639071 | 104 | 5 |
| trusted-single | 594.15 | 390.05 | 0.656484 | 120 | 6 |
| trusted-chain | 750.35 | 546.85 | 0.728793 | 120 | 6 |
| conflicting-sources | 952.20 | 724.60 | 0.760975 | 168 | 6 |
| four-disabled-headers | 474.35 | 262.80 | 0.554021 | 200 | 5 |

The identical semantic overlay and existing resolver/consumer/management tests pass on baseline, candidate and candidate race: **21 top-level tests plus 26 subtest records per arm**. These cover hostile header trust rules, configured-header stripping, snapshot selection, nil/invalid contexts, caller/returned-slice isolation, override precedence, parent values/deadlines/cancellation, retained request lifetime and concurrent readers. All 26 commands (three builds, three correctness invocations and 20 timing invocations) exit0, record natural cleanup, and leave no owned descendants. Complete source file sets and hashes are verified before and after each command; exact executable hashes bind every timed invocation. The independent verifier checks original logs, all samples, audits, identities and unchanged acceptance decisions.

`source-identity.json`, `frozen-inputs.json` and `REVIEWED-INPUTS.json` bind baseline 8fa16378af4ac0117276726fe6e51d8bc4c74c85, 430 existing source files plus the same overlay, the sole existing-file change, compiler, reviewed runner and approved plan. `candidate.patch` and `client_identity_context_test.go` provide the exact measured change and test/benchmark overlay. Setup/resolver/deadline construction is outside timed ranges. There were no failed commands, retries or selective reruns in this screen.

This demonstrates an identity-path microbenchmark allocation and time reduction. It does not measure complete public context initialization, SQLite/event persistence, whole-application HTTP/WAN throughput, or performance relative to native Go, HAProxy or Caddy. Full integration checks remain the coordinator's next gate before production retention. The existing immutable resolver contract is retained: future code must not mutate its internal header list while requests use it.

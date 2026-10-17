# Identity context allocation screen

**Accepted and integrated.** The candidate uses one request-owned context for the resolver's fresh client identity and immutable configured-header list. Public getters retain defensive copies, existing setters retain override behavior, and parent values/deadlines/cancellation still propagate. The exact measured production patch is archived here; production files were unchanged during collection.

The baseline is `8fa16378af4ac0117276726fe6e51d8bc4c74c85`. The sole existing-source change is `internal/server/client_identity.go`; patch SHA-256 is `1bb0fa5ad6d1b073e8a73fcc1510b54d31bc8934418049a2fb53475c300cfa00`, and candidate runtime-file SHA-256 is `a62ee2f50e0fb8aae3b842cf979441b804ce1f3f998164c46819cefbbd410ed3`. [Source identity](source-identity.json), [exact patch](candidate.patch), [test/benchmark overlay](client_identity_context_test.go.txt), and [frozen inputs](frozen-inputs.json) bind all 430 existing source inputs plus the identical overlay in both variants.

Ten alternating AB/BA pairs use pinned Go 1.26.6, CPU8/GOMAXPROCS1 and 150ms per case. All **28 cases / 560 samples** pass. The candidate saves **48–200 B and 2–6 allocations per operation**. Per-case median time ratios are **0.554021–0.819674**; individual paired ratios are **0.516657–0.886376**. Setup and resolver/deadline construction are outside timed ranges.

| ResolveRequest only, background parent | Before ns/op | After ns/op | B saved/op | Allocs saved/op |
|---|---:|---:|---:|---:|
| Direct peer | 334.10 | 269.00 | 48 | 2 |
| Trusted single-IP header | 594.15 | 390.05 | 120 | 6 |
| Conflicting sources | 952.20 | 724.60 | 168 | 6 |
| Four disabled header names | 474.35 | 262.80 | 200 | 5 |

The complete case set covers direct/invalid peers, configured untrusted peers, trusted single-IP/chain sources, conflicting sources and disabled sources; background/deadline parents; and ResolveRequest alone/with both exported reads. [All results](results.json) retain every case. [Independent verification](independent-verification.json) reconstructs all original samples and reapplies the unchanged prospective gates: each paired observation saves at least 32 B and 2 allocations/op, each case's median candidate/baseline time ratio is at most 1.03, and each paired ratio is at most 1.10. No failed commands, retries or selective reruns occurred.

Baseline normal, candidate normal and candidate race each pass **21 top-level tests / 47 pass records including subtests**. Coverage includes hostile-header trust rules, configured-header stripping, snapshots, nil/invalid contexts, slice ownership, override precedence, cancellation/deadlines, retained lifetime and concurrent readers. All 26 commands exit0 with natural cleanup and no owned descendants. Source sets/hashes are checked before and after each command; exact executable identities bind timing invocations. See [review notes](REVIEW-NOTES.md), [commands](commands.json), [raw benchmark log](logs/pair-01-baseline.stdout.log), [ownership audit](logs/correctness-candidate.outer.json) and [outcome](OUTCOME.md).

This establishes an identity-path microbenchmark improvement. It does not establish a complete context-initialization, persistence, HTTP/WAN throughput, or native Go/HAProxy/Caddy result. [Integration receipt](integration.json) verifies that all 431 integrated compilation inputs match the measured candidate, including the new tests. Resolver header storage continues to rely on the existing immutable resolver contract.

The 119 reviewed evidence files are copied byte-for-byte (772,666 bytes). [Export map](export-map.json) binds every copied file to its local original; [original export list](source-export-list.json) preserves that reviewed list, and [SHA256SUMS](SHA256SUMS) binds this whole packet. The text scan records no credential-pattern matches. Source tree copies, executables, caches, temporary runtime files and credentials are excluded; the archived test/benchmark source retains exact bytes with a `.go.txt` filename. Preparatory wording in the frozen proposal/plan records the pre-execution design, not pending execution.

The integration receipt was added after collection; the 119 archived evidence files remain unchanged.

# Mixed-route locality regressions — normal and race passed

Both focused regressions passed once normally and once under the race detector, with exactly two top-level passes, no skips, package success and natural owner cleanup in each command. The tested source is production checkpoint `1fa81d839f35ae575c41779a309d356c23c1f956` plus one test-only overlay: 431 base compile inputs and 432 inputs with the new file. The execution HEAD, `b5a8915e6fd06ae5f6010ee53ce426254ea74862`, includes documentation changes; the exact base compile hashes match the measured CPU identity candidate.

| Command | Top-level passes | Skips / failures | Package test time | Outer compile + test time | Cleanup |
| --- | --- | --- | --- | --- | --- |
| Normal | 2 / 2 | 0 / 0 | 0.012 s | 8.7472 s | Natural, no surviving owners |
| Race | 2 / 2 | 0 / 0 | 1.040 s | 13.6658 s | Natural, no surviving owners |

`TestAgentRouteLocalityMixedWarmColdRetirement` uses the existing four-lane Yamux and HTTP fixture. A warm target becomes busy while a second complete target key remains idle and a third is cold. Concurrent requests retain distinct connection ownership: the second target reuses its own idle connection, while busy and cold requests use independent connections. Retiring the first route preserves its two active bodies and both unrelated targets. Its retained factory creates a fresh connection and entry while the old bodies drain. All five exact payloads complete, all claims return to zero, final retirement empties the pool, and the peer-FIN-aware capacity check reaches zero live stream leases.

`TestAgentRouteLocalityMixedGenerationReplacement` isolates pool ownership using the existing locality helpers and alternate HTTP protocols. A new generation with the same agent ID cannot borrow an old generation's idle entry. Replacing that exact target key and then performing stale old-generation cleanup preserves the new generation and both active body claims. Both bodies complete and release their ownership; final retirement empties the pool. This case does not promise uninterrupted bodies when an actual agent connection is disconnected.

The regressions assert connection and body ownership, key separation, retirement and release conservation. They do not impose exact concurrent lane ordering or uniform fairness. They provide no WAN, throughput, pressure or capacity qualification.

The coordinator checked the frozen file set and all 431 source hashes before and after both commands. The independent saved-evidence audit rehashed the small plan, runner and overlay inputs, matched the recorded source map to the accepted CPU candidate, parsed both complete Go JSON streams, verified their hashes and exact pass counts, and checked natural outer cleanup. It did not rerun the preflight after the tested file was integrated into the actual source tree. The integration receipt records that the copied file matches the tested overlay SHA256 `e191f59dc83191b150a76ee15596e240acebcbed21f9201312713efb0359c13d`.

Both commands use pinned Go 1.26.6, CPUs 8–11, build parallelism 2, test parallelism 1, a 90 s test timeout and a 240 s outer deadline. Offline module settings, shared dedicated caches, 8 MiB stream rejection thresholds and a 4 GiB combined proof / dependency rejection threshold are recorded in the plan. Thresholds are polled rejection bounds. No source archives, executables, caches, databases or credentials are included in this packet.

[The saved audit](audit/AUDIT.json) contains exact counts, timings and hashes; [the plan](provenance/PLAN.json) contains the source closure and commands. Raw normal and race outputs are under `runtime/`, exact tested Go source is `source/agent_transport_locality_mixed_test.go.txt`, and `REVIEW.md` records the coverage rationale. `EXPORT.json`, `EXPORT-LEDGER.json`, `CREDENTIAL-SCAN.json` and `SHA256SUMS` bind the lean publication.

# Public-route reuse: original WAN qualification

The retained production route-reuse change passes the original 96-transfer
steady-state WAN qualification on 2026-10-17. All 96 exact 256 MiB transfers finish without an
HTTP, payload, protocol or event-conservation error; all fifteen endpoint/helper
processes exit naturally. Every paired and pooled aggregate/legacy ratio exceeds
the original 0.90 threshold in both directions. The saved outcome is
`complete_valid_positive`.

The implementation is retained and this scoped original steady-state WAN gate
is cleared.
Both modes use the same new server policy, so these ratios compare aggregate-v1
with legacy under that policy. They do not measure an isolated old-versus-new
causal improvement, or clear HAProxy, tail-latency, broad parity or release gates.
Earlier stopped, negative and incomplete diagnostics remain unchanged.

Cold/retained transfer slowdown remains visible: aggregate retained-download
rows 25 and 41 run at 14,341,440.431861518 and 14,647,608.967473641 bytes/s
(14.3414 and 14.6476 MB/s). The unchanged original gate excludes these
`retained-warmup` rows from its steady-state summary. Their correct completion
does not establish throughput parity for all cold or retained transfers.

| Fresh pair | Order | Download aggregate / legacy | Upload aggregate / legacy |
| --- | --- | ---: | ---: |
| 1 | legacy, aggregate | 0.984944 | 1.006544 |
| 2 | aggregate, legacy | 0.971189 | 1.029307 |
| 3 | legacy, aggregate | 0.988910 | 1.037843 |
| Pooled | nine steady samples per mode/direction | 0.986485 | 1.029365 |

Pooled medians are 36.9323 / 37.4382 MB/s for aggregate / legacy downloads and
39.4898 / 38.3632 MB/s for uploads (decimal MB/s). Pooling takes the median of
all nine steady rates per mode and direction, rather than averaging pair ratios.
The exact rates, formulas and canonical verdict are preserved in
[OUTCOME.json](analysis/OUTCOME.json),
[the canonical summary](evidence/runs/original96-once/results.summary.json) and
[the attestation](evidence/runs/original96-once/h2-stability-attestation.json).

## Retained change

Public requests prefer an idle, usable pooled entry for the same complete route
key across at most four lane lookups. Selection and the request's active claim
are atomic; locks are released before I/O. Busy or cold routes keep ordinary
round-robin selection and existing admission behavior. The complete route key,
agent generation, allowed public lanes, retirement and response-body lifetime
rules remain enforced. Aggregate control lane zero remains excluded. Idle
preference provides neither lane-wide exclusivity nor a fairness guarantee;
an idle hint does not establish TCP congestion-window warmth.

The six exact changed Go files are
[`agent_transport_pool.go`](https://github.com/Kirari04/p2pstream/blob/101d882d2c1c95d771253520fe13b1cf3c4a5a32/internal/server/agent_transport_pool.go), the
new [`agent_transport_locality_test.go`](https://github.com/Kirari04/p2pstream/blob/101d882d2c1c95d771253520fe13b1cf3c4a5a32/internal/server/agent_transport_locality_test.go),
and the existing tests
[`agent_transport_pool_test.go`](https://github.com/Kirari04/p2pstream/blob/101d882d2c1c95d771253520fe13b1cf3c4a5a32/internal/server/agent_transport_pool_test.go),
[`agent_real_transport_test.go`](https://github.com/Kirari04/p2pstream/blob/101d882d2c1c95d771253520fe13b1cf3c4a5a32/internal/server/agent_real_transport_test.go),
[`agent_registry_test.go`](https://github.com/Kirari04/p2pstream/blob/101d882d2c1c95d771253520fe13b1cf3c4a5a32/internal/server/agent_registry_test.go) and
[`direct_transport_pool_test.go`](https://github.com/Kirari04/p2pstream/blob/101d882d2c1c95d771253520fe13b1cf3c4a5a32/internal/server/direct_transport_pool_test.go).
The [exact patch and base identities](source/BASE-IDENTITY.json) bind these bytes
to base commit `bd593420a6905f8922bb96d07ec351483c0df2c3`.

That commit alone is not the tested source identity: the reviewed patch was
uncommitted during qualification. The 430-file ordinary compilation inventory
has digest `b359dc67c290193f68bf33519853df3473e31f9a4d8e718c0bd1ccc282d14947`;
the recorded source reference appends
`+public-route-idle-reuse-source-<that digest>` to the full base commit.
[Focused checks](proofs/focused/PLAN.json) and the
[530-entry runtime freeze](evidence/SOURCE.json) retain the complete byte bindings.

## Workload and proof scope

The original schedule, routing and timed transfer function are unchanged: three
fresh server/agent pairs in legacy/aggregate, aggregate/legacy, legacy/aggregate
order share one origin and routed topology. Each cohort has four download and
four upload warmups, then retained and three repeated steady transfers per
direction. Every transfer is 256 MiB over HTTP/2 at 80 ms RTT. Warmup closes idle
transports after each request; all four aggregate receive pools must exceed
8 MiB at both endpoints before timed work. CPU placement remains server 0–3,
agent/origin 4–7, load 8–15, with proxy `GOMAXPROCS=4`. Historical insecure-TLS,
IP-address and no-SNI semantics are preserved.

The same fresh ordinary, untagged server binary serves both modes. Aggregate
uses the fresh ordinary agent; legacy reuses the exact accepted binary from
`3ec137c3ad3d07028e867042fb00d3933912d7ab` plus qualification overlay digest
`43dbba91e6485b1fd5376c2331614adf8728f32d2aff0aea2ba08362dd0698bb`.
The [legacy reuse proof](evidence/provenance/LEGACY-REUSE.json) and
[ordinary build receipt](proofs/build/build-once/receipt.json) bind binary hashes,
Go 1.26.6 metadata, source inputs and natural build exits. No early-share
selector or observer overlay is carried into the new binaries.

The default readbacks observe CUBIC and `tcp_slow_start_after_idle=1` before and
after the cohort. These are namespace defaults, without per-socket controller
attribution. The canonical lifecycle joins fifteen natural exits; the outer
supervisor reports no forced termination, adoption or remaining owned process,
and all 530 frozen inputs remain unchanged. This standard-mode run makes no
cgroup memory-limit claim. A separate post-cohort namespace scan finds no match
among 588 readable namespace links, with 1,268 permission gaps; it is a bounded
observation, not an exhaustive absence claim. Private endpoint PIDs are not
interpreted as host PIDs.

## Verification history and retained evidence

Focused ordinary normal and race checks each pass 69 top-level tests, 96
including subtests. Six fresh ordinary build/metadata commands pass. The initial
five-case offline boundary command preserves four passing methods and one
fixture failure: its pre/post host samples accidentally shared one dictionary.
A separately named continuation deep-copies that dictionary and passes only the
previously failed method; exact method equivalence records reuse of the other
four passes. The validator, runtime harness and production source do not change
for that correction. Both attempts and their natural-exit proofs remain in
[the exact evidence map](EXPORT-MAP.json); no failed status is rewritten.

The packet retains the exact changed sources, source inventories, runner diffs,
focused/build/offline histories, canonical rows and lifecycle/default/outer
proofs, saved outcome and independent reviews. [EXPORT-MAP.json](EXPORT-MAP.json)
maps each selected source to its exact hash and canonical destination;
[INPUT-DISPOSITION.json](INPUT-DISPOSITION.json) explains omitted prerequisites.
Unchanged binaries/build dependencies are referenced by hash. Binaries, caches,
full source-tree duplicates, endpoint readiness/application logs, payloads and
TLS certificate/key material are excluded. Earlier evidence remains linked in
[PRIOR-EVIDENCE.json](PRIOR-EVIDENCE.json).

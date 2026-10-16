# Rejected direct-transport cache read-hit experiment

The current profile attributed about 12% of sampled mutex delay to the direct
transport cache. The experiment used an RWMutex read-hit path and an atomic
last-use timestamp based on monotonic time since pool creation. Miss creation,
configuration keys, operator connection limits, resource ownership,
invalidation and idle reclamation retained their existing behavior. The
production code was restored after the measurements below.

Both binaries were built from clean 3ec137c archives. They used identical
concurrency/benchmark test and one-line fixture overlays. The candidate alone
received `candidate.patch`; these overlays and executable hashes are recorded
in `provenance.json`. The frozen 3ec137c qualification harness/runner measured
native TLS, H1 tiny GET, the full recorder pipeline, proxy CPUs 0–3 (four logical
CPUs on two physical cores), load/origin CPUs 4–15, GOMAXPROCS=4, a 4 GiB cgroup
without swap, two-second warmup and ten-second timed intervals. Each client
count ran three orders: baseline→candidate, candidate→baseline,
baseline→candidate, each followed by HAProxy. Root exclusively owned benchmark
CPU after direct source-only acknowledgments from Astra and both workers.

Three alternating cache-hit microbenchmark pairs improved median lookup cost
from 88.62 to 37.14 ns/op at parallelism 1 and 108.5 to 46.84 ns/op at parallelism 4.
Both versions allocated zero bytes/objects per hit. This isolated gain did not
translate into a sustained benefit:

| H1 clients | Baseline useful/s | Candidate useful/s | Change | Baseline p99 ms | Candidate p99 ms |
| --- | ---: | ---: | ---: | ---: | ---: |
| 16 | 28,242 | 27,713 | -1.87% | 1.949 | 1.996 |
| 128 | 25,908 | 24,980 | -3.58% | 16.824 | 17.092 |

Every timed response had the expected status/protocol/payload. All 12 baseline
and candidate cells persisted exactly their timed event counts, with zero
drops, client errors or drain/query errors. The candidate completed
1,580,548 verified timed requests. Focused normal/race tests passed for
concurrent creation, hit/invalidation/reclaim overlap, configuration isolation,
pressure recovery, idle rotation and blocking idle close. The earlier micro
result is therefore retained as a diagnostic; it does not justify a production
change when these sustained measurements show no request-level gain.

Raw rows preserve all three passes, latency, origin reconnects, CPU, memory and
FD observations. This is a small local experiment and does not prove the
read-lock pattern is slower on every topology. It does reject this change for
the measured production path. To replay, apply `shared-fixture.patch` and the
saved concurrency test to both clean source archives; apply `candidate.patch`
only to the candidate. Run `BenchmarkDirectTransportPoolCacheHit` with
GOMAXPROCS=4/affinity 0–3/500ms, then the frozen qualification runner with the
recorded binaries at 16/128 clients, three passes, H1/direct only.

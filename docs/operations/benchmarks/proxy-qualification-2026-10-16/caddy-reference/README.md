# Caddy reference diagnostic

This additional control compares a current Go reverse proxy with p2pstream and
HAProxy on the existing tiny-response workload. It does not replace the
predeclared HAProxy throughput/latency gates or qualify other features.

The finite runner is `scripts/qualify-proxy-caddy.sh`. It reuses the committed
`TestProxyQualificationProcess` origin and `TestProxyQualificationLoad` driver,
including payload/status/protocol validation, warmup, timed useful throughput,
latency, CPU, FD and socket observations. Each cohort shares one generated
P-256 certificate, an HTTP/1.1 loopback origin, `/direct/small`, CPU affinity and
load-generator binary. Caddy and p2pstream use separate 4 GiB Docker cgroups
with swap disabled and private disk-backed scratch mounted at `/tmp`. The
runner rejects memory filesystems for scratch and records the host filesystem
and free space. HAProxy remains a host process, so its cgroup memory is not
comparable. The p2pstream process also contains an idle real-agent fixture.

Caddy explicitly enables H1/H2 public protocols, disables automatic certificate
management, redirects, configuration persistence and its management endpoint,
and binds only an ephemeral loopback port. It uses one upstream HTTP transport
with the same explicitly chosen idle-per-host capacity as the candidate.
Active connections remain uncapped. There is no access log or p2pstream policy,
cache or raw-event persistence pipeline in the Caddy control. These are
forwarding-path comparisons, not feature-equivalence measurements. Configuration
comes from the primary [Caddy reverse-proxy reference](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy),
[global options](https://caddyserver.com/docs/caddyfile/options), and
[manual TLS certificate syntax](https://caddyserver.com/docs/caddyfile/directives/tls).

The official [Caddy v2.11.7 release](https://github.com/caddyserver/caddy/releases/tag/v2.11.7)
was selected on 2026-10-16. Its Linux amd64 archive and checksum artifact were
verified against their GitHub asset SHA-256 digests. `release-provenance.json`
records those hashes and the extracted executable hash. The official executable
was compiled with Go 1.26.8; the p2pstream/driver binaries currently use Go
1.26.6. That toolchain difference is an explicit limitation of this diagnostic.
No binary is checked into the repository.

Both test binaries must be built from clean archives of their immutable source
commits, with no production or test overlays. The runner requires the exact
committed qualification harness file, verifies it against both source commits,
copies it into the evidence directory and records its hash alongside the source
commits and executable hashes. It rejects a dirty or mismatched harness. The
local one-second startup smoke predated this stricter provenance check and is
only a startup/cleanup check, not performance evidence.

The retained full replay uses the committed idle-128 checkpoint
`3ec137c3ad3d07028e867042fb00d3933912d7ab`, built from a clean archive, for
both candidate and driver, with `--idle-per-host 128`. The full run is three rotating
Caddy/HAProxy/p2pstream cohorts, two-second warmup and ten-second measurement,
H1/H2 clients at concurrency 16 and 128. Raw config, normalized Caddy JSON,
version/build metadata, provenance and results are retained under ignored
`tmp/caddy-reference/run.*/`. The script removes only its own containers and
child processes when it exits.

The first full cohort used a 256 MiB `/tmp` tmpfs and is excluded from the
reference comparison. Its fourth candidate workload filled the fixture SQLite
storage after approximately 1.19 million persisted events across the process;
the event drain failed and 2,773 raw events were dropped. Client payloads still
validated, but the conservation check stopped the run and cleaned its children.
All 12 rows, including earlier rows using the same wrong storage, are labeled
under `fixture-failure/` and are not sustained comparison evidence. The corrected
runner uses disk-backed scratch while retaining the same cgroup memory limit.

A subsequent disk-backed run was interrupted because another worker's Go
build/test/race work overlapped approximately 2026-10-16 02:19:50–02:22:45
Europe/Zurich. Its 31 completed rows are retained under `cpu-overlap/` only as
functional/event diagnostics and are excluded from the timing comparison.
The fresh reference pass requires direct SOURCE ONLY acknowledgments from all
workers and records UTC start/end timestamps for every load subprocess.


## Retained clean comparison

The 36-row run completed from 2026-10-16 00:25:03 to 00:32:21 UTC
(the per-cell timestamps in `results.jsonl` are authoritative). All three
passes validated payload length/content, status and HTTP version with zero
request errors. All 12 candidate cells also persisted exactly every timed
request event, with zero queue drops and no drain/query errors. The root held
exclusive benchmark CPU after direct source-only acknowledgments from both
workers and Astra. The next worker test handoff followed run completion.

These are medians of three rotating orders, in useful responses per second:

| Public protocol / clients | p2pstream | Caddy | HAProxy | p2pstream / Caddy | p2pstream / HAProxy |
| --- | ---: | ---: | ---: | ---: | ---: |
| H1 / 16 | 27,530 | 27,306 | 64,941 | 100.8% | 42.4% |
| H2 / 16 | 20,491 | 18,620 | 35,645 | 110.0% | 57.5% |
| H1 / 128 | 26,057 | 26,269 | 79,019 | 99.2% | 33.0% |
| H2 / 128 | 23,358 | 20,062 | 36,681 | 116.4% | 63.7% |

The candidate is close to Caddy throughput on these four direct tiny-GET
workloads while retaining its policy and recorder pipeline. This does not
establish upload, streaming, real-agent or broad modern-proxy parity. The
predeclared HAProxy 80% throughput gate remains unmet.

At 128 H1 clients, median p99 latency is 16.558 ms for the candidate versus
14.378 ms for Caddy and 4.580 ms for HAProxy. At 128 H2 streams it is
11.032 / 12.699 / 5.365 ms respectively. `summary.json` includes pass ranges,
latency, CPU, RSS, FDs and connection counts; individual rows remain available.
No end-to-end improvement is attributed solely to the SQL microbenchmark:
this immutable checkpoint includes both bounded SQL insertion and idle-128.

The proxy affinity is four logical CPUs on two physical cores, with load and
origin on the remaining logical CPUs. Caddy and candidate H2 loads multiplex
one client TCP connection; the existing HAProxy driver uses two. The
[HAProxy architecture guide](https://docs.haproxy.org/3.2/management.html#2)
explains that each connection is served by one worker. Its c16 H2 CPU consumption is
9.88–9.92 CPU-seconds per ten-second measurement; c128 is 9.99–19.46,
with throughput 36,204–55,483 useful/s. Those cells do not measure HAProxy's
fully utilized four-thread ceiling. H1 uses 16 or 128 client connections and
consumes roughly 35–40 proxy CPU-seconds per ten-second measurement across
controls. The H1 HAProxy throughput gap therefore remains decisive.

RSS and cgroup observations are descriptive, not a feature-normalized memory
ranking. Bind-mounted executable pages can already be charged to the host's
file cache, HAProxy is not in a separate cgroup, and the candidate contains an
idle real agent. Generated certificate/key contents, database files and
executables are excluded from committed artifacts. The exact runner and
qualification harness SHA-256 hashes are in `provenance.json`; the harness is
the committed source at the recorded immutable checkpoint.

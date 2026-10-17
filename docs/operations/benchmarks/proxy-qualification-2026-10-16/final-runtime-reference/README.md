# Fresh direct HTTPS reference at `248bad0`

All 36 rows completed with exact successful payloads, request-event conservation
and zero errors. Candidate throughput was 101–120% of Caddy and 34–67% of
HAProxy. All four predeclared HAProxy throughput gates (at least 80%) failed.
The original wrapper exit is one for that rejection; the workload child exited
zero and cleanup completed naturally without forced groups or containers.

| Public protocol / concurrency | Candidate requests/s | Caddy requests/s | HAProxy requests/s | Candidate / Caddy | Candidate / HAProxy |
| --- | ---: | ---: | ---: | ---: | ---: |
| H1 / 16 | 29,287 | 28,094 | 67,352 | 1.04249 | 0.43484 |
| H1 / 128 | 27,829 | 27,492 | 82,784 | 1.01225 | 0.33617 |
| H2 / 16 | 21,929 | 19,285 | 37,592 | 1.13714 | 0.58335 |
| H2 / 128 | 25,115 | 20,917 | 37,660 | 1.20068 | 0.66689 |

Values are three-repeat warmed medians. Every public connection uses HTTPS,
including H1. All proxies forward to the same plain HTTP/1 loopback origin.
Each timed H2 row reuses exactly one validated prewarmed client connection;
the HAProxy configuration permits 256 streams. This matches connection topology
and does not measure HAProxy's maximum throughput with arbitrary connections.
The fixed cohort uses three rotating proxy orders, two seconds of warmup and
ten timed seconds per cell, concurrency 16/128 and 128 idle upstream connections.
Closed-loop saturated tail values remain in `summary.json` as diagnostics;
they do not establish latency parity at equal offered load.

Candidate and driver are the same Go 1.26.6 test binary built from an immutable
clean archive of `248bad099682878fe398c0ebaad235787ef47858`, without overlays.
Caddy is the pinned official v2.11.7 binary, built with Go 1.26.8. HAProxy is
3.2.9-1ubuntu2.2; exact binary hashes, build information and effective configs
are preserved. Proxy CPUs 0–3 are four logical CPUs on two physical SMT cores;
load and origin use CPUs 4–15. Candidate and Caddy run in separate 4 GiB Docker
cgroups without swap. HAProxy uses the same CPU set on the host and does not
have the 4 GiB container constraint. No memory or capacity parity follows from
these throughput measurements. Temporary storage is owned ext4 throughout.

The H1 driver uses an IP URL without explicit TLS SNI; overriding the HTTP Host
header does not set SNI. This cohort did not record peer-certificate fingerprints.
A later correctness smoke using the same production fixture/client behavior
observed the app's generated fallback certificate, while the native control
served the configured shared fixture certificate. The original reference
therefore does not establish certificate-matched handshake behavior. Its raw
measurements remain unchanged. The later native attribution uses explicit SNI
and verifies the shared certificate in each arm; it is a separate experiment
and cannot be compared causally with these no-SNI reference cells.

Compilation completed in 11.36 seconds. The workload took 439.05 seconds.
The complete manifests preserve actual commands, source tree/archive identity,
Go environment, binary identities, timestamps, child exit and cleanup outcomes.
Independent review in `root-review.json` verifies all 36 rows, payload and event
conservation, protocol and H2 connection topology, hashes and lifecycle.

The compiler wrapper received a bounded, owned-process-group lifecycle before
this run. `helper-history/` preserves its original source and correction.
`build-contracts/527036/` is an original failed harness attempt: its isolated
execution namespace omitted `Path` and failed before spawning a compiler or
workload. Its error was initially masked by a missing manifest. The narrow
correction adds that dependency, retains evidence and exposes the original
error. The single corrected nine-case run in `527036.rerun1/` passes natural
completion, nonzero exit, timeout/descendant, spawn/signal/cleanup cases. The
ordinary accepted compile and workload required no forced cleanup.

This supports Caddy-class saturated throughput for these direct tiny-GET HTTPS
workloads. The HAProxy target, original agent WAN regression, near-saturation
latency and final pressure/recovery checks remain open. See the separate
[moderate-load comparison](../final-runtime-moderate-latency/README.md).

`SHA256SUMS` covers files actually exported here. Raw result/configuration/log
bytes are unchanged. Source archives, executables, databases, ready/stop files
and generated TLS credentials are omitted. Saved paths and upstream manifests
may refer to those omitted runtime files. `export-map.json` records each copied
file's original path and identity; original failures are retained unchanged.

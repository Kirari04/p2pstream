# Native public-listener header burst

This finite safety diagnostic sends one 64,077–64,078-byte HTTP/1.1 request
with 8,000 distinct empty header keys on each established TLS connection.
The child uses the production public TLS listener and direct forwarding
pipeline, default native Go header parser and 64 KiB header policy. The
origin holds admitted requests for three seconds. The proxy and an idle
four-lane agent fixture share a 4 GiB Docker cgroup with swap disabled and
1 GiB of touched background ballast. The origin and driver are outside that
cgroup. Proxy CPUs are `0,1,2,3` (four logical CPUs on two physical cores),
with driver/origin work on `4-15`; `GOMAXPROCS=4` and Go 1.26.6.

The TLS setup phase has one shared two-second deadline so established sockets
are released into the header burst before the production five-second
read-header deadline expires. Requests are released together after setup.
TLS failures include deadline cancellation and admission refusals; they are
not all attributable to one cause. Connection admission counters are measured
from before TLS setup. The 1,536-client step attempted 831 TLS handshakes and
left 705 unattempted when that shared deadline expired.

| Requested TLS connections | Established | 204 | 503 | Request I/O errors | Sampled peak cgroup bytes |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 128 | 128 | 128 | 0 | 0 | 1,449,689,088 |
| 256 | 256 | 256 | 0 | 0 | 2,143,662,080 |
| 512 | 512 | 237 | 275 | 0 | 2,822,168,576 |
| 1,024 | 834 | 156 | 678 | 0 | 3,176,906,752 |
| 1,536 | 585 | 145 | 440 | 0 | 3,216,990,208 |

All steps recorded zero cgroup OOM/kill events, a live child, 32/32 subsequent
small requests and zero remaining public connection, handshake, request and
response reservations. The larger steps recorded explicit connection and
request admission refusals. They do **not** establish 1,536 concurrent parses
or successful requests. No parser allowance was lowered from this result.

The sampled header phase includes parsing, forwarding and retained decoded
maps; it does not isolate parser memory. Twenty-millisecond samples can miss
short peaks, so cumulative kernel `memory.peak` is also preserved. Heap and
RSS gauges have different accounting and timing from cgroup charge and should
not be substituted for one another. Each staircase step reuses the same
process, including heap and origin-pool retention from preceding steps. Final
cgroup charge was 3,063,123,968 bytes: this demonstrates functional recovery
and lease release, not return to initial resident memory. The retained direct
connection reservation after most steps is an idle origin pool.

`failure_kinds_cumulative` is queried after an explicit recorder drain, before
the current step's 32 recovery requests. An empty error kind means success;
preceding steps' recovery requests are included. Counters are cumulative and
admission misses can differ from final 503 counts because acquisition can be
retried. The result schema records drain/query errors rather than silently
claiming complete counts when those operations fail. This run reported none.

## Provenance and replay

Production source: `a2063ee287d309a6d3ed7283986b5c3a0035fb92`.
The runner archives that commit and overlays only
`internal/server/public_header_burst_qualification_test.go`; concurrent Yamux
or qualification edits do not enter this binary. The production archive
already contains the supporting qualification sampler and real-agent fixture.

```sh
scripts/qualify-proxy-headers.sh a2063ee287d309a6d3ed7283986b5c3a0035fb92
```

The finite script compiles once, runs one child plus an outside driver/origin,
and cleans up its own container. No persistent dev server is started.
The exact run's harness SHA-256 is
`2b40f6f2d457555a141facfcd35d3d8df20bc8421f09e7570fd0c27e5b0d6b1a`;
binary SHA-256 is
`f555e285b893e15f19873a0945d780ce7a96c643e8f17821154d81dc708a15d2`.
Build-directory/debug metadata can change the binary hash on replay. Sanitized
results SHA-256 is
`d3351154bb9fa67f8966117267a61380657aa19a129066c665a6a109ab1f06cf`.
Raw logs and frozen build source remain locally in
`tmp/header-burst/run.4WYp5C/`; generated private keys are not checked in.

An earlier local run, `tmp/header-burst/run.HqENCv/`, used serial TLS worker
retries without one shared setup deadline. Early connections could exceed the
production read-header timeout, so its high-concurrency write errors are
confounded. It is superseded by this corrected run and supports no stronger
capacity conclusion.

## Separate memory-fixture recovery validation

`memory-fixture-validation.json` records the focused 512 MiB cgroup tests from
a clean archive of the same production source with only
`internal/server/public_memory_stress_test.go` overlaid. The fixture baseline
was reduced from 376 to 352 MiB to leave room for application setup below the
controller's 75% healthy-exit threshold. The 440 MiB pressure stimulus,
rejection checks and no-OOM checks remain unchanged. Recovery waits for both
a successful reservation and a healthy sample instead of stopping on the
first successful reservation.

The pressure case admitted 24 sockets, reached 461,987,840 of 536,870,912 bytes,
rejected six acquisitions and recovered healthy at 370,282,496 bytes. The
separate LOWAT case recorded 5,996,544 bytes of socket growth. Both passed.
Evidence SHA-256:
`a7c40c88be7bb77bd074d645a461614fc60e27f0aab2c6e9f190d9535e3ab732`.
This is a test-fixture correction; it does not alter production thresholds.

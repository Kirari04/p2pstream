# Aggregate agent idle-stream lifecycle

This trace follows the one-agent external 1,536-H1 capacity wave through the
production idle-connection lifetime without disconnecting any physical tunnel
session. Candidate `cad0850603ea61ba889cb49ace9caaf858a2a150` includes
protocol checkpoint `2aa32d31cb8cfc236a4f0b5f2dc889bd22f7d466`. Both the
server and agent ran in distinct 4 GiB cgroups; the server carried a 1 GiB
ballast.

The capacity and 512-request functional recovery rows passed with exact event
conservation and zero OOM/OOM-kill deltas. After the public wave, the server
HTTP transport deliberately retained 1,536 reusable Yamux streams, 384 on each
of four bulk lanes. Agent `InUse` was therefore also 1,536 at the one- and
five-second samples. Both sides returned to their connected idle baseline by
30 seconds and remained there at 35 and 65 seconds:

| Offset | Agent `InUse` | Bulk-lane streams | Agent cgroup | Agent RSS | Server receive-window owners |
| ---: | ---: | --- | ---: | ---: | ---: |
| 1 s | 1,536 | 384 / 384 / 384 / 384 | 71,733,248 B | 75,137,024 B | 5 |
| 5 s | 1,536 | 384 / 384 / 384 / 384 | 71,028,736 B | 75,550,720 B | 5 |
| 30 s | 0 | 0 / 0 / 0 / 0 | 68,403,200 B | 77,758,464 B | 5 |
| 35 s | 0 | 0 / 0 / 0 / 0 | 67,559,424 B | 77,824,000 B | 5 |
| 65 s | 0 | 0 / 0 / 0 / 0 | 67,264,512 B | 77,946,880 B | 5 |

All five physical lanes remained connected. The fixed aggregate session pool
remained 10,485,760 bytes, as intended; this workload did not grow it. The
server's five receive-window owners likewise remained at the physical-session
baseline. These retained session promises are separate from per-request stream
ownership.

The fixture then opened 128 new requests, closed only the server's idle HTTP
transports, and left the physical Yamux sessions running. Agent `InUse` and
every server bulk-lane stream count had returned to zero before the first
post-close sample and stayed zero through 35 seconds. This demonstrates the
explicit pressure-reclamation path without using session teardown to hide a
leak.

Go RSS did not contract with logical ownership. The server RSS rose from about
1.33 to 1.36 GB across the full trace, while cgroup socket memory returned to
zero. The result therefore establishes reservation, stream and socket cleanup;
it does not claim that the Go runtime immediately returns heap arenas to the
kernel.

`results.jsonl` contains the original candidate and HAProxy rows plus every
lifecycle sample. `summary.json` extracts the candidate trace. `manifest.json`
records exact source, binary, runner and result hashes, and `SHA256SUMS`
protects the checked-in evidence.

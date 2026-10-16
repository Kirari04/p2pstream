# Raw TLS and aggregate Yamux transport diagnostic

This comparison tests a transport ceiling and attempts to reproduce the proxy's
direction-transition slowdown outside its HTTP integration. Removing Yamux also
removes its framing, credit traffic, scheduling and logical-stream lifecycle.
Raw TLS keeps an origin TCP relay for each data lane throughout conditioning;
Yamux creates fresh conditioning streams and then retains four logical streams.
A contrast would therefore localize a broader transport/lifecycle difference,
not prove that any single credit mechanism caused it.

The fixed workload has two fresh alternating pairs: Yamux then raw TLS, followed
by raw TLS then Yamux. Every arm uses five persistent authenticated TLS sockets,
including one control lane and four data lanes, and sixteen 256 MiB transfers.
Each arm receives fresh server, router and agent network namespaces. The routed
topology applies 40 ms delay in each direction and 1 Gbit/s; server CPUs are
0–3, agent CPUs 4–7 and load CPUs 8–15, with GOMAXPROCS=4 at both endpoints.
These are logical CPUs; the server set occupies two physical cores with SMT.
Namespace freshness follows each helper's boot ID, PID and process start ticks,
with three distinct namespace inodes during each arm and recorded helper joins
before the next generation. Linux may recycle an inode after its namespace is
destroyed; such reuse does not identify the same live namespace.

The schedule conditions four download lanes and four upload lanes, then opens
retained download A on lane 1 and upload B on lane 2, followed by download C on
lane 3 and upload D on lane 4. The last four phases upload on A, download on B,
download on C and upload on D. All four retained lanes stay open through the
last phase before joined cleanup. Transfer timing includes logical setup and
payload completion acknowledgment in both arms. TCP_INFO is collected after
each transfer, with separate all-five socket checks at readiness and post16.

The agent uses the production relay function, including its joined cancellation
fix. Both sides use 32 KiB relay I/O. Aggregate Yamux uses the production byte
and allocation window bounds, growth code and timers, with a disclosed fixture
incoming bound of 16 on agent data lanes. Its endpoint-wide receive-pool ledger
has a 2 GiB limit. A separate 64 MiB ledger tracks enumerated working estimates;
it does not bound TLS runtime heap or kernel socket memory. Actual heap, file
descriptors and socket observations are recorded separately.

The binaries come from base ccb9c9cb683b555044033d68ff59112aa4bf21d9 plus the
four-file tagged overlay producing source tree
40e1046c2f68ee3d545dfd582cd424c2f3604b2a. The unique build tag is
p2pstream_transport_ceiling; ordinary qualification counters and growth tracing
are disabled. The absolute Go 1.26.6 toolchain and all four temporary variables
on owned ext4 storage are recorded in the build evidence. The corrected harness
checkpoint is 248bad099682878fe398c0ebaad235787ef47858. Its runner SHA-256 is
87cf7c1b008c86e5a416713f34d5c45d59774efa6d6f038b895f6da681e1a70b;
its wrapper SHA-256 is
beed4036380ee6da291259f0dbba43b1f46ddc7ed7515ef5a8c7924aec8b3b54.

All 64 payload hashes, sequence, configuration, source/binary identities,
stable five-socket tuples, pool growth, exact ownership ledgers and natural
shutdown checks are prerequisites. Each Yamux arm must have conditioning
download median at least 29.7 decimal MB/s, at least one of phase indices
8/10/13 below that floor, and reused lane-3 download at index 14 above it.
Every raw postwarm download at indices 8/10/13/14 must achieve at least 90% of
its own conditioning download median. Both alternating pairs must agree.

Healthy standalone Yamux is nonreproduction and does not clear the original WAN
regression. Both transports stalling or mixed results are inconclusive. This is
neither the original aggregate-versus-legacy acceptance gate nor a production
tuning retention decision.

Fixture validation preserved three original nonzero outcomes: an agent EOF
classification failure after the first aggregate arm, a validator field-name
mismatch after corrected aggregate endpoints passed, and a topology JSONL
formatting failure after both arms completed all 32 low-RTT smoke transfers.
The last run's actual payloads, lifecycle snapshots and six fresh namespaces
pass independent review. The one-line formatter correction was validated
offline against semantically identical topology objects and the saved 32/6
records, preserving the original exit failure. This derived fixture correctness
pass makes no performance claim; the full 80 ms run verifies the formatter
end-to-end.

The first full-run attempt completed its first 32 transfers, then stopped before
the next pair's payloads because the kernel recycled a destroyed namespace
inode. Its freshness check incorrectly required inode numbers to stay unique
after teardown. That partial run remains unchanged and cannot be combined with
a later run. The corrected helper lifetime check must pass deterministic reuse
cases and four actual endpoint-free topology generations before the complete
64-transfer comparison starts again. The final topology preflight passed all
four 80 ms generations, with twelve distinct process lifetimes and eight inode
numbers, including actual post-join inode recycling. Its stderr was empty and
both inner and top-level statuses were zero. Ten analyzer contracts and an
actual disappeared-helper rejection passed. Independent review verified every
one of its 130 indexed files and byte-identical extracted functions, then
checked modeled PID reuse with a new start time and rejection of a noninteger
PID. These are fixture checks without endpoint traffic or performance evidence.

An earlier topology-only attempt is preserved as inconclusive: durable statuses
were zero but the execution interface reported one, and its pre-fix stderr had
post-termination process-disappearance tracebacks. The recorded minimal
no-workload commands reproduce the interface status difference when explicit
exit is combined with shell errexit in this environment. The accepted fresh
topology preflight completed naturally with consistent zero statuses.

The complete diagnostic finished all 64 transfers in 541.45 seconds. Its
classification is `complete_timing_rejection`: neither standalone Yamux arm
reproduced the required transition slowdown. All correctness, source,
configuration, socket, growth and joined-cleanup checks passed. The original
nonzero exit is the predeclared timing rejection, with no timeout, interruption
or forced cleanup. All eight terminal reports had zero remaining pool and
working ownership, no errors or reconnects, and joined logical and physical
relays. No owned helper survived.

| Pair | Yamux fresh DL median MB/s | Yamux minimum transition MB/s | Reused lane-3 MB/s | Minimum raw postwarm/warm ratio |
| --- | ---: | ---: | ---: | ---: |
| 1, Yamux then raw | 34.373 | 34.331 | 35.386 | 1.003526 |
| 2, raw then Yamux | 34.614 | 34.329 | 35.113 | 0.989482 |

Rates use decimal MB/s. Both Yamux transition minima exceed the 29.7 MB/s
screen, so the required slowdown is absent in both fresh pairs. Both raw arms
pass their own normalized 0.90 floor. Actual RTT ranged from 96.128 to 96.538 ms.
Aggregate receive growth was exercised at every receiving endpoint, reserving
460,062,720–476,446,720 growth bytes per endpoint before joined release.

Independent review verified the eighteen indexed core files, all 64 payload
digests and the full chronological schedule, twelve lifecycle snapshots, four
joined topologies, twelve fresh helper lifetimes, and all eight terminal
reports. Incremental and pure-revalidated result/snapshot bytes match. A fresh
execution of the exact saved final analyzer produced a byte-identical summary
and the expected timing-rejection exit.

This is a clean nonreproduction outside the HTTP integration. It does not clear
the original aggregate-versus-legacy WAN regression or establish performance
parity. The next diagnostic adds native HTTP/1 origin and the production agent
HTTP transport, including its ordinary idle-connection lifecycle, before any
public ReverseProxy/H2 layer is added.

`complete-64/` contains the accepted complete raw diagnostic, its original
timing-rejection exit, saved harness and frozen source/binary manifests.
`history/` preserves each earlier failure, contract and partial attempt under
its own directory; no partial runs are combined. `root-review/` records the
independent hash, source-replay, lifecycle and exact-analyzer checks.
`frozen-bundle/` contains the portable tagged patch and build/check logs.

`SHA256SUMS` at this directory's root covers only files actually exported.
Nested original checksum indexes and manifests remain byte-identical and may
also list omitted executables or runtime files; use them as original
provenance. Executables, Git object/index files, source archives, generated TLS
credentials, authentication-key files and readiness/stop files are excluded.
All exported files were checked against actual fixture authentication values
and private-key markers before copying. `export-map.json` records each copied
file's original path and identity. No successful status was substituted for
any original failed attempt.

# Current WAN continuation review

The current production reproduction uses source `8ac4c8d`; subsequent `15b79d0`
only commits the reviewed evidence. The initial 32-transfer cohort is separate.
Fresh alternating 96-transfer qualification fails download pairs 1 and 3 at
0.8109937458 and 0.8110017765. All exactness, event and natural-lifecycle checks
pass. No production fix or gate clearance is established.

## Protocol derivative architecture

Use the existing full production fixture and immutable current endpoints.
Only the public load transport changes between forced H1 and H2. Both arms use
verified RootCAs, ServerName public.test, independently bound leaf DER SHA-256,
observed HTTP major and ALPN. Fresh owned topology and endpoint processes are
required for each arm. Keep the original schedule, payload, public policies,
adaptive ownership, 8 KiB reader and 30-second upstream idle timeout.

Bounded test-only httptrace callbacks are installed through the existing public
handler context. Do not wrap ResponseWriter or change body copy, framing,
buffers, flushes, lane windows or credit publication. Trace sequence, actual
origin identity, GotConn and PutIdleConn records must be correlated independently
to every raw row. Missing or repeated callbacks and unexpected reuse make the
observation invalid or inconclusive. A new connection does not itself prove idle
expiry, and absent PutIdleConn is not proof of failed reuse.

Go source checkpoint approved for build and focused tests:

- Base overlay: b2c90d2ee6bf9ade304163795022071b4d368b61f0e9d5bc73d0e52d2569538b
- Corrected helper: b0b6d037d51892e89d39e6f55107f73c30647ad9a20283a026007fd69b3be42d
- Test server binary: 60667aa9bbe7375de77d341c83cd8576301366f26cda25fb980fa7b813ba405e

Focused normal and race checks pass. Initial H1 ALPN fixture failures are
retained; the test fixture was corrected to advertise production-equivalent
protocols, while the H1-only negative fixture advertises only http/1.1. No ALPN
or certificate acceptance criterion was relaxed.

## Required runner review before traffic

Reuse the reviewed private user/PID/mount/network supervisor. Refuse an existing
output directory; bind source/binary/harness/certificate hashes before and after
each arm; retain exact natural exit status and group/namespace joins. Forced
cleanup invalidates the result. Do not start a second arm over an unjoined first
arm. Check complete ordered rows, finite rates and payload/wall rate identity,
exact events, protocol, certificate and all unique lifecycle exits. Preserve
complete failed timing attestations after checking correctness and cleanup.
Inconclusive reuse cannot exit successfully. Smoke has its own explicit reduced
schedule and cannot establish sustained performance.

The runner and supervisor passed review after correcting the exact jq TLS
filter, restoring profile-environment rejection and cgroup evidence, binding
approved binaries, and separating timing failure from natural cleanup. Eight
validator controls and the exact positive/negative jq filter pass. The first
tiny smoke stops before endpoints or traffic because relative binary paths do
not survive the runner's directory change; both outer namespaces join naturally.
That attempt remains unchanged. The reviewed path-only correction
`8e31d7c32995df85fbfd05346dd1317fba53b201c70761aa9b38287398bf5789`
passes a no-traffic relative-path contract. One fresh tiny-smoke-2 is authorized.

Tiny-smoke-2 completes 24 exact responses with verified H1/H2, pinned certificate,
event conservation and wholly natural cleanup, but the original classifier exits
one for possible idle expiry. The offline audit identifies a classification bug:
the unchanged original fixture explicitly closes idle transports after warmup
sequences 1–8. Post-close sequences 9–12 visit each of the four lane-specific
keys for the first time, so none has a retained reuse opportunity. All observed
earlier same-lane revisits cross an explicit idle close. This is not evidence of
a production connection-lifetime fault. Original summaries, attestations and
nonzero statuses remain untouched.

Pending independent source confirmation, the next narrow diagnostic correction
must distinguish first post-close key visits from actual retained-key revisits.
Record elapsed age from the prior PutIdle callback and exact origin identity.
An extra smoke-only sequence 13 can revisit the sequence-9 key without changing
the sustained 16-phase schedule, idle timeout, policies or performance gates.
Its diff and classifier contracts require review before traffic.

Independent source review confirms the explicit-close and lane-key mechanism
(`POOL-LIFETIME-INVESTIGATION.md`, SHA-256
`34a085190527e63bfbb954b2a1f336340cb25befa61e23116a12826f3dc2d10e`).
The narrow classifier correction and smoke-only probe pass reviewed contracts.
Tiny-smoke-3 completes 26 exact responses/events with the same verified leaf
certificate, expected H1/H2 ALPN, unchanged binaries, and natural cleanup. Both
sequence-13 probes reuse sequence-9's exact origin ID after 3.431 seconds (H1)
and 3.416 seconds (H2). Astra independently rechecks raw hashes, conservation,
pre/post manifests and reuse timestamps.

One fresh sustained H1-then-H2 cohort is now authorized: original 16 phases per
arm, 256 MiB warmup and steady payloads, three timed repetitions, 8 MiB retained
pool requirement, configured 80 ms RTT. No smoke probe, retry, longer idle
timeout or gate change is authorized. Preserve all timing and reuse failures.

One matched protocol cohort
can localize an investigation boundary but cannot clear the original WAN gate,
establish a runtime mechanism, or qualify modern-proxy parity.

# Matched verified WAN read-wait trace diagnostic evidence

This package preserves a bounded server-side Go execution-trace diagnostic
on production source `8ac4c8df8237e37777ba2fac0da6f949da17bdea`. It
contains completed tiny H1 and H2 evidence, the original failures, separate
offline reductions, independent partition reviews, and exact source and
contract provenance. It does not qualify a runtime change, establish a cause,
or clear the original WAN release blocker.

## Evidence scope

The proposal and its amendment reject `runtime/trace.FlightRecorder` as a
strict memory/output bound. The implemented diagnostic observer has a fixed
64 MiB raw trace file cap, while the owned process cgroup is the only hard
total-process bound. A
complete trace can locate handler-goroutine state intervals and wait stacks. It
cannot by itself identify peer credit, origin delivery, TCP buffering, or a
background-goroutine cause.

Historical matched-copy evidence placed the server in no finite child cgroup,
and a nested-path recording bug omitted the inherited scope's exact historical
limits. A current read reports the shared inherited Codex scope as unlimited.
That current observation does not reconstruct the historical limit or assign
shared-scope memory to the server.

All three scope-only prerequisite attempts remain separate:

- Attempt 1 failed before the dummy or unit started because the private user
  namespace could not reach the user scope bus. It rejects that launch route,
  not the 4 GiB limits.
- Attempt 2 demonstrated host-side delegated scope creation and namespace-local
  `write(0)` migration, then failed its host/local PID comparison. The dummy
  exited 42 before the required server-only interval; acceptance is false.
- Attempt 3 corrected PID provenance and passed one server-only dummy interval
  with `MemoryMax=4294967296`, `MemorySwapMax=0`, zero high/max/OOM events,
  exact host/local identity binding, natural joins and no forced cleanup. This
  proves the finite launch prerequisite only. It is not endpoint, trace, WAN,
  pressure, or performance qualification.

The observer history preserves the pre-Astra source, the superseded
`b964fd8f...` checkpoint and the final source-only checkpoint
`327a70507ddb94af99e1b2bc2ab7b9037a18b93daebfaef9acb51a7c0bd80faf`.
The final observer patch is
`6ced006fc0aa8284898d7b696bb24b983a6162994513c520e3626c74e64d0c57`.
Focused normal and race attempts both have inner and outer status 0, unchanged
pre/post source inventories, pinned Go provenance and preserved tool-envelope
records. They built no endpoint binary and ran no traffic.

The key immutable indexes are attempt 1 `SHA256SUMS` `bd0947fc...`, attempt 3
`manifest.sha256` `cddce01c...`, focused checkpoint `accb27f1...`, focused
evidence manifest `9eec0cb5...`, pre-Astra source index `64e6e6db...`, and
superseded `b964fd8f...` checkpoint index `07019a30...`. Attempt 2 has no
all-artifact manifest; its complete current directory is bound file by file in
the package allowlist, while its `RESULT.md` and source index retain hashes
`c0cac0eb...` and `097ec778...`.

The audited endpoint build is complete. Manifest
`42f3d7660883425391dbf01f83975957873701a65ea8daa19041a4afada15b3c`
binds source, module and toolchain pre/post checks, the exact build command,
build-directory Go environment, output build info, outer status 0 and server
binary hash
`47981bcd767fb7eb3e1456ae4d99c6d80d9fffc62eecb6be0fb67f9245f8b2aa`.
The provenance is allowlisted; the 58.9 MB binary is not. That build step
executed no endpoint.

Analyzer checkpoint
`117d177a15ae4b47ac48d4e41a5886659ced823a2f0ed6bd85bb60614abcae9b`
is frozen with its source history, schema, fixtures, nine synthetic contracts,
one real pinned-Go parser control and independent Astra audit. Both direct test
paths return 0, while both outer execution envelopes report 1. The unexplained
discrepancies, exact outer commands and all saved statuses remain unchanged.
The independent audit recomputes the real handler region as 20,113,088 ns:
1,536 Running, 1,152 Runnable and 20,110,400 Waiting. The control-channel wait
has an unknown stack, so `attribution_complete=false`. This is local parser
conformance, not endpoint or WAN evidence.

The final integration source checkpoint manifest
`fbaad6fdaf62debb2e2d34481bcc77c229bc28416940c9b92d735a673b600a29`
binds 20 runner, supervisor, validator and contract source files. The focused
integration checkpoint
`4ba27bc41e87a4bbcb73bab658f37b1a3e52e6d57fa1ed698f7d2a470feb8163`
binds static/reconstruction, five resource, three traced-transfer and 24 arm
cases. Every direct status and the saved outer status is 0, and `FINAL` is
complete. The tool envelope nevertheless reports exit 1 with empty output.
The exact outer command, saved statuses and unexplained discrepancy remain
preserved without a rerun. A separate malformed read-only inspection is also
preserved; it partially printed statuses before a shell quoting error and had
no effect on the contract results. These contracts executed no endpoint or
traffic.

The focused result tree has 1,041 files and 4,144,234 bytes, including its
1,040-entry manifest. The package retains every attempted case
input, output and status, its manifest, checkpoint, command and envelope
sidecars. It omits only `static/rebuilt.sh`, whose
`127da1ef1f1a8923fa00b218bf8458cb83f67f8c2b97f5fb2a353162c4624ed6`
hash is identical to the canonical generated runner in the final source
checkpoint. The 1,039 other manifest entries remain selected.

The first tiny smoke is a frozen preflight failure. H1 returned 2 before scope,
endpoint or traffic creation; the coordinator returned 1 and withheld H2 with
status 125. Its sole failed predicate compared the same canonical `go.mod`
through unequal lexical paths. The failure snapshot manifest is
`3d0a453cc596f0fa4ea95d5c362cd028e4bc33067f08049ce3929fe3d6440e4f`.
The original outputs remain unchanged. A separate correction snapshot
`5c3d01456b9de0a158f3d42095edd5bdbc015334877d10ca7fc3f735aa13f6a9`
canonicalizes that path. Its static contract has direct status 0 and complete
`FINAL`, while its tool envelope reports 1; that discrepancy is preserved.
The correction ran no endpoint or traffic and does not repair the failed smoke.

The second tiny smoke is also frozen and rejected. It completed eight exact H1
conditioning rows, then rejected the pre-health schema before sequence 9 and
before any trace. H2 never started. Inner endpoint cleanup was forced and
invalidates success; individual endpoint exits and a post-resource snapshot are
absent. The outer supervisor and owned scope joined naturally. Its checkpoint
manifest is
`4016de5e3c3d16a29892ac8869ce1915074fc7a8b53cfdbd3973f9a7223fc5e8`,
artifact manifest is
`43f9f75ea7c67f4d2a7975612b78a181f622b09c73d2e59794a860c9ad6eb86f`,
and offline audit is
`840a887af6602f5626a0f9733b7576707a8ab2bbc8583833450ec0df976f7f61`.
The package retains the raw rows, lifecycle, scope, resource, control,
reduction and status evidence. It excludes private keys, readiness records,
endpoint logs and temporary endpoint state. Both public test certificates are
retained for independent SAN and DER verification.

The cleanup-status proposal now preserves its rejected, unexecuted first
contract and the corrected second revision separately. The approved patch is
`a08468825eb33a3e0a2069cc4842f807ed725befbb5dce896404dead8a218e94`.
The second revision ran once without endpoints or traffic and returned direct,
outer and tool status 0. Evidence
`6ded17b1bd93cd530236e618562e451f90a79fac3a2d95b02668eb4c12f0ae19`
records the TERM-only status-23 and TERM/KILL status-137 cases, joined process
groups, invalidating cleanup markers and no residual cleanup. It is a cleanup
control, not endpoint lifecycle or WAN evidence.

The capacity derivative retains both Go source checkpoints. Checkpoint 1
manifest `1be561325076c55ab1e77474a3f2acf0abce44470fe082b5df92a3da78a082d6`
is the pre-review source-only version. Corrected checkpoint 2 manifest
`a3c4f89addc191cc27c51994589c8bef9ec92ab898274b9d3cb151f91e5e30c3`
binds overlay
`0392a5055b5c1282627d959216bed7bd898e6f48da46a0c79c46d457e94284de`
and the separate adaptive-enabled correction. The normal and race focused
attempts accept with direct and saved outer status 0. Both tool envelopes report
1; exact raw tool-result sidecars `435ea04f...` and `be0614ab...` preserve those
discrepancies without retry. The four duplicate source pre/post inventories are
omitted; their equal hash remains bound in each attestation and focused
checkpoint
`95da60a769b1efd32abb8e799b8e94fedea1c197965fbe239ded10939f8771ff`.

The audited capacity build has direct and saved outer status 0 and tool-envelope
status 1. Manifest
`2bf50c5e7c7aaa4a1d16c0ab09ae516b92abf714404af7a0869234e80a64e075`
and raw tool result `8482a9a5...` bind the selected build records, exact command,
working directory, Go environment and output metadata. The output binary hash
is `57d77ddc5ee8ec6ab49cab0b7d0dd29641a70fa8598f0c7ba3eac73f46632ff5`;
the binary is excluded. Full integration source checkpoint 1 manifest
`e95b6912ad038fdeb9fdff762aa7ea95e62ef8d7ea56ffcbc4d5796b50986009`
retains all 29 indexed entries. It reports that no integration contracts,
endpoint or traffic ran at that checkpoint.

Integration source checkpoint 2 manifest
`1fde8bd1f7ff7979d9886d1722d0b9c1a4dee5c49a8a0ce58033b6087315fd60`
separately freezes the contract-driver correction. Its single no-traffic
attempt retains all 1,528 independently verified records. Every direct status
and the saved outer status is 0, while raw tool result
`490c03bf0eeca72a6ac1364bbfb5547bf04c9a5a1611869288432c310ee74c4c`
reports 1. The attempt manifest
`134b33e84063abcc2c8ecba1007e1f58d7be9cee60a931244a0e62954e6cabc9`
and checksum index
`4e6647850c17ca65ee4b748d99bb41d9e9156f9db2d4a1a66096686a47dde98a`
preserve that discrepancy without rerun. No endpoint or traffic ran. The old
and capacity full source inventories differ only in the three intended
`_test.go` files; the prior `47981bcd...` binary remains unchanged and separate.

Capacity tiny smoke 1 is a frozen rejected attempt. H1 completed eight exact
conditioning rows, then a healthy authenticated server-capacity snapshot with
selected source `host` was incorrectly rejected by a harness predicate that
required `cgroup_v2`. The independent leaf evidence still showed the intended
4 GiB/no-swap scope and zero pressure events. Rejection occurred before the
sequence-9 trace Start or transfer; no trace was created, H2 was withheld with
status 125, and the coordinator and outer status are 1. All five inner endpoint
groups required forced cleanup with TERM status 0, wait status 143 and absent
final groups. The outer supervisor and delegated scope joined naturally, but
the attempt remains invalid and the endpoint exits are not natural.

Checkpoint
`06bdacb95b40862d1ab6697182f14bcb42b4df9766d46650c97f17ea4968778d`
binds the complete 128-file run through hash mappings while this package retains only 63 safe run records plus the outer status. The selected
content includes rows, failure control, resource and scope evidence, lifecycle,
statuses and the public certificate. Private keys, credentials, commands,
readiness and transient endpoint files, logs, databases and rebuilt source are
hash-bound but their contents remain excluded.

The memory-source correction checkpoint is now frozen with patch
`3390be389f3a6f39ed50043062ab2193dda0d7368f6912ad9785a35236ac25bb`.
Its focused static, traced-transfer and arm-validator checks pass with direct,
outer and tool status 0 and ran no endpoint or traffic. The original
`SHA256SUMS.txt` remains unchanged with its stale self-entry; all other 1,593
entries verified. `MANIFEST-DISCREPANCY.txt` explains the redirect-into-self
error, and `SHA256SUMS-CORRECTED.txt` separately indexes all checkpoint files
except itself. No source or result was rewritten to correct the index.

Capacity smoke 2 is a preserved pre-exec status-126 failure. Its outer command
sidecar lacked execute permission, so the coordinator never ran and no output
directory, endpoint or traffic existed. All seven command, invocation, status,
stream, raw-tool and classification sidecars remain selected unchanged.

Capacity smoke 3 is a closed rejected attempt. H1 completed 13 rows with all
endpoint processes exiting naturally and resource checks returning 0. Three
trace captures and their parser steps completed, but the original analyzer
rejected every reduction on the known Go zero-PC-frame case. H1 and the pair
therefore remain failed, and H2 was withheld with status 125. The original
attestation's outer-group failure issue is misleading because it interpreted
the composite invalidation from analyzer status 1. The raw supervisor record is
authoritative: `group_joined:true`, `forced:false`; this does not repair the
analyzer rejection.

The three raw traces total 444,328 bytes and are explicitly selected with their
original rejected reductions, parser/analyzer statuses and reduction index.
Trace-safety report
`9360dd159d321c661cb3265989b5022920f3dc4b7d91b50fff0ee6d39bb7e349`
and manifest
`e9e163386de3339c4f3b85615b97cc8267a3aa014f27a9d240b4c90cfc01813c`
approve that bounded export and document disclosed scheduler/runtime metadata.
The 73,579,046 bytes of parsed text and 1,989,688 bytes of wire text remain
omitted review intermediates; their hashes remain bound. The process-local
trace bearer was never persisted, so exact bearer comparison is unavailable.
The review instead records zero user logs, the complete annotation/string
inventory, no bearer-shaped lowercase hex value and no exact closed-run secret
or readiness-value match. This is an export-safety result, not a valid reduction
or causal finding.

The zero-PC analyzer correction is frozen in two source checkpoints. Checkpoint
1 records the first reviewed four-fixture correction; checkpoint 2 separately
adds the mixed-known-ancestor case. Their manifest hashes are
`1e5f620f11fb7d4580b03c0df7f1c972fcb2f4b07fd3b19f085cf8e24e7c8178`
and
`b115606d685f5b84fb885baf8210f4ae6056ddb389a37924ec0f51543efc49ef`.
The first contract attempt is preserved as a pre-exec status-127 failure from a
missing `/usr/local/bin/python3`. The second attempt exercises 14 cases with
direct status 0 and accepted summary, while its raw outer tool result reports
1; that unexplained envelope discrepancy remains unchanged.

Three separate offline H1 reducers for smoke-3 sequences 9, 11 and 13 return
status 0 and conserve each handler-state partition. Unknown handler wait stacks
remain unattributed; the zero-PC events themselves are outside these handler
regions. The offline audit explicitly preserves the original H1
`accepted:false`, validator status 1, original analyzer statuses 1, and withheld
H2. These reducers are diagnostic follow-up only; they do not rewrite the
failed cohort or establish a cause.


The standalone H2 tiny arm then completed 13 exact H2 rows with verified TLS,
protocol, reuse, copy-observer, trace, resource and natural lifecycle evidence.
Its original arm remains `accepted:false`: parser statuses are 0, original
analyzer statuses are 1, 1 and 0 for sequences 9, 11 and 13, validation and
wrapper are 1, and runner is 0. A separate H2 offline reduction returns 0 for
all three traces while retaining `attribution_complete:false`; it does not
rewrite the original arm.

Independent H1 and H2 audits separately recompute exact handler-state
partitions from the frozen parsed inputs. The H1 audit preserves its initial
status-1 parser attempt and corrected status-0 attempt. Both final audits find
the zero-PC events outside the relevant handler path and make no cause claim.
The H2 export-safety report
`583e55978f580b10fb778683a4f131d18b77a5980087899ea63908b99ad9ad62`
approves only three raw traces totaling 662,549 bytes. Their parsed and wire
intermediates remain omitted. The original H2 failure and analyzer statuses
remain authoritative.

Final zero-PC source checkpoint
`4fbd3dc61799c5002774d2ea789c7392d64c4144194ec2ed230e22520defe8ce`
binds the complete 14-case source and both H1/H2 offline evidence without
authorizing traffic. It supplements rather than replaces source checkpoints 1
and 2.

The frozen sustained-size review rejects the original full-text parser path
before traffic: 256 MiB payloads conservatively project 2.218–2.563 GB of
expanded parsed text, above the unchanged 512 MiB physical-file bound, and a
prior retained-time projection is 844 MB. A separately hashed lossless
stack-interning source direction is authorized for source review with a new
4 GiB logical-expanded budget. It is absent from this package, has not run
traffic, and makes no runtime, cause, protection-equivalence or gate claim.

`ALLOWLIST.tsv` binds 5,342 completed files totaling 34,796,898 bytes by
SHA-256, source path, destination and category. The memory-source
checkpoint adds 1,597 files totaling 7,174,163 bytes; smoke 2 adds 7 files
totaling 3,300 bytes; smoke 3 adds 84 run/outer files totaling 839,884 bytes and
26 trace-safety files totaling 58,674 bytes. The prior capacity smoke-1 failure
remains 71 files totaling 208,438 bytes. `check-allowlist.py` verifies the
immutable set across all three source roots and refuses broad build-tree selection
or sensitive runtime artifact names.

## Package boundary

The allowlist includes the design, all three scope-probe sources and
attempts, failed runner-source checkpoint, superseded and final observer source
checkpoints, narrow final source files, source inventories, and focused
normal/race evidence. It also includes the frozen analyzer checkpoint and
controls, audited build provenance, final integration source checkpoint and all
focused integration contract evidence except the hash-identical rebuilt-runner
duplicate described above. It also includes both frozen failed tiny attempts,
their immutable source snapshots, the separate path-correction controls,
cleanup proposal history and control, both capacity Go checkpoints, focused
normal/race evidence, audited build provenance and capacity integration source
checkpoints 1 and 2 plus the complete frozen no-traffic attempt and the closed
capacity smoke-1 failure selection. It now also includes the frozen
memory-source correction/check checkpoint, smoke-2 pre-exec failure, closed
smoke-3 failure, its bounded raw-trace safety package, all three zero-PC source
checkpoints, the failed and successful zero-PC contracts, and two separate
three-sequence offline reduction sets. It excludes the full
source trees, test and analyzer caches, temporary directories, binaries, smoke-3
parsed and wire intermediates,
databases, private keys, readiness files, endpoint state and logs, and all
credentials.

The source contains fixed public test bearer literals used by contract tests.
Those are source fixtures. A generated run-scoped trace bearer is a credential:
its value, environment record, request header, control invocation and any file
that exposes it must never enter a future tracked package or export map.
Raw artifacts stay immutable; omission is the only permitted treatment for a
raw artifact that exposes the generated bearer. Sanitization must never rewrite
or replace the original raw file. Controller response, status and timing JSON
that contains no bearer or other secret is necessary evidence and remains
eligible for later explicit allowlisting.

The checker also performs a nonprinting exact-value comparison against secrets
and endpoint readiness values recovered from excluded files in closed runs. It
reports counts and matching artifact names only. Public route IDs are not
treated as authentication tokens. An exact match rejects the whole selected
artifact; raw evidence is never rewritten to remove a value.

Raw runtime traces require a separate content and size review. The 4,698-byte
generated parser-conformance control trace remains allowlisted with its parsed
output and manifest. The six H1/H2 smoke-3 raw traces are allowlisted only under
the reviewed scope described above; their expanded parsed and wire forms remain
excluded. Neither set establishes a performance cause.

`ancillary-source-contracts.tar.gz` contains the source, checkpoint and
no-traffic contract material. Actual diagnostic-run evidence remains loose and
readable. `PACKAGE-MAP.tsv` gives the placement, size, mode and hash of every
selected artifact; archive members should be located through that map rather
than assumed to exist as loose paths. `ANCILLARY-BUNDLE.sha256` binds the
compressed archive, and `verify-evidence-package.py` independently verifies
every archive and loose entry.

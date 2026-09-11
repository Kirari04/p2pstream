# Managed updates pre-staging review — 2026-09-11

Scope: the pending changes on top of staging commit
`6e529c0fc2c3615134e5fc47a2ba74c30826ca97`, following failures on
`v0.1.53-staging.84`–`.87`. This review does not publish a release or modify the
remote fleet.

## Review method

Three independent agents reviewed management recovery, host execution, and
UI/release compatibility. Follow-up passes cross-reviewed the fixes. The parent
added a disposable Ubuntu 24.04 Multipass test using the actual installer,
systemd services, separate Unix identities, signed management API, release
verification, and real agent tunnels. Docker additionally exercised host tests
as root with an unprivileged floor reader.

The VM test exposed an activation handoff race that the original unit tests did
not catch: a worker poll while the privileged helper finished could enqueue the
same activation again. The helper rejected that duplicate, and its failure
report replaced an already accepted successful activation. This became an
additional regression gate.

## Findings addressed

| Area | Failure | Correction and evidence |
| --- | --- | --- |
| Legacy recovery | The `.84` worker omitted four duplicated rollback fields, although its signed root receipt included the result. | Accept only the completely empty legacy rollback envelope with a verified, matching root receipt. Reject conflicting/partial fields. Tests exercise lost responses and replay counters. |
| Cancellation | Cancelling a blocked or incompletely proven rollback released the traffic fence. | Preserve the fence until a verified result and fresh matching tunnel prove recovery; pre-authorization cancellation still releases safely. |
| Retry deadlines | New rollback generations retained old completed root evidence and escaped the new action deadline. | Clear generation-local evidence while preserving identity replay floors; recognize legacy persisted pending recovery. |
| Management restart | Cached authorization and the reported management build could disagree after a generation or server upgrade. | Validate cached command context; use the signed server version for outstanding privileged commands and reverify staged metadata when the first command uses a newer server. |
| Unix permissions | Atomic root writes changed the floor to `root:root`, preventing the worker from staging another update. | Preserve updater-group read access while keeping the floor root-owned. Test with an actual unprivileged UID. |
| Repeated target | A healthy activation followed by rollback made the same target fail strict version/sequence floors. | Permit only an exact previously activated manifest hash at the equal floor. Keep signature, compatibility, expiry, epoch and minimum-safe checks. Migrate older floors only from authenticated matching evidence. |
| Duplicate commands | Repeated rollback commands restarted services and minted different receipts; repeated activation could turn success into failure. | Authenticate completed-command replay and republish its original result without re-execution. Reject substituted or superseded commands. Management preserves accepted proof against obsolete execution-failure reports while retaining real health-failure quarantine. |
| Shared staging files | Cleanup for an old completed command could remove the next campaign's staged files before its ready marker existed. | Coordinate with the existing worker lock and require a matching live command before deleting shared files. Retain unarmed candidates, including identical-target candidates for a new generation. |
| Recovery slot | Reactivating the running target could overwrite its distinct fallback with itself. | Preserve the authenticated previous slot. Exclude an already-running exact version and commit from new campaign previews. |
| First enrollment | A newer rescue updater raised the initial tunnel floor above the preserved old live agent. | Seed the tunnel floor from the actual preserved live build; track rescue compatibility separately. |
| Repair concurrency | Bootstrap changed shared state before stopping updater writers. | Stop triggers, then activator, then worker; refuse still-active writers. Serialize root bootstrap and worker enrollment with their respective locks. |
| Service recovery | Successful helper invocations exhausted systemd's five-start limit, leaving the following rollout stuck even after repair. | Keep successful commands from exhausting the crash budget and restore failed updater units during a completed repair. Exercise repeated real systemd actions in the VM. |
| UI state | Recovery history could disappear behind terminal history; polling and mixed retry/recovery controls obscured state. | Prioritize unresolved campaigns, poll with environment isolation, preserve draft inputs, separate recovery from stage retry, keep errors beside the action, and wrap narrow layouts. |
| Release compatibility | The configured minimum still admitted known-broken old rescue runners. | Manifest generation enforces a fixed minimum `v0.1.53-staging.88`, or a stricter configured minimum. Later tunnel releases retain this fixed baseline. |

## Verification record

| Check | Result | Local evidence |
| --- | --- | --- |
| `go test ./...` | Passed, including server and integration suites | `tmp/deep-review-go-test.log` |
| Focused race tests across updater, release verification, signed protocol and management | Passed; server portion 106.823 seconds | `tmp/deep-review-race.log` |
| `go vet ./...` and final harness vet | Passed | `tmp/deep-review-vet.log`, `tmp/deep-review-harness-vet.log` |
| Frontend tests | 257 tests / 904 assertions passed | `/tmp/p2pstream-ui-deep-review-tests.log` |
| Frontend typecheck and production build | Passed | `/tmp/p2pstream-review-final-build.log` |
| Installer lifecycle suite and shell syntax | Passed | `tmp/deep-review-lifecycle.log` |
| Real Multipass/systemd lifecycle | Passed in 91.45 seconds on Ubuntu 24.04 amd64 | `tmp/managed-updates-systemd/test.log`, `tmp/managed-updates-systemd/systemd-journal.log` |
| Code generation | Passed; generated files unchanged | `tmp/deep-review-generate.log` |
| Browser check of the existing remote-environment view | No horizontal overflow at 375, 900 and 1174 pixels; polling refreshed status and preserved a custom open planner draft | Read-only browser interaction; no campaign submitted |

The repeatable VM entry point is `scripts/test-managed-updates-systemd.sh`.
Its final run completed installation and enrollment, upgrade to `v1.1.0`,
activation of `v1.2.0`, signed cancellation rollback with the historical `.84`
worker, loss and retry of an already accepted rollback response, and recovery
of the previous live tunnel. It then proved six failed start attempts executed
only five helper processes, repaired the pinned updater without resetting the
failed unit manually, and successfully rolled out the same `v1.2.0` target.
Assertions verified process paths, signed evidence, fresh tunnels, floor read
permissions, pinned binary digest, enrolled updater version, and unchanged agent
credentials/network configuration. No production fleet action was submitted.
The disposable VM was removed after collecting the logs.

## Limits and deployment order

The VM covers Linux amd64 and one agent, with current-source binaries stamped
with distinct test build identities. The historical `.84` source supplies the
legacy worker while the privileged helper uses the fix. A VM-local TLS mirror
stands in for GitHub artifact hosting. This does not prove ARM64, real GitHub/OCI
publication, multi-host route quorum, active proxied traffic draining, arbitrary
existing machine configuration, or VM reboot/power-loss behavior. Unit tests
cover additional quorum, drain, timeout and interrupted-journal paths.

Upgrade management first so it can accept legacy recovery receipts and parse
the new updater compatibility bound. Complete pending recovery, then repair
older pinned updaters, preserving the agent token, live binary and network
policy. Start with a canary before wider rollout. A machine whose worker remains
stale must be diagnosed before it can provide recovery proof; a management-only
release cannot repair an unreachable host process.

The intended first corrected updater is `.88`; confirm that tag contains these
fixes when publishing. If another release consumes that tag first, move the fixed
minimum to the first release that actually contains the corrections. Re-run the
VM gate before staging changes to execution, recovery, enrollment or units.

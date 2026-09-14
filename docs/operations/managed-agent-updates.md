# Managed Agent Updates

Managed updates are a Linux/systemd workflow for rolling a stable
release or isolated staging prerelease across a fleet without rotating tunnel
tokens or logging in to each host.

The management server publishes desired state only. It cannot send shell
commands, local paths, service names, or download URLs. An unprivileged host
worker checks assignments and stages an exact raw binary; a small privileged
helper has no network access and can only verify, switch fixed binary slots,
restart the fixed agent unit, or restore the previous slot.

## Trust model

GitHub Releases in the configured `owner/repo` are the authenticated software
distribution boundary. Each release carries a canonical manifest with its
exact channel, SemVer, commit, monotonic release/security sequence,
compatibility ranges, expiry, artifact sizes and SHA-256 digests, OCI index,
and complete attachment inventory. The updater rejects arbitrary URLs, unknown
assets, mutable channel tags, cross-channel releases, digest mismatches,
incompatible builds, expired metadata, and rollback below persisted floors.

There is no release signing key, threshold signature, or separate root
metadata. The remaining Ed25519 identities protect privileged host actions:

- the existing agent bearer opens the tunnel but cannot request, report, or
  approve an update;
- the unprivileged updater identity authenticates check/stage/health reports
  but cannot activate a binary;
- the pinned management authority authorizes each exact activate/rollback
  command;
- the privileged activator identity attests the result of one consumed command,
  including its digest, nonce, monotonic sequence, and action counter.

## Configure the server catalog

The server catalog is enabled by default. Hosts still require one-time
enrollment and an explicitly created rollout campaign; enabling the catalog
does not update agents automatically. Set `AGENT_UPDATES_ENABLED=false` to
disable managed updates.

These are the defaults for a stable server. Set overrides as needed, then
restart p2pstream:

```dotenv
AGENT_UPDATES_ENABLED=true
AGENT_UPDATE_REPOSITORY=Kirari04/p2pstream
AGENT_UPDATE_CHANNEL=stable
```

Official builds select their compiled `stable` or `staging` channel
automatically; an explicit `AGENT_UPDATE_CHANNEL` must agree with that build.
If GitHub is unavailable, a cached last-known-good target remains eligible only
until its manifest expiry. Ordinary proxy traffic remains available when the
catalog is unavailable.

On the first pristine managed-update startup, the server creates a separate
Ed25519 command-authority key at
`${CONFIG_DIR}/agent-update-management-authority.json` (or
`AGENT_UPDATE_AUTHORITY_KEY_FILE`) and pins its public identity independently
in SQLite. This key is generated automatically by the management service; it
is not involved in publishing releases. Back up the database and key file as
one recovery unit. A missing, replaced, permissive, or mismatched key disables
managed enrollment and campaign progression instead of silently generating a
new identity for existing state.

## Remote environments

Selecting a trusted remote environment scopes **Agents → Updates** to that
server: its fleet, release catalog, command authority, enrollment tokens, and
campaigns. The parent forwards operator actions using the saved environment
management token over the existing certificate-pinned direct or agent
transport. Both servers require admin authorization; the remote server still
enforces release trust and rollout safety gates.

Host enrollment, update checks, and reports go directly to that environment's
management endpoint using their dedicated credentials. They are not forwarded
through the parent's environment proxy. Bootstrap commands use the remote
server's advertised management URL, or its saved environment URL when unset;
configure `MANAGEMENT_PUBLIC_URL` if hosts need a different reachable address.

The parent needs a build that supports forwarding update actions. The remote
server also needs the managed-update APIs and working catalog/authority
configuration; enabling updates on the parent does not configure the remote.

## Enroll hosts once

Open **Agents → Updates**. An unenrolled host has an **Enable managed updates** action. The UI
uses the same setup dialog as **Install Agent** and **Reinstall / Repair**, with
an editable Management URL, release details, and optional local files.
Enrollment preserves the host's existing management CA and agent configuration.
A remote server advertising localhost defaults to its saved environment
address. Enter the address reachable from the agent host.

The dialog provides one complete command with the short-lived enrollment token
included. Copy it and run it on the agent host; no separate token entry or file
downloads are needed. It fetches and verifies the selected release's installer
and architecture-specific binary before execution. Treat the copied command
as a credential. The handoff contains:

- the management HTTPS origin and agent public ID;
- a short-lived, single-use updater enrollment token;
- the pinned GitHub repository identifier;
- the management-authority public key, key ID, and epoch;
- no `AGENT_TOKEN`.

The installer preserves `agent.env`, the current tunnel binary, and the running
service. It installs the supplied release as a separately pinned rescue/updater
executable, snapshots the existing tunnel into its first content-addressed
slot, creates isolated worker/activator identities, enrolls them, and enables
the updater timer only after the server accepts the exact identity and
repository binding. Onboarding does not perform an out-of-campaign upgrade or
restart.

| Path | Owner | Purpose |
| --- | --- | --- |
| `/etc/p2pstream-updater` | `root:p2pstream-updater` | fixed origin/repository, enrollment receipt, and management authority |
| `/var/lib/p2pstream-updater/worker` | updater user | network-worker key and monotonic check/report state |
| `/var/lib/p2pstream-updater/root` | root | activator key, command/action counters, slot metadata, and recovery journals |
| `/opt/p2pstream-agent/slots` | root | immutable versioned raw binaries |
| `/opt/p2pstream-agent/updater/p2pstream` | root | pinned rescue/control executable |
| `/usr/local/bin/p2pstream` | root | symlink to the active fixed slot |

## Roll out a release

1. Open **Agents → Updates** and confirm the release shows **Verified**.
2. Choose **Plan rollout** and select enrolled connected agents.
3. Set canary size, wave size, maximum unavailable, route quorum, and healthy
   dwell.
4. Choose **Preview safety** and resolve any eligibility or compatibility
   failure.
5. Start the campaign.

Only the current canary or wave stages, avoiding a fleet-wide download burst.
Before activation the server cordons the agent, retires reusable idle streams,
waits for tracked streams to drain, and rechecks route quorum. Each discrete
wave must complete before the next begins. Success requires the privileged
activation receipt, a fresh tunnel carrying the exact target version and
commit, a worker health report, and the configured healthy dwell.

Pause stops new activation work while preserving durable state. Cancel removes
work that has not activated and requests local rollback for activated
assignments. Failed or blocked assignments can be retried with compare-and-swap
campaign generations so stale browser actions cannot overwrite newer operator
decisions.

## Failure and recovery

Cancelling a campaign does not immediately release hosts with an outstanding activation authorization. They remain cordoned and reserved until the privileged updater attests rollback and the agent establishes a fresh tunnel. A blocked host in a cancelled campaign can use **Recover agents** (called **Retry** in older interfaces) to request recovery. When rollback is already pending, wait for the updater instead of retrying the same assignment again. A paused campaign must be resumed or cancelled before its queued rollback can execute.

After verified recovery, the historical assignment remains **failed**, with desired action **none** and traffic eligible. That host can be selected in **Plan rollout** again. If the updater is stale or rollback times out, inspect the updater and activator service journals on the host; cancelling or refreshing the browser cannot replace that missing host evidence. Administrator-authorized recovery still requires root signature validation and a fresh tunnel carrying the exact restored build.

If the worker journal reports `root action report does not match its signed result receipt`, inspect the signed root receipt and the exact result fields in the report. Current management requires the manifest, artifact, restored version, and restored commit to match that receipt; an empty or partial legacy envelope is unsupported. Durable signed action and failure results are replayed in place after a lost response. Do not delete or edit host receipts, rotate the agent token, or reinstall the live agent to clear a reporting error. Complete the previous-release repair and re-enrollment steps in [Upgrades](./upgrades) before the planned v0.1.53 cutover.

If a host enrolled successfully but shows **Worker stale**, check the updater
service and timer journals. Complete stale-host repair with the previous release
before the planned v0.1.53 cutover; v0.1.53 does not reconstruct obsolete updater
state or service conditions. Keep the existing agent token, live binary, and
destination policy while performing that repair.

Re-enrolling or repairing an already managed host updates its rescue runner and keeps its existing agent binary and rollback state. Upgrading that binary requires a rollout campaign. A successful repair message alone does not confirm that the agent upgraded; check **Live tunnel** and the campaign result.

The updater timer can stop scheduling after repair if its next trigger was
already consumed.
The enrollment command sends its own check-in before systemd starts the worker,
so management can show the new pinned updater version while subsequent checks
never arrive. Check `systemctl status p2pstream-updater.timer`: the characteristic
state is **active (elapsed)** with **Trigger: n/a**. The original timer's boot
trigger was already consumed, and stopping/reloading the units can discard the
service activation timestamps needed by `OnUnitActiveSec`. The corrected timer
adds `OnActiveSec=30s`, giving every timer restart a fresh scheduling deadline.

For an already repaired host, apply the timer correction directly:

```sh
sudo install -d -m 0755 /etc/systemd/system/p2pstream-updater.timer.d &&
printf '[Timer]\nOnActiveSec=30s\n' | sudo tee /etc/systemd/system/p2pstream-updater.timer.d/10-rearm.conf >/dev/null &&
sudo systemctl daemon-reload &&
sudo systemctl restart p2pstream-updater.timer &&
sudo systemctl start p2pstream-updater.service
```

This preserves the agent credentials, network permissions, signed receipts and
current campaign. The timer should show a future trigger while waiting; **running**
is also normal while its worker executes. A failed worker invocation requires
its journal (`sudo journalctl -u p2pstream-updater.service -n 50 --no-pager`).

If activation reports that the agent service did not become active and rolled
back, inspect the agent service journal and candidate slot permissions. Current
updater state requires the protected executable and version directory to be
readable and executable with mode `0755`, with the existing no-follow, owner,
type, digest, and size checks. It does not repair legacy slot modes during
retry. Complete that repair with the previous release before the planned v0.1.53
cutover; changing only the management server or live agent binary does not
replace the pinned runner.

- A failed download, manifest, size, or digest check never reaches the
  privileged helper.
- A crash during activation is recovered from its fsynced journal and either
  finishes the exact transition or restores the prior slot.
- A failed restart/readiness check restores the previous slot and restarts the
  fixed service.
- Security and release floors advance only after activation succeeds.
- Campaign success requires server-observed evidence; worker status alone
  cannot advance a wave.
- Server watchdogs bound staging, draining, privileged action, reconnect, and
  evidence waits.
- Verified staged bytes and durable signed action/failure results are retried in
  place after a lost response; a worker rollback result is not used as a
  substitute for the durable root receipt.
- Successful updates retain only current, previous, and journal-referenced
  slots using no-follow owner/type/link checks.
- The rescue updater has its own compatibility version. Upgrading it requires a
  fresh enrollment token and explicit local bootstrap.

## Release workflow

Stable and staging use the same automatic build/verify/publish workflow. A push
to `staging` publishes a unique `vX.Y.Z-staging.N` prerelease; stable is
dispatched from `main` with an exact `vX.Y.Z`. Both build Linux `amd64` and
`arm64` binaries and images, verify every byte against the canonical manifest,
publish immutable version and commit aliases, create the GitHub release, read
its assets back, and finally move only the matching `staging` or `latest` image
alias. No signing ceremony, release key, signature secret, draft/publish split,
or workstation is required.

Enable GitHub immutable releases for defense in depth. Configure the manifest
validity, security/minimum-safe, server/updater/protocol compatibility, and
current-protocol repository variables listed in
`internal/agentupdate/README.md`. Docker deployments that require immutable
rollbacks should pin the version tag or OCI digest rather than a channel alias.

## Verification

The manifest generator requires at least `v0.1.53-staging.88` for the pinned
updater, or the configured `AGENT_UPDATE_UPDATER_MIN_VERSION` if higher, and
rejects incompatible maximums. This release floor does not replace the persisted
state checks required for the planned v0.1.53 cutover. Updater range bounds accept canonical SemVer
prereleases; server compatibility and minimum-safe-version bounds keep their
current stable-version rules. Before upgrading to v0.1.53, upgrade management to `v0.1.53-staging.90`, complete
pending recovery, and repair or re-enroll older pinned updaters with the
preparation release. v0.1.53 requires a manifest-pinned floor and does not
auto-restore an absent manifest pin.

Run the real service lifecycle test on a Linux amd64 workstation with Go and
Multipass installed:

```sh
scripts/test-managed-updates-systemd.sh
```

The script builds the current source, creates a disposable Ubuntu 24.04 VM, and
uses the production installer, service users,
systemd hardening, updater/activator executables, authenticated management
handlers, and agent tunnels. It verifies upgrades, signed cancellation
rollback with exact signed result fields, lost-response retry, pinned updater
repair after verified recovery, recovery from an exhausted systemd crash limit,
and a subsequent rollout to the same cancelled target.
Success checks include the real agent
process executable, reported build, released traffic fence, and unchanged
agent token/network configuration. It independently checks the repaired pinned
binary and its enrolled updater version. A VM-local TLS mirror supplies fixture
artifacts through the normal download and digest-verification path.

Logs are retained under `tmp/managed-updates-systemd` and a VM created by the
script is removed afterward. Set `P2PSTREAM_SYSTEMD_OUTPUT_DIR` for another log
directory. `P2PSTREAM_SYSTEMD_VM_NAME` may select an existing disposable VM named
`p2pstream-update-review...`; it must have no agent installation and is retained
for inspection. The harness refuses to provision the workstation.

This test covers one Linux amd64 agent. It does not simulate a multi-agent
route quorum, active routed request draining, ARM64, production GitHub/OCI
publication, or a pre-existing host's arbitrary configuration. Candidate
binaries use the same current source with distinct build identities; the old
worker uses its historical source while the privileged activator stays fixed.
Unit and lifecycle tests cover additional state transitions and failure
injection, and a production rollout still begins with a canary.

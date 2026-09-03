# Managed Agent Updates

Managed updates are an opt-in Linux/systemd workflow for rolling a stable
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

## Enable the server catalog

Set the server variables below, then restart p2pstream:

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

## Enroll hosts once

Open **Agents → Updates**. An unenrolled host has a **Bootstrap** action. The UI
presents a secret-free command and short-lived token as separate copy steps.
The command prompts for the token so it is not placed in shell history or
process arguments. The handoff contains:

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
- Verified staged bytes and durable action/failure results are retried in place
  after a lost response.
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

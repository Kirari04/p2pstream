# Managed Agent Updates

Managed updates are an opt-in Linux/systemd workflow for rolling a signed stable release or isolated staging prerelease across a fleet without rotating tunnel tokens or logging in to each host.

The management server publishes desired state only. It cannot send shell commands, local paths, service names, or download URLs. An unprivileged host worker checks assignments and stages an exact raw binary; a small root helper has no network access and can only verify, switch fixed binary slots, restart the fixed agent unit, or restore the previous slot.

## Trust Model

Three credentials have separate authority:

- the existing agent bearer opens the tunnel but cannot request, report, or approve an update;
- the unprivileged updater identity authenticates check/stage/health reports but cannot activate a binary;
- the pinned management authority signs enrollment and each exact activate/rollback command;
- the root activator identity signs the result of one consumed authority command, including its digest, nonce, monotonic command sequence, and monotonic root-action counter.

Stable and staging manifests are canonical JSON signed by the configured Ed25519 threshold root. They include the exact version, channel, commit, release/security sequence, compatibility ranges, expiry, per-platform raw-binary size and SHA-256, the exact OCI index and child descriptors, and a name/size/SHA-256 inventory of every published release attachment. Release assets are create-only. Stable accepts only final SemVer versions; staging accepts only prereleases, pins the exact running management prerelease, and persists an independent rollback floor.

The initial release root and the separate Ed25519 management-authority public key arrive as explicit bootstrap inputs and are pinned in `/etc/p2pstream-updater`. The installer verifies the authority key ID before enrollment, and ordinary bootstrap cannot replace either pin or lower any persisted floor. The release channel cannot replace them. Later release-root changes require a threshold signature from the currently pinned unexpired root and an exactly monotonic root version.

## Enable the Server Catalog

Set the server variables below, then restart p2pstream:

```dotenv
AGENT_UPDATES_ENABLED=true
AGENT_UPDATE_REPOSITORY=Kirari04/p2pstream
AGENT_UPDATE_CHANNEL=stable
AGENT_UPDATE_ROOT_FILE=/etc/p2pstream/update-root.json
```

The root file must be the same canonical metadata used by the shared release workflow. Official builds select their compiled `stable` or `staging` channel automatically; an explicit `AGENT_UPDATE_CHANNEL` must agree with the intended deployment. If the root is missing, malformed, expired, or incompatible, managed enrollment and campaign progression fail closed while ordinary proxy traffic remains available. A GitHub outage may use a cached last-known-good target only until that target's signed expiry.

On the first pristine managed-update startup, the server creates a separate
Ed25519 command-authority key at
`${CONFIG_DIR}/agent-update-management-authority.json` (or
`AGENT_UPDATE_AUTHORITY_KEY_FILE`) and pins its public identity independently
in SQLite. The key file is owner-only and never leaves the management host.
After that first pin, a missing, replaced, permissive, or mismatched file does
not generate a replacement: managed enrollment and campaign progression are
disabled while the reverse proxy continues to run. Back up the database and
this key as one recovery unit. Authority rotation is deliberately not automatic
in this version; key loss requires an explicit recovery and host re-enrollment
procedure rather than silently adopting a new signer.

## Enroll Hosts Once

Open **Agents → Updates**. An unenrolled host has a **Bootstrap** action. The UI presents a secret-free command and the short-lived token as separate copy steps. The command prompts for the token so it is not placed in shell history or process arguments. The handoff contains:

- the management HTTPS origin and agent public ID;
- a short-lived single-use updater enrollment token, copied separately;
- the pinned repository identifier and canonical root metadata;
- the management-authority public key, key ID, and epoch;
- no `AGENT_TOKEN`.

Run it on the existing agent host. The installer preserves `agent.env`, the current tunnel binary, and the running agent service. It installs the supplied release as a separately pinned rescue/updater executable, snapshots the existing tunnel into its first content-addressed slot, creates isolated worker/root identities, enrolls them, and enables the updater timer only after the server accepts the exact identity and root binding. Onboarding therefore does not perform an out-of-campaign tunnel upgrade or restart.

Updater host state is split across:

| Path | Owner | Purpose |
| --- | --- | --- |
| `/etc/p2pstream-updater` | `root:p2pstream-updater` | fixed origin/repository, signed enrollment receipt, pinned release root and management authority |
| `/var/lib/p2pstream-updater/worker` | updater user | network worker key and monotonic check/report state |
| `/var/lib/p2pstream-updater/root` | root | activator key, management-command and root-action counters, slot metadata, recovery journals |
| `/opt/p2pstream-agent/slots` | root | immutable versioned raw binaries |
| `/opt/p2pstream-agent/updater/p2pstream` | root | pinned rescue/control executable; ordinary tunnel campaigns do not replace it |
| `/usr/local/bin/p2pstream` | root | symlink to the active fixed slot |

## Roll Out a Release

1. Open **Agents → Updates** and confirm the release shows **Verified**.
2. Choose **Plan rollout** and select enrolled connected agents.
3. Set the canary size, wave size, maximum unavailable count, route quorum, and healthy dwell.
4. Choose **Preview safety**. Every selected agent must pass current enrollment, recent updater check-in, connectivity, assignment, platform, compatibility, and route-quorum checks.
5. Start the campaign.

Only the current canary or wave is allowed to stage, avoiding a fleet-wide download burst. Before activation the server cordons the agent, retires reusable idle streams, waits for every tracked stream to drain, and rechecks that every matching route target retains the requested number of other eligible connected agents. Every canary must complete before the first normal wave, and every member of one discrete wave must complete before the next wave is released. A wave advances only after the root attestation, a fresh tunnel carrying the exact target version and commit, a worker health report, and the healthy dwell. A normal agent self-report is not sufficient proof.

Pause stops new activation work but preserves durable state. Cancel removes work that has not activated and requests local rollback for already activated assignments. Failed or blocked assignments can be retried with compare-and-swap campaign generations so stale browser actions cannot overwrite newer operator decisions.

## Failure and Recovery

- A failed download or signature/digest check never reaches the root helper.
- A crash during activation is recovered from the fsynced root journal and either finishes the exact transition or restores the prior slot.
- A failed restart/readiness check restores the previous slot and restarts the fixed service.
- A host never advances its security, root, or release floors until activation succeeds.
- Campaign success requires server-observed evidence; forged worker status cannot advance a wave.
- A server-owned watchdog bounds staging, draining, root-action, reconnect, and evidence waits even when the updater or candidate stops calling management. Pre-activation drain expiry restores the old route; post-action expiry remains cordoned and requires explicit operator recovery.
- Verified staged bytes and signed durable action/failure results are retried in place after control-plane response loss; the worker does not redownload an unchanged artifact.
- Successful updates retain only the current, previous, and journal-referenced slots. Pruning uses no-follow, owner/type/link checks and never removes a recovery reference.
- The rescue updater has its own compatibility version. If a future target requires a newer rescue version, generate a fresh enrollment token and rerun the explicit local bootstrap; ordinary campaigns intentionally do not self-update the rescue control plane.
- Removing the normal agent token does not remove updater identity. Disable the agent or updater identity in management when decommissioning a host, then run the full-purge uninstaller.

## Release Workflow Requirements

Stable and staging publishing use the same mandatory prepare/sign/publish ceremony. The prepare
phase creates an unsigned draft for one exact version and commit. Separately
controlled offline signers inspect that candidate and each emit one signature
contribution; an offline workstation merges them only after the pinned-root
threshold is met. The protected publish phase receives only the resulting
public signature envelope, verifies the unchanged draft root, manifest,
release identity, every attachment, and the OCI index re-read by its signed
digest, then publishes without replacing an asset. Production signing private keys never enter GitHub Actions, repository
secrets, the management server, or one shared signing process.

Configure the `agent-update-publish` protected GitHub environment with required
reviewers and its per-release `AGENT_UPDATE_OFFLINE_SIGNATURES_BASE64` secret.
Staging pushes automatically prepare a unique `vX.Y.Z-staging.N` draft; an
operator still supplies the independently produced signature envelope and
dispatches the protected publish phase before the prerelease or `staging` image
alias becomes available.
Enable GitHub immutable releases for the repository so the registry also
enforces the workflow's create-only release contract after publication.
The repository variables for the canonical root, security/minimum-safe floors,
server/updater/protocol compatibility, current protocol, and manifest validity
must also be present. See `internal/agentupdate/README.md` for exact commands and
the two workflow dispatch phases. Docker consumers that require the same
content guarantee must deploy the signed OCI digest (or enforce an equivalent
OCI signature/admission policy); ordinary mutable tag pulls do not evaluate the
agent-update signature.

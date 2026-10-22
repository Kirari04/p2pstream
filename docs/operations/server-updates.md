# Management Server Updates

**System → Server updates** updates the server selected in the environment switcher. The installed version comes from that server. A local development UI can operate a remote environment using the same page and its saved, certificate-pinned management connection.

The first implementation supports operator-triggered updates for a single Linux Docker Compose server on amd64 or arm64, using the standard named `/data` volume. Unattended updates are off. Native/systemd, rootless Docker, Swarm, external databases, custom entrypoints, inherited container mounts, temporary mounts inside `/data`, volume subpaths, and file-backed Compose secrets/configs require a manual deployment.

## Install once on the selected Docker host

Use **System → Server updates** on the intended environment. The page gets its setup command from that management server's actual version, commit, channel, platform and persistent installation ID. Releases containing the bootstrap script provide a single-line `curl … | sudo sh` command. Older releases keep their original setup block; a newer browser UI cannot invent installation support for an older remote server. If the installed release lacks the Docker bundle/identity API, or its release metadata has expired, first manually deploy a current published release containing installation support. Published releases are never rewritten to add assets.

The ordinary Docker [quickstart](../getting-started/quickstart) downloads a small release deployment package, with editable `compose.yaml` and `.env`, and pins the image digest. A checkout is only needed for contributor/source builds.

Run the copied command in Bash on the selected environment's **local rootful Docker host**, from the existing Compose deployment's directory. Requires curl, sudo, Python 3.9+, Docker Engine and [Compose 2.20 or newer](https://docs.docker.com/reference/compose-file/include/). Custom projects, ordered Compose files, environment files and project-directory overrides can be entered in the setup card; use exactly the options which produced the running deployment. Remote Docker contexts, rootless Docker and unsupported layouts are refused.

The one-liner executes the selected release's bootstrap script from GitHub over HTTPS. That script checks the manifest against the digest supplied by the server, verifies the release descriptor and bundle before extraction and host installation, pulls a publisher-bound prebuilt Linux amd64/arm64 updater, and cleans up temporary downloads. GitHub and the publisher are trusted to supply the bootstrap code; the later checksum checks verify release content, not the script already executing. SHA-256 metadata is not an independent publisher signature. Server and updater references are pinned by content digest, with no local image build. The older setup block retains its embedded bundle-hash check.

Before activation, setup checks the selected installation ID, actual immutable running image ID and platform, release labels, real project/data volume and resolved Compose configuration hash. It repeats deployment checks after downloads, rejects pending Compose/.env changes and other data writers, then validates the supported layout. Identical version/commit on a different installation cannot satisfy the ID check. The ID lives in `/data/server-installation-id` before enrollment and survives container recreation and restore. A copied data volume intentionally retains that identity: do not run concurrent writable clones.

The updater receives **host-level Docker authority**. The management container receives only a read-only private control directory and random credential; no public updater port is added. State and recovery information live in the root-owned private `/etc/p2pstream-server-updater`, outside application data. Enrollment preserves ports, settings, volume and other services, uses the executor's pinned Compose version, starts and authenticates the private executor first, then recreates the server **once**. It reports enabled only after both server health/configuration identity and authenticated updater reachability are verified. Public traffic and agent tunnels reconnect during this restart; setup is not zero downtime.

Repeated setup confirms healthy enrollment without a restart or credential reset; it does **not** upgrade the host updater. If a legacy enrollment lost its private connection after ordinary Compose recreation, the verified setup helper can repair that exact saved deployment even when the installed controller predates the repair. It first proves the current image, settings, installation and data volume, then recreates the server with its saved binding; changed inputs or another data writer are refused. This repair preserves the pinned executor and credentials. Refresh the Updates page afterward to migrate the deployment integration. Interrupted setup resumes the same protected inputs and credentials. A host lock and durable phases prevent duplicate controllers and reject new updates while a host transaction is incomplete. Existing `config.json` alone never indicates success.

## Apply settings and operate the host

New enrollment uses one canonical deployment at `/etc/p2pstream-server-deployment/compose.json`. The original Compose entry points become small `include` files pointing there, so ordinary commands retain the updater, private connection and current software image. Setup preserves editable copies beside the originals with `.p2pstream-input-…` suffixes and records their ordered paths, project and environment-file options. Edit those saved inputs and the same `.env`, then apply supported settings through the installed controller:

```bash
sudo /etc/p2pstream-server-updater/manage apply
sudo /etc/p2pstream-server-updater/manage status
sudo /etc/p2pstream-server-updater/manage logs
sudo /etc/p2pstream-server-updater/compose ps
```

`apply` resolves the saved editable inputs on the host, validates the running deployment and candidate layout, and preserves the **current** image digest, installation ID, token, control mount, data volume and prebuilt updater. A software update performed since enrollment is retained; an older `P2PSTREAM_IMAGE` or image entry in the editable inputs cannot downgrade it. Choose software releases in the UI. Builds, hooks, replica/platform/restart changes, unsupported state/data paths, and edits to other services/networks/volumes are rejected before stopping services. Manage independently changing services in another Compose project.

The controller records the exact pre-apply model, pauses the idle executor under the shared host lock, recreates the server once, restores the private connection, and verifies it. Configuration rollback uses that operation's current image/settings; it is **not a data restore**. No arbitrary deployment hooks run. The canonical model can contain resolved application secrets: it is root-owned and readable only by root or a restricted operator group. If no suitable group exists, setup reports that ordinary Compose commands require sudo. The updater token stays in a separate file under root-private state, mounted read-only into the server. Status and errors do not print resolved settings; logs should remain operator-only.

After new enrollment or migration, `docker compose up -d --force-recreate` from the recorded deployment directory recreates the current managed deployment without dropping the updater or producing an updater orphan. The recorded explicit `-f` entry points work too. This recreates already-applied settings; use `manage apply` to validate and activate edits. `sudo /etc/p2pstream-server-updater/manage recreate` provides a host-checked server-only recreation. The installed `compose` wrapper remains read-only. A private receipt permits equivalent native Compose configurations even when Compose versions calculate different hashes; actual image, mounts, environment, ports or security-setting drift is refused.

### Does the updater upgrade itself?

**No.** The server and the host updater are separate components:

| Action | What changes |
| --- | --- |
| Update the server from this page | Server software changes; the host updater image and tools stay pinned. |
| Run ordinary setup again | A healthy installation is checked; its pinned updater is retained. |
| Run the one-time **Upgrade host updater** migration | The legacy updater is explicitly replaced with the matching published executor and its Compose integration is migrated. |

After that one-time migration, future host-updater upgrades remain a separate operator action. There is no automatic self-update or general host-updater refresh command. Follow the applicable release's host upgrade procedure when one is required.

The progress display works with existing executors. Backup I/O and cleanup improvements belong to the executor binary, so updating only the server or browser does not apply them to an already pinned executor.

### Migrate an existing enrollment

An existing enrollment keeps its original workflow until explicitly migrated. First update the server to a published release that supports coordinated deployment ownership, using its existing updater or the documented manual deployment procedure. Then open **System → Server updates → Upgrade host updater** and copy the migration command. Older UI versions call this card **Upgrade deployment integration**. Run it with the recorded Compose context on the Docker host. It selects the matching published executor, checks both binaries' capabilities and preserves the current image, installation identity, credentials, data and release floors.

Migration journals source-file changes before replacing them, keeps editable originals and resumes through `manage repair` after interruption. It refuses unrelated default Compose files or changed deployment inputs. If the legacy private binding needs repair first, the migration command performs that verified repair before proceeding; this can require two server restarts instead of one. A browser-only update cannot upgrade an older server or executor, and old published bundles do not acquire new repair behavior. Before migration, continue using `manage apply`; plain recreation on the older layout can still detach the private binding.

## Interrupted host setup and removal

If setup or application fails, the controller attempts to restore the exact pre-operation deployment. If restoration cannot be verified, it retains a durable `recovery_required` phase, private snapshots and an exact recovery command. A canceled or killed process similarly leaves durable phases. Inspect and resume from the host:

```bash
sudo /etc/p2pstream-server-updater/manage status
sudo /etc/p2pstream-server-updater/manage logs
sudo /etc/p2pstream-server-updater/manage repair
# For an unfinished host configuration/removal transaction only:
sudo /etc/p2pstream-server-updater/manage rollback
```

`repair` reuses saved credentials and pins, resumes initial activation/migration, or restores an interrupted configuration/removal transaction before you explicitly apply again. It first repairs an unavailable executor without restarting a healthy server. If legacy plain Compose recreation stripped the private binding, it can restore that binding only after proving the exact current image, installation, data volume and saved settings. Changed inputs must be restored before repair; repair does not adopt them or restore application data. `rollback` is refused for healthy deployments and cannot restore an obsolete enrollment-era release after normal software updates. An update or paused **data** recovery blocks configuration/removal and must be resolved separately below. An external image change or lost/ambiguous data deployment requires host diagnosis.

Host helpers have durable container identities. If the controller is interrupted, read-only inspection refuses to terminate a surviving helper. Explicit `repair` settles an abandoned setup/settings helper; interrupted data recovery resumes through `recover-update`, bound to the same saved update operation. When Docker access fails during helper cleanup, restore it before running that explicit recovery command. A timeout never permits competing rollback until helper termination is confirmed.

Failed helper output is retained in the root-private `/etc/p2pstream-server-updater/helper-failure.log`. It can include resolved settings or secrets; inspect it locally with sudo when the reported helper diagnostic is needed.

If layout validation rejects preparation before `config.json` exists, the original server has not been changed. Run `sudo /etc/p2pstream-server-updater/manage discard-preparation`, correct and apply the original Compose inputs, then copy a fresh setup block. This command first confirms helper termination and refuses any enrollment configuration, update state, maintenance marker or executor. It archives staging privately without changing containers or data. Once configuration exists, use `repair` or `rollback` to preserve credentials, or the narrowly scoped failed-enrollment cancellation below when applicable.

Archival retains recovery tools and inputs until its journal commit, and resumes interrupted cleanup under the same host lock. Retry `discard-preparation` before that commit, or the fresh setup block after it; retained history and the exported current model remain private and available.

Remove the updater explicitly when no update is active:

```bash
sudo /etc/p2pstream-server-updater/manage remove
```

Removal strips updater credentials, control mount, executor service and managed startup ownership from the **current** deployment, retaining the latest pinned image, settings, ports, data and other services. It verifies server health before removing the stopped executor. For migrated deployments it replaces the original entry point with that current standalone model and clears its companion overrides; saved editable inputs remain available. It also exports `/etc/p2pstream-server-updater/detached-compose.json` and prints the applicable command. Recovery tools and the prior model remain available after interruption. Use the exported current model when enrolling again; completed removal state is archived privately.

### Cancel a never-completed enrollment

An initial setup can restore the original server and report `Original deployment restored; enrollment is not enabled`. For example, `v0.1.53-staging.103` can fail `record-runtime` with `ambiguous duplicate Docker runtime entry` when the container and service names produce identical network aliases. Its saved `manage repair` reuses the same pinned executor, so repeating repair cannot apply a later alias fix.

Once a fixed release containing `cancel-enrollment` is published, use its verified bootstrap to cancel this failed enrollment before manually upgrading. This recovery path accepts only a never-completed `rolled_back` enrollment with no saved updater state (`state.json`), including preview history. It verifies the restored original deployment, installation identity, repository and channel, and refuses changed settings, unresolved helpers, runtime ownership or a running executor. It does not change the running server or its data. Failed state is archived privately, and its trust floors remain enforced for subsequent enrollment. Do not delete the state directory or lower its floors.

Download `setup-server-updater.sh` from the **exact fixed release's** GitHub assets. Use the selected server's installation ID and architecture, and the SHA-256 of that fixed release's manifest. The release must use the same repository and channel as the failed enrollment. The placeholders below are not a published version or executable values:

```bash
sudo sh ./setup-server-updater.sh 'Kirari04/p2pstream@<exact-fixed-release>' \
  '<installation-id>' '<fixed-release-manifest-sha256>' '<amd64-or-arm64>' \
  --cancel-enrollment --
```

Cancellation accepts no Compose options and cannot be combined with `--migrate-deployment`. It uses the saved deployment context, so the fixed bundle can recover an older restored server without requiring that server to run the bundle's version. A refusal leaves the saved recovery state available; diagnose the reported condition before continuing.

After cancellation succeeds, manually deploy the fixed published server image using the restored original Compose files, project and named data volume. Preserve the settings and data; follow [Upgrades](./upgrades) for applicable database and backup requirements. Then refresh the selected server's Updates page and run its fresh setup command to enroll the matching fixed executor. Cancellation alone does not install an updater, and server-only upgrades do not refresh a pinned host updater.

## Preview and update

1. Select the intended environment, such as `fireani.me`.
2. Open **System → Server updates**.
3. Preview the latest eligible release or enter an exact version in the installed stable/staging channel.
4. Review the environment, previous and target versions, pinned image, and interruption notice.
5. Start the update and leave the page open to follow its status.

The executor checks the running build against immutable Docker image labels and independently fetches the exact published GitHub release. It validates canonical metadata, asset hashes, image repository/platform, compatibility, expiration, release sequence, and recovery floor. The release publisher and GitHub HTTPS remain the source of trust; the current manifest format is not independently signed. A browser-supplied digest cannot substitute another image.

A preview expires after ten minutes and binds the instance, installed version/image, target release, and saved deployment hash. The deployment is checked again before replacement. Switching environments clears the visible preview. Request retries use the same operation ID, and accepted work belongs to the updater process rather than the browser connection.

## Downtime and recovery

The server also owns the public proxy and agent tunnel listener. Replacement interrupts public requests and existing streams. Plan for reconnections; this is not a zero-downtime upgrade.

Before stopping the server, the updater downloads both required image references, pauses management mutations and agent update polling, checks backup layout and available space, waits for in-flight management/certificate work, and rejects active agent rollouts or management TLS rotation. The live backup check is advisory when cache files disappear during the scan. It repeats strictly after stopping the server; data growth or disk use between checks can still cause a backup failure.

The updater then blocks new server starts durably, disables automatic container restart, stops the process and takes the data-volume writer lock. It checks SQLite integrity and archives application data, including SQLite/WAL, certificates, update authority and cache. The stable runtime lock and deployment owner marker are preserved separately and never restored from a snapshot. Every compatible server takes the same lock before configuration or database writes. A container waiting from an older deployment generation is refused after the lock is released. A supervisor crash leaves startup blocked until recovery publishes an allowed generation. Health probes are read-only.

The stopped backup remains necessary for a consistent rollback snapshot. Its persisted checksum and archive structure are verified together in one read. A larger data/cache volume still increases downtime; the updater does not omit cached data or copy a live database. Ordinary Compose recreation during an update can interrupt recovery and require host repair, but cannot grant a compatible server concurrent access to the offline data. Older binaries do not implement this guard; never substitute one into a managed deployment.

Session login, logout, and the UI's read-only bootstrap remain available while the server is running so operators can reload the Updates page during validation or a paused recovery.

The candidate is created stopped, has automatic restart disabled, and is then started. Acceptance requires the expected build, image, database schema and instance, running enabled listeners, and reconnection of all agents connected at preparation time. These checks must hold continuously for 120 seconds within a 300-second deadline. These are runtime readiness checks, not external synthetic requests to every route. An unrelated agent outage can therefore cause a rollback.

Update duration is longer than the interruption: image downloads happen while the previous server runs, and the candidate can serve requests during the stability check. The page shows the last reported stage, actual elapsed time, and the freshness of its management connection separately. Stage time is not a stability countdown or a prediction of completion. A lost management connection does not establish that the update failed, and a restored management connection does not prove every public route is healthy.

The result distinguishes **failed before replacement**, **previous version restored**, and **host recovery required**, with the reported reason and next steps. Keep the operation ID when investigating. If the UI cannot reconnect, inspect the host's status and logs rather than starting a second update.

If acceptance fails, the updater stops the candidate, verifies the backup checksum, restores the complete data snapshot, and starts the previous image. It never relies on down-migrations. Data written after the snapshot is lost on rollback. Once either version passes validation, normal restart policy and management access are restored.

The journal and observed release/security floors live outside application data and survive rollback. After successful candidate validation and restart-policy restoration, retention cleanup keeps the three most recently written snapshots, regardless of update outcome, plus the active operation's snapshot. Cleanup runs after the candidate is serving; a cleanup error appears as a maintenance warning on a successful update and does not roll it back. Failed updates retain their snapshots until a later successful update runs retention, so check private backup storage if failures accumulate. Backups contain secrets and remain in the private state directory; keep a separate off-host backup policy. The current operation and its initiating admin/token identity are recorded in `state.json`.

A restarted updater resumes an interrupted operation from its durable phase without requiring GitHub. If recovery itself fails, the operation becomes **Host recovery required** and stays paused across restarts. New updates remain blocked. Fix the reported host problem, such as insufficient disk space or Docker failure, then retry recovery once:

```bash
sudo /etc/p2pstream-server-updater/manage recover-update
```

Do not delete the maintenance file, journal, or snapshots to unblock a failed operation. Do not run another writer against `/data` while recovery is in progress. If the server cannot return, diagnose from the host logs and protected journal; its UI may be unavailable.

## Release maintenance and verification

The server and separate `p2pstream-updater` GHCR packages must be public for the documented anonymous installation. [New packages default to private](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry#pushing-container-images); after the first candidate push, set the updater package visibility to public and rerun the workflow. Release CI checks both digest references with an empty Docker credential directory and blocks publication if anonymous access fails.

The release workflow includes server compatibility metadata as a hashed attachment of the existing strict agent manifest. Existing agent readers keep their original manifest shape. Update `internal/serverupdate/metadata.go` deliberately when the SQLite schema or accepted agent/runtime protocols change; a test checks its schema against the migrated database.

Legacy support is scheduled to end with **v0.1.53** (not yet released).
Complete the [v0.1.53 upgrade preparation](./upgrades) before installing it.
`v0.1.53-staging.90` still includes the migration and repair support needed for
older installations. Keep a complete `CONFIG_DIR` backup and the matching
binary or image: rollback restores that backup, rather than opening the
upgraded database with an older release.

The test suite covers altered/stale previews, replay, changed release metadata, crash recovery, SQLite snapshot restoration, unsafe enrollment layouts, and local/remote authorization. Run the isolated real-view browser tests with:

```bash
cd web/management
bun run e2e:install
bun run e2e:updates
```

They build a fixture and serve its assets through browser interception, without starting development servers. Before a production rollout, rehearse a real replacement, host restart, and failed-candidate restore on a disposable deployment matching the host's Docker/Compose versions and agent topology.

The host lifecycle tests additionally cover setup identity, stale commands, unavailable downloads, custom context, interruption, idempotency, rollback/removal and pinned tooling. Run `python3 -I scripts/test-server-updater-host.py`.

Run the repeatable container rehearsal on a Linux Docker host with:

```bash
make docker-server-updater-test
```

It builds two release identities and runs a separate Docker daemon in a disposable container. Registry aliases, published fixture ports, and replacements stay inside that daemon. The scenarios cover a healthy upgrade with the full 120-second dwell, schema/authority restoration after a failed candidate, paused recovery and explicit retry, and an executor killed during validation. Each uses a real TLS-connected agent and forwarding route, a read-only server root filesystem, and the production Compose driver. It also exercises the shipped updater command, private-socket authentication, maintenance access, and exclusive executor lock. Diagnostics are saved under `tmp/server-updater-tests/`; CI runs this target and retains its evidence.

Only the unpublished GitHub release catalog and deterministic failure checkpoints are fixtures. The source-verification tests separately cover canonical metadata, architecture/image binding, replay floors, expiration, and tampering. The container rehearsal does not model a machine power cycle. For that check, use a disposable VM, interrupt after the durable `committing` phase while the candidate's restart policy is `no`, then boot the VM and confirm the updater starts and validates the candidate before reporting success. Never perform fault injection against a live deployment.

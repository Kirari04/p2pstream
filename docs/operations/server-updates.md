# Management Server Updates

**System → Server Updates** updates the server selected in the environment switcher. The installed version comes from that server. A local development UI can operate a remote environment using the same page and its saved, certificate-pinned management connection.

The first implementation supports operator-triggered updates for a single Linux Docker Compose server on amd64 or arm64, using the standard named `/data` volume. Unattended updates are off. Native/systemd, rootless Docker, Swarm, external databases, custom entrypoints, inherited container mounts, temporary mounts inside `/data`, volume subpaths, and file-backed Compose secrets/configs require a manual deployment.

## Install once on the selected Docker host

Use **System → Server Updates** on the intended environment. The page gets its complete setup block from that management server's actual version, commit, channel, platform and persistent installation ID. A newer browser UI cannot invent a setup command for an older remote server. If the installed release lacks the Docker bundle/identity API, or its release metadata has expired, first manually deploy a current published release containing installation support. Published releases are never rewritten to add assets.

The ordinary Docker [quickstart](../getting-started/quickstart) downloads a small release deployment package, with editable `compose.yaml` and `.env`, and pins the image digest. A checkout is only needed for contributor/source builds.

Run the copied block on the selected environment's **local rootful Docker host**, from the existing Compose deployment's directory. Requires Bash, curl, sudo, Python 3.9+, Docker Engine and its Compose plugin. Custom projects, ordered Compose files, environment files and project-directory overrides can be entered in the setup card; use exactly the options which produced the running deployment. Remote Docker contexts, rootless Docker and unsupported layouts are refused.

The block downloads over HTTPS, verifies its embedded bundle hash before extraction/execution, pulls a publisher-bound prebuilt Linux amd64/arm64 updater, and cleans up temporary downloads. GitHub and the publisher remain the trust source; SHA-256 metadata is not an independent publisher signature. The release descriptor and bundle are themselves hashed attachments of the existing canonical release manifest. Server and updater references are pinned by content digest, with no local image build.

Before activation, setup checks the selected installation ID, actual immutable running image ID and platform, release labels, real project/data volume and resolved Compose configuration hash. It repeats deployment checks after downloads, rejects pending Compose/.env changes and other data writers, then validates the supported layout. Identical version/commit on a different installation cannot satisfy the ID check. The ID lives in `/data/server-installation-id` before enrollment and survives container recreation and restore. A copied data volume intentionally retains that identity: do not run concurrent writable clones.

The updater receives **host-level Docker authority**. The management container receives only a read-only private control directory and random credential; no public updater port is added. State and recovery information live in the root-owned private `/etc/p2pstream-server-updater`, outside application data. Enrollment preserves ports, settings, volume and other services, uses the executor's pinned Compose version, starts and authenticates the private executor first, then recreates the server **once**. It reports enabled only after both server health/configuration identity and authenticated updater reachability are verified. Public traffic and agent tunnels reconnect during this restart; setup is not zero downtime.

Repeated setup confirms healthy enrollment without a restart or credential reset. Interrupted setup resumes the same protected inputs and credentials. A host lock and durable phases prevent duplicate controllers and reject new updates while a host transaction is incomplete. Existing `config.json` alone never indicates success.

## Apply settings and operate the host

Your original Compose files and `.env` remain the editable deployment inputs. Setup records their absolute source directory, ordered files, project and environment-file options. After changing supported settings, apply them through the installed controller:

```bash
sudo /etc/p2pstream-server-updater/manage apply
sudo /etc/p2pstream-server-updater/manage status
sudo /etc/p2pstream-server-updater/manage logs
sudo /etc/p2pstream-server-updater/compose ps
```

`apply` resolves the original inputs on the host, validates the running saved deployment and candidate layout, and preserves the **current** image digest, installation ID, token, control mount, data volume and prebuilt updater. A software update performed since enrollment is retained; an older `P2PSTREAM_IMAGE` or image entry in the editable inputs cannot downgrade it. Choose software releases in the UI. The same pinned Compose tools compute and apply configuration hashes. Builds, hooks, replica/platform/restart changes, unsupported state/data paths, and edits to other services/networks/volumes are rejected before stopping services. Manage independently changing services in another Compose project.

The controller records the exact pre-apply model, pauses the idle executor under the shared host lock, recreates the server once, restores the private connection, and verifies it. Configuration rollback uses that operation's current image/settings; it is **not a data restore**. No arbitrary deployment hooks run. Secrets in resolved models stay in private files and are not printed in status/errors. Logs may contain application diagnostics and should remain operator-only.

While enrolled, use `manage apply` instead of the original `docker compose up`. Running the old command bypasses the supported workflow and can remove the private connection; the updater refuses a configuration-hash mismatch. The `compose` wrapper is limited to read-only `ps`, `logs` and `config`. Inspect generated resolved configuration only in a private terminal: it contains secrets.

The updater image/tools remain pinned when server software changes. A future release needing a new executor API requires a deliberate host-side executor upgrade; the UI cannot replace the Docker-authorized component.

## Interrupted host setup and removal

If setup or application fails, the controller attempts to restore the exact pre-operation deployment. If restoration cannot be verified, it retains a durable `recovery_required` phase, private snapshots and an exact recovery command. A canceled or killed process similarly leaves durable phases. Inspect and resume from the host:

```bash
sudo /etc/p2pstream-server-updater/manage status
sudo /etc/p2pstream-server-updater/manage logs
sudo /etc/p2pstream-server-updater/manage repair
# For an unfinished host configuration/removal transaction only:
sudo /etc/p2pstream-server-updater/manage rollback
```

`repair` reuses saved credentials and pins, resumes initial activation, or restores an interrupted configuration/removal transaction before you explicitly apply again. Restore recorded input files if they changed after initial setup was staged. `rollback` is refused for healthy deployments and checks its baseline image/snapshot; it cannot restore an obsolete enrollment-era release after normal software updates. An update or paused **data** recovery blocks configuration/removal and must be resolved separately below. An external image change or lost/ambiguous data deployment requires manual host diagnosis; automatic configuration rollback never guesses a safe image or restores data.

Host helpers have durable container identities. If the controller is interrupted, read-only inspection refuses to terminate a surviving helper. Explicit `repair` settles an abandoned setup/settings helper; interrupted data recovery resumes through `recover-update`, bound to the same saved update operation. When Docker access fails during helper cleanup, restore it before running that explicit recovery command. A timeout never permits competing rollback until helper termination is confirmed.

Failed helper output is retained in the root-private `/etc/p2pstream-server-updater/helper-failure.log`. It can include resolved settings or secrets; inspect it locally with sudo when the reported helper diagnostic is needed.

If layout validation rejects preparation before `config.json` exists, the original server has not been changed. Run `sudo /etc/p2pstream-server-updater/manage discard-preparation`, correct and apply the original Compose inputs, then copy a fresh setup block. This command first confirms helper termination and refuses any enrollment configuration, update state, maintenance marker or executor. It archives staging privately without changing containers or data. Once configuration exists, use `repair` or `rollback` to preserve credentials.

Archival retains recovery tools and inputs until its journal commit, and resumes interrupted cleanup under the same host lock. Retry `discard-preparation` before that commit, or the fresh setup block after it; retained history and the exported current model remain private and available.

Remove the updater explicitly when no update is active:

```bash
sudo /etc/p2pstream-server-updater/manage remove
```

Removal strips only updater credentials/identity/control mount and executor service from the **current** saved deployment, retaining the latest pinned server image, settings, ports, data and other services. It verifies server health before removing the stopped executor. Recovery tools, journal and prior model stay available if removal is interrupted. Successful removal exports the current editable deployment to `/etc/p2pstream-server-updater/detached-compose.json`; the command prints the exact `sudo docker compose -p ... -f ... up -d --no-deps --no-build p2pstream` command to manage it. Use that exported model to retain the latest software/settings rather than reverting to obsolete source-image pins. To enroll again, select this server in the UI and copy a fresh setup command, using `-f /etc/p2pstream-server-updater/detached-compose.json` and the saved project name. Completed removal state is archived privately during fresh enrollment.

## Preview and update

1. Select the intended environment, such as `fireani.me`.
2. Open **System → Server Updates**.
3. Preview the latest eligible release or enter an exact version in the installed stable/staging channel.
4. Review the environment, previous and target versions, pinned image, and interruption notice.
5. Start the update and leave the page open to follow its status.

The executor checks the running build against immutable Docker image labels and independently fetches the exact published GitHub release. It validates canonical metadata, asset hashes, image repository/platform, compatibility, expiration, release sequence, and recovery floor. The release publisher and GitHub HTTPS remain the source of trust; the current manifest format is not independently signed. A browser-supplied digest cannot substitute another image.

A preview expires after ten minutes and binds the instance, installed version/image, target release, and saved deployment hash. The deployment is checked again before replacement. Switching environments clears the visible preview. Request retries use the same operation ID, and accepted work belongs to the updater process rather than the browser connection.

## Downtime and recovery

The server also owns the public proxy and agent tunnel listener. Replacement interrupts public requests and existing streams. Plan for reconnections; this is not a zero-downtime upgrade.

Before stopping the server, the updater downloads both required image references, pauses management mutations and agent update polling, waits for in-flight management/certificate work, and rejects active agent rollouts or management TLS rotation. It then disables automatic container restart, stops the process, checks SQLite integrity and available backup space, and archives the whole `/data` tree, including SQLite/WAL, certificates, update authority, and cache.

Session login, logout, and the UI's read-only bootstrap remain available while the server is running so operators can reload the Updates page during validation or a paused recovery.

The candidate is created stopped, has automatic restart disabled, and is then started. Acceptance requires the expected build, image, database schema and instance, running enabled listeners, and reconnection of all agents connected at preparation time. These checks must hold continuously for 120 seconds within a 300-second deadline. These are runtime readiness checks, not external synthetic requests to every route. An unrelated agent outage can therefore cause a rollback.

If acceptance fails, the updater stops the candidate, verifies the backup checksum, restores the complete data snapshot, and starts the previous image. It never relies on down-migrations. Data written after the snapshot is lost on rollback. Once either version passes validation, normal restart policy and management access are restored.

The journal and observed release/security floors live outside application data and survive rollback. Three recent completed snapshots and the active operation’s snapshot are retained. Backups contain secrets and remain in the private state directory; keep a separate off-host backup policy. The current operation and its initiating admin/token identity are recorded in `state.json`.

A restarted updater resumes an interrupted operation from its durable phase without requiring GitHub. If recovery itself fails, the operation becomes **Host recovery required** and stays paused across restarts. New updates remain blocked. Fix the reported host problem, such as insufficient disk space or Docker failure, then retry recovery once:

```bash
sudo /etc/p2pstream-server-updater/manage recover-update
```

Do not delete the maintenance file, journal, or snapshots to unblock a failed operation. Do not run another writer against `/data` while recovery is in progress. If the server cannot return, diagnose from the host logs and protected journal; its UI may be unavailable.

## Release maintenance and verification

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

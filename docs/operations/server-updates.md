# Management Server Updates

**System → Server Updates** updates the server selected in the environment switcher. The installed version comes from that server. A local development UI can operate a remote environment using the same page and its saved, certificate-pinned management connection.

The first implementation supports operator-triggered updates for a single Linux Docker Compose server on amd64 or arm64, using the standard named `/data` volume. Unattended updates are off. Native/systemd, rootless Docker, Swarm, external databases, custom entrypoints, inherited container mounts, temporary mounts inside `/data`, volume subpaths, and file-backed Compose secrets/configs require a manual deployment.

## Install once on the deployment host

First deploy a release containing the server-updater commands through your existing deployment procedure. Older servers cannot install this feature through their UI. Subsequent target releases must include `p2pstream_server_update.json` in their canonical release manifest.

Use `scripts/install-server-updater.sh`, `scripts/server-updater-compose.sh`, and `Dockerfile.updater` from that installed release's source archive or matching repository checkout. From the directory containing the **existing deployment's** Compose file and `.env`, run:

```bash
sudo /path/to/matching-release/scripts/install-server-updater.sh
```

If the Compose file and scripts share the repository root, the UI's `sudo ./scripts/install-server-updater.sh` command works directly. Pass the same Compose options used for your deployment, including the project name when overridden:

```bash
sudo /path/to/matching-release/scripts/install-server-updater.sh -p production -f compose.yaml -f production.yaml
```

Enrollment restarts the server once. The installer:

- Requires a running published release with its canonical multi-platform registry digest and updater support.
- Verifies the installed release against GitHub and records its release/security floor before enabling updates. Enrollment requires network access and unexpired metadata.
- Saves the fully resolved Compose deployment under `/etc/p2pstream-server-updater/compose.json`, retaining the original snapshot separately.
- Checks that the captured Compose configuration matches the running container before enrollment. Apply or revert pending Compose or `.env` edits through your existing deployment procedure first.
- Pins the running image and a locally built updater image by digest/image ID.
- Adds a separate updater container with the Docker socket, persistent recovery state, and the data volume.
- Gives the management container a read-only control-directory mount and a random private-socket credential. It adds no public updater port.
- Preserves a read-only management root filesystem, adding a bounded writable `/tmp` tmpfs for its private readiness socket when needed. Incompatible mounts covering that socket are rejected before activation.

The updater has host-level Docker authority. Keep its state directory private and outside `/data`. The installer rejects unsupported data layouts instead of assuming they can be restored safely.

Other containers may mount the data volume read-only, for example to read an agent CA certificate. An additional running writer blocks the update before downtime; the updater checks again before backup and restoration.

After enrollment, use the installed **Compose wrapper** for host operations. It runs the exact Compose version pinned into the updater and reads the saved deployment, preventing version-dependent configuration hash differences:

```bash
sudo /etc/p2pstream-server-updater/compose ps
sudo /etc/p2pstream-server-updater/compose logs --tail 100 p2pstream-server-updater
```

The original Compose file and `.env` no longer control the enrolled service. Running them again can replace it with different settings; the updater detects a Compose configuration-hash mismatch and refuses new updates. To change settings, first confirm no update is active, edit the private saved model, and apply it with the command above plus `up -d --no-deps p2pstream`. Keep resolved Compose dollar-sign escaping intact. Never edit its image, identity, control mount, or updater state during an operation.

The updater image remains pinned when the management image changes. Releases needing a newer executor API require a host-side upgrade of the executor; the UI cannot replace the component holding Docker authority.

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
sudo /etc/p2pstream-server-updater/compose stop p2pstream-server-updater
sudo /etc/p2pstream-server-updater/compose run --rm --no-deps p2pstream-server-updater /app/p2pstream server-updater recover
sudo /etc/p2pstream-server-updater/compose up -d --no-deps p2pstream-server-updater
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

Run the repeatable container rehearsal on a Linux Docker host with:

```bash
make docker-server-updater-test
```

It builds two release identities and runs a separate Docker daemon in a disposable container. Registry aliases, published fixture ports, and replacements stay inside that daemon. The scenarios cover a healthy upgrade with the full 120-second dwell, schema/authority restoration after a failed candidate, paused recovery and explicit retry, and an executor killed during validation. Each uses a real TLS-connected agent and forwarding route, a read-only server root filesystem, and the production Compose driver. It also exercises the shipped updater command, private-socket authentication, maintenance access, and exclusive executor lock. Diagnostics are saved under `tmp/server-updater-tests/`; CI runs this target and retains its evidence.

Only the unpublished GitHub release catalog and deterministic failure checkpoints are fixtures. The source-verification tests separately cover canonical metadata, architecture/image binding, replay floors, expiration, and tampering. The container rehearsal does not model a machine power cycle. For that check, use a disposable VM, interrupt after the durable `committing` phase while the candidate's restart policy is `no`, then boot the VM and confirm the updater starts and validates the candidate before reporting success. Never perform fault injection against a live deployment.

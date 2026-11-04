# Upgrades

Server upgrades are operator-owned. Use the deployment's Compose file and `.env` to select and run an exact image digest. Managed agent updates remain available under **Agents → Updates** and have their own [rollout procedure](./managed-agent-updates).

If an earlier release enrolled this server in **Management Server Updates**, first
[leave server updater enrollment](#leave-server-updater-enrollment). Complete
removal while the enrolled image is still running, before changing its image or
Compose configuration.

## Legacy support ends with v0.1.53

**Legacy support ends with v0.1.53-staging.91 and the stable v0.1.53 release.**
The staging cutover is scheduled for `v0.1.53-staging.91`; stable `v0.1.53` is
not published yet. These versions no longer repair old database layouts, policy
formats, or managed-agent-updater state automatically. New installations need no preparation.

| Version | What to do |
| --- | --- |
| `v0.1.52` and earlier stable releases | Prepare existing data and agents before upgrading to `v0.1.53`. |
| `v0.1.53-staging.90` | Available preparation release: it still includes the legacy migration and repair support. |
| `v0.1.53-staging.91` and later staging builds | Legacy support is removed. Complete the preparation below first. |
| `v0.1.53` and later stable releases | The same cutoff applies to stable releases. |

Staging builds through `v0.1.53-staging.90` do **not** contain this cutover.
Follow the preparation below before moving to `v0.1.53-staging.91` or stable
`v0.1.53`.

## Prepare an existing installation

1. **Back up before changing versions.** Save the entire `CONFIG_DIR`, the
   current binary or image tag, Compose file, and environment. Stop application
   writers and follow [Backup and restore](./backup-restore) so the SQLite backup
   is consistent.

2. **Complete old migrations and repairs before installing v0.1.53.**
   `v0.1.53-staging.90` still provides this support. If your installation needs
   it, run that release first and let startup finish. Complete any agent rollout,
   rollback, enrollment repair, or management TLS rotation there.

3. **Let historical dashboard data finish processing.** On the preparation
   release, this read-only query must return `1`:

   ```sql
   SELECT proxy_backfilled_through_id >= proxy_backfill_upper_id
      AND agent_backfilled_through_id >= agent_backfill_upper_id
   FROM observability_rollup_state WHERE id = 1;
   ```

   If it returns `0`, keep the preparation release running. Do not change these
   counters manually. The new release refuses to upgrade an unfinished backfill.

4. **Correct any remaining old configuration on the preparation release:**

   | Setting | Required preparation |
   | --- | --- |
   | Proxy target URL | Save only the origin, such as `https://upstream.example:8443`. Remove credentials, paths, queries, and fragments; a trailing `/` is accepted. Configure upstream authentication separately. |
   | Policy with `legacy_first_value` | Open the policy and resave its equivalent CEL expression. Keep explicit first-value matching if that is the intended behavior. |
   | Public certificate without saved validity dates | Re-upload the certificate so its validity dates are stored. |
   | Old managed agent updater | Complete repair or re-enrollment while legacy support is available. Keep security floors and durable receipts intact; see [Managed agent updates](./managed-agent-updates). |

   The new release checks old target URLs and policy flags before changing the
   database. If a check fails, correct the named record with the preparation
   release and retry.

5. **Use an explicit database location.** If `p2pstream.db` is still in the
   working directory, stop the server and move it with any SQLite `-wal`/`-shm`
   files into `CONFIG_DIR`, or set `DATABASE_URL` to its existing location. From
   v0.1.53, the server no longer copies that database automatically.

6. **Update custom management clients.** Local-auth provider updates must send
   `local_auth_security_settings_present=true` and the complete security
   settings. WAF rule updates must send `geo_restriction`, including disabled
   mode when appropriate. Stop sending `allow_cookie_requests` and
   `allow_cookie_requests_acknowledged`; the database upgrade removes the unused
   column automatically. Cookie-bearing requests continue to bypass the cache.

7. **Coordinate the server, agents, and pinned updater.** Use matching release
   builds for the cutover. Both fixed and adaptive tunnel modes now require
   explicit capacity and mode headers. Updating an agent's live binary does not
   update its separate pinned updater.

After preparation, take another complete backup and keep the matching
preparation-release binary or image. This is the restore point if the cutover
fails.

## Before changing versions

- Read the selected release's notes and check server, agent, and pinned agent-updater compatibility.
- Finish active agent rollouts, rollback recovery, and management TLS rotations before restarting the server.
- Schedule a maintenance window: the server restart interrupts public requests and agent tunnels.
- Save the full `CONFIG_DIR` data volume and your Compose files, `.env`, release receipt, and current image digest. Stop application writers for a consistent SQLite backup; follow [Backup and restore](./backup-restore).

Keep the backup with its matching image. Database migrations run at startup; replacing the image alone does not reverse those migrations. Do not run concurrent writable server containers against the same data volume.

## Leave server updater enrollment

Earlier staging releases could replace operator-owned Compose inputs with a
managed deployment. Its image and settings can be literal values in the managed
model, so editing `.env` does not necessarily change them. New releases omit the
server updater and its recovery commands and manifests: the old updater cannot
install them, and its removal health checks cannot finish after the image changes.

1. Keep the currently enrolled image and deployment in place. Finish any active
   update, rollback, or paused host recovery using that release's controller.
   Back up the full data volume and private deployment/recovery files. If
   enrollment or recovery is incomplete, resolve it with the installed release's
   recovery procedure before proceeding; do not delete its journal or owner marker.

2. **Before changing the server image**, run the installed removal command:

   ```bash
   sudo /etc/p2pstream-server-updater/manage remove
   ```

   This recreates the server without enrollment, preserving its current image,
   settings, ports, and data volume. Wait for successful removal and healthy
   service. If it fails, retain the current image and recovery state and follow
   the controller's repair instructions; do not proceed with the upgrade.

3. Save the editable deployment location and exact project/file command printed
   by removal. Depending on the enrolled layout, removal exports the current
   model to the original Compose entry points, leaves a detached canonical model
   at `/opt/p2pstream/deployment/compose.yaml`, or prints a command using
   `/etc/p2pstream-server-updater/detached-compose.json`. The single-file layout
   also retains `/opt/p2pstream/compose.inputs.yaml`; do not resume its old
   updater `apply` workflow. Use the **detached deployment**, with its retained
   project name and named data volume, for subsequent Compose commands. Do not
   switch back to a stale pre-enrollment file or assume `.env` controls a
   resolved model's literal image/settings.

4. Inspect the effective configuration with that deployment's `docker compose
   config`, and inspect its containers. Confirm no `p2pstream-server-updater`
   service/container, `SERVER_UPDATE_*` environment, or
   `/run/p2pstream-server-update` mount remains. Check that the server is healthy
   on the same image and data volume. Keep rendered configuration private: it
   can contain secrets. Take a new consistent backup of this standalone state.

5. Only now follow [Upgrade a Compose deployment](#upgrade-a-compose-deployment).
   Preserve the exported model's project name, ports, settings, and data mount.
   When its image is a literal value, update `services.p2pstream.image` directly;
   changing `P2PSTREAM_IMAGE` in `.env` works only if the Compose model references it.

6. After the standalone deployment and upgrade are verified, obsolete updater
   files and its unused `server-updater-data` volume may be archived or removed.
   First preserve recovery backups and relocate any **active** exported Compose
   file out of `/etc/p2pstream-server-updater`, updating and verifying your
   project/file command. Only then retire that directory and the obsolete
   `/usr/local/sbin/p2pstream-server-update` launcher. Identify the actual updater
   volume by its deployment configuration; never remove the server's data volume
   or run `docker compose down -v` for cleanup.

For rollback to an updater-era image, use the matching data and deployment backup
taken **after successful removal**. Those older images refuse standalone startup
if `/data/.p2pstream-runtime-owner.json` remains. A backup from an enrolled or
interrupted deployment needs its matching controller's recovery/removal procedure
before it can become standalone; do not bypass that guard by deleting the marker.
Never delete or replace `.p2pstream-runtime.lock`. The database rollback rules
[below](#restore-after-a-failed-upgrade) still apply.

## Upgrade a Compose deployment

1. Select an exact published stable tag (`vX.Y.Z`) or staging tag (`vX.Y.Z-staging.N`). Follow the verified download block in the [quickstart](../getting-started/quickstart), setting `P2PSTREAM_RELEASE` to that tag and `P2PSTREAM_DIRECTORY` to a **new, separate directory**. Preparation writes deployment inputs without starting Docker.

2. Inspect the prepared `.env` and release receipt. The `P2PSTREAM_IMAGE` value must refer to the intended repository and immutable digest:

   ```dotenv
   P2PSTREAM_IMAGE=ghcr.io/kirari04/p2pstream@sha256:REPLACE_WITH_VERIFIED_RELEASE_DIGEST
   ```

   Use the digest from the verified release preparation, never the literal placeholder. Moving `latest`, `staging`, and `nightly` tags do not preserve an exact image identity.

3. In your **existing deployment directory**, update only `P2PSTREAM_IMAGE` in `.env` to the prepared value, or `services.p2pstream.image` if your detached Compose model pins the image directly. Compare the new release's `compose.yaml` and default settings in `.env` with your deployment and deliberately apply any required changes. Preserve your settings, secrets, project name, ports, and persistent data-volume mount. Keep the new release receipt with the deployment once the upgrade is verified.

4. Validate and pull before stopping the current server, then recreate it:

   ```bash
   docker compose config --quiet
   docker compose pull p2pstream
   docker compose up -d p2pstream
   docker compose ps
   docker compose logs --tail=100 p2pstream
   ```

   Use the same project name, ordered `-f` files, and `--env-file` options as your normal deployment for every command. Do not run the newly prepared Compose project against the live data volume.

5. Verify the running release and traffic before closing the maintenance window:

   - The container uses the intended digest and its logs show successful startup.
   - The management UI and **Overview** load, or your management API responds if the UI is disabled.
   - **Proxy → Listeners** shows the expected listeners running.
   - Agents reconnect under **Agents → Fleet**, and managed updater check-ins succeed.
   - TLS, WAF, access policies, and important public hostnames work as expected.

Keep the previous image and complete backup until the new version has been validated. Never use `docker compose down -v` during an upgrade: it removes persistent volumes.

:::warning Compose files from v0.1.49 or earlier
These files may inject `PUBLIC_MAX_CONCURRENT_REQUESTS_PER_TARGET=256` even when
it is absent from `.env`. Change the Compose default to `0` or set
`PUBLIC_MAX_CONCURRENT_REQUESTS_PER_TARGET=0` explicitly to use automatic limits.
:::

## Upgrade a binary/systemd deployment

Download the exact release's Linux binary for the host architecture and verify its SHA-256 against that release's checksum manifest before installation. Save the current binary, configuration, and a consistent full data backup. Stop the service before replacing the executable:

```bash
sudo systemctl stop p2pstream
sudo install -m 0755 ./p2pstream /usr/local/bin/p2pstream
sudo systemctl start p2pstream
sudo journalctl -u p2pstream -n 100 --no-pager
```

Substitute your configured service name and binary path. Verify listeners, agent connections, and public traffic as for Compose.

## Roll out aggregate tunnel resource accounting

Upgrade the **server first**, validate existing agent traffic, then update a
small agent canary before the remaining fleet. Keep the previous server and
agent builds, configuration, and a consistent database backup. Protocol fallback
does not establish compatibility with every older release or override the
legacy-data preparation and updater-removal steps above.

Mixed versions have different resource requirements:

| Combination | What to expect |
| --- | --- |
| New agent, older server without `aggregate-v1` | Legacy fallback reserves about **35.54 MiB per tunnel lane** with the default 512 KiB receive window: about **142.17 MiB for four lanes**, before active-stream charges. These are accounting promises, not measured RSS. A small agent can refuse the session for insufficient headroom even when the older agent connected successfully. |
| Older agent, new server | Legacy streams need a separate receive-storage reservation, about **641 KiB per stream** at the default window, in addition to the server stream base. This extra check can fail immediately after ordinary stream admission and return a capacity 503; it does not wait in the fair admission queue. |
| New agent, new server | Negotiated `aggregate-v1` uses a separate control lane and the configured number of bulk lanes. With the default four bulk lanes, expect five physical tunnel sessions. |

Before continuing the rollout, check agent connection logs for the negotiated
mode, exercise each important agent-backed route and its health checks, and
check for capacity 503s, repeated reconnects, or resource-pressure rejections.
Confirm the canary negotiates `aggregate_flow_control=true` and retains its
control and bulk connections. A disconnected legacy lane zero replaces the
whole tunnel group; allow that reconnect to finish before judging readiness.
Keep adequate headroom during either mixed-version interval. Do not disable
resource accounting to force a connection.

Review any explicit `SERVER_TUNNEL_ESTIMATED_STREAM_BYTES` override. Its
historical full-stream value is now retained as a conservative **base**, with
aggregate session pools or legacy receive storage charged separately. An
unchanged explicit value can therefore reserve more total memory and admit
fewer streams. The default `0` selects the owned server base automatically;
see [Configuration](../reference/configuration) before changing an override.

Cache-bypass event counts can increase because bypass attribution is now
retained when forwarding, including requests with caching unavailable or
disabled. This reporting change alone does not mean that previously cacheable
responses stopped being cached.

For a coordinated binary rollback, restore the previous agent builds while the
new server is still available, verify their traffic and headroom, then use the
supported server rollback procedure. Rolling the server back first can put new
agents onto the larger legacy-session budget described above. Keep agent-updater
receipts and security floors intact.

Database migration **21** only replaces seven event-ID indexes with indexes
excluding NULL IDs; it does not rewrite event rows. Its down migration restores
the original full indexes. This index operation is distinct from a binary
rollback: older schema-20 binaries do not automatically undo migration 21, and
server rollback requires the matching pre-upgrade backup. It
does not make the earlier schema cutovers reversible or relax the backup
requirement below. Allow a maintenance window for index rebuilding on large
event databases.

## Restore after a failed upgrade

Stop the new server, restore the complete pre-upgrade data backup and its matching Compose inputs or binary, then start that saved version. Follow [Backup and restore](./backup-restore) and repeat the verification above. Writes made after the backup are lost on restoration. Do not assume an older binary can read a database migrated by a newer release.

Coordinate agent rollback with the restored server's compatibility requirements. Use the signed agent rollback/recovery procedure; preserve receipts and updater security floors.

## Related procedures

- [Backup and restore](./backup-restore)
- [Docker reference](../reference/docker)
- [Database reference](../reference/database)
- [Configuration reference](../reference/configuration)
- [Managed agent updates](./managed-agent-updates)

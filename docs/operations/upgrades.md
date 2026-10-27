# Upgrades

Server upgrades are operator-owned. Use the deployment's Compose file and `.env` to select and run an exact image digest. Managed agent updates remain available under **Agents → Updates** and have their own [rollout procedure](./managed-agent-updates).

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

## Upgrade a Compose deployment

1. Select an exact published stable tag (`vX.Y.Z`) or staging tag (`vX.Y.Z-staging.N`). Follow the verified download block in the [quickstart](../getting-started/quickstart), setting `P2PSTREAM_RELEASE` to that tag and `P2PSTREAM_DIRECTORY` to a **new, separate directory**. Preparation writes deployment inputs without starting Docker.

2. Inspect the prepared `.env` and release receipt. The `P2PSTREAM_IMAGE` value must refer to the intended repository and immutable digest:

   ```dotenv
   P2PSTREAM_IMAGE=ghcr.io/kirari04/p2pstream@sha256:REPLACE_WITH_VERIFIED_RELEASE_DIGEST
   ```

   Use the digest from the verified release preparation, never the literal placeholder. Moving `latest`, `staging`, and `nightly` tags do not preserve an exact image identity.

3. In your **existing deployment directory**, update only `P2PSTREAM_IMAGE` in `.env` to the prepared value. Compare the new release's `compose.yaml` and default settings in `.env` with your deployment and deliberately apply any required changes. Preserve your settings, secrets, project name, ports, and persistent data-volume mount. Keep the new release receipt with the deployment once the upgrade is verified.

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

## Roll out proxy admission changes

The stream-accounting changes retain the existing tunnel wire format and
per-stream receive reservations. Each server and agent enforces its own resource
budget, including when a fixed concurrency ceiling is configured.

Prefer a staged server-first rollout. Verify fleet readiness, ordinary requests,
TLS, cancellation and capacity errors before updating a small agent cohort.
Then expand only after checking both processes' memory and descriptor headroom.
Matching builds remain the preferred final state; unchanged wire format does
not establish a compatibility test matrix or bypass the cutover requirements
above.

Review explicit limits before rollout:

- `SERVER_TUNNEL_ESTIMATED_STREAM_BYTES` is a floor for the complete initial
  server-stream estimate. Zero selects the derived estimate; a positive value
  cannot reduce it. Larger values reserve more and can lower concurrency.
- `SERVER_TUNNEL_MAX_CONCURRENT_STREAMS` and `TUNNEL_MAX_CONCURRENT_REQUESTS`
  remain ceilings. Resource pressure can reject work below those ceilings.
- `TUNNEL_UPSTREAM_SOCKET_BUFFER_BYTES=0` keeps kernel autotuning. An old
  positive override still requests fixed buffers and can limit WAN throughput.

The default initial promises are about 897 KiB per server stream and 1,409 KiB
per agent stream, before additional receive-window growth and other owners.
Small agents can reach their own budget before the server. Check rejection
reasons rather than raising limits to reproduce an earlier concurrency count.
See [Agent proxy performance](./agent-proxy-performance) for accounting details.

Dashboard cache-bypass counts can rise after this upgrade without a caching-policy
change. Forwarded requests now retain bypass attribution in their recorded event
even when the cache decision stays on the request pipeline's stack. Previously,
those requests could be omitted from the bypass count.

If admission behavior requires rollback, retain the previous binary/image and
configuration, drain affected work and use the supported update procedure.
Binary rollback does not restore database compatibility across the v0.1.53
cutover; the backup requirement below still applies.

## Restore after a failed upgrade

Stop the new server, restore the complete pre-upgrade data backup and its matching Compose inputs or binary, then start that saved version. Follow [Backup and restore](./backup-restore) and repeat the verification above. Writes made after the backup are lost on restoration. Do not assume an older binary can read a database migrated by a newer release.

Coordinate agent rollback with the restored server's compatibility requirements. Use the signed agent rollback/recovery procedure; preserve receipts and updater security floors.

## Related procedures

- [Backup and restore](./backup-restore)
- [Docker reference](../reference/docker)
- [Database reference](../reference/database)
- [Configuration reference](../reference/configuration)
- [Managed agent updates](./managed-agent-updates)

# Upgrades

## Legacy support ends with v0.1.53

**Legacy support ends with v0.1.53-staging.91 and the stable v0.1.53 release.**
The staging cutover is scheduled for `v0.1.53-staging.91`; stable `v0.1.53` is
not published yet. These versions no longer repair old database layouts, policy
formats, or managed-updater state automatically. New installations need no preparation.

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
   | Old managed updater | Complete repair or re-enrollment while legacy support is available. Keep security floors and durable receipts intact; see [Managed agent updates](./managed-agent-updates). |

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

## Install v0.1.53 when it is released

For supported Docker Compose deployments, use
[Management Server Updates](./server-updates). For a manual Compose upgrade,
pin the `v0.1.53` image tag in your Compose configuration, then run:

```bash
docker compose pull
docker compose up -d
```

For a binary/systemd install, download the matching release binary and install it:

```bash
sudo install -m 0755 p2pstream /usr/local/bin/p2pstream
sudo systemctl restart p2pstream
```

:::warning Compose files from v0.1.49 or earlier
These files may inject `PUBLIC_MAX_CONCURRENT_REQUESTS_PER_TARGET=256` even when
it is absent from `.env`. Change the Compose default to `0` or set
`PUBLIC_MAX_CONCURRENT_REQUESTS_PER_TARGET=0` explicitly to use automatic limits.
:::

## Verify the upgrade

- The management UI and **Overview** load without unsupported-state errors.
- **Proxy → Listeners** shows the expected listeners running.
- Agents reconnect under **Agents → Fleet** and updater check-ins succeed.
- TLS, WAF, and local-auth settings are correct.
- A test request succeeds for each important hostname.

## Roll back to an earlier version

**Do not run a pre-v0.1.53 binary against a database upgraded by v0.1.53.** Stop
the new release, restore the complete backup taken after preparation, and start
the binary or image saved with that backup. Writes made after the backup are
not restored. Managed server updates use snapshot recovery if validation fails.

Coordinate agent rollback too: new fixed-mode agents require a response header
that older servers may omit. Use the supported signed agent rollback/recovery
procedure. Never delete receipts or lower updater security floors to force an
older binary.

## Related procedures

- [Management Server Updates](./server-updates)
- [Backup and restore](./backup-restore)
- [Database reference](../reference/database)
- [Configuration reference](../reference/configuration)
- [Managed agent updates](./managed-agent-updates)

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
first remove updater enrollment with `sudo /etc/p2pstream-server-updater/manage remove` if enabled. Use the current exported Compose model and exact project/file command printed by removal, pin the `v0.1.53` image tag in that configuration, then run the equivalent of:

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

## Roll out aggregate tunnel resource accounting

Upgrade the **server first**, validate existing agent traffic, then update a
small agent canary before the remaining fleet. Keep the previous server and
agent builds, configuration, and a consistent database backup. Protocol fallback
does not establish compatibility with every older release or override the
legacy-data preparation and managed-updater checks above.

Mixed versions have different resource requirements:

| Combination | What to expect |
| --- | --- |
| New agent, older server without `aggregate-v1` | Legacy fallback reserves about **173 MiB per tunnel lane** with the default 512 KiB receive window: about **693 MiB for four lanes**, before active-stream charges. These are accounting promises, not measured RSS. A small agent can refuse the session for insufficient headroom even when the older agent connected successfully. |
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
agents onto the larger legacy-session budget described above. Keep updater
receipts and security floors intact.

Database migration **21** only replaces seven event-ID indexes with indexes
excluding NULL IDs; it does not rewrite event rows. Its down migration restores
the original full indexes. This index operation is distinct from a binary
rollback: older schema-20 binaries do not automatically undo migration 21, and
managed server rollback still follows its snapshot/compatibility policy. It
does not make the earlier schema cutovers reversible or relax the backup
requirement below. Allow a maintenance window for index rebuilding on large
event databases.

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

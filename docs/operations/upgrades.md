# Upgrades

Upgrade the p2pstream image or binary while keeping the same persistent data directory.

## Use This When

Use this when moving to a new container tag, updating a binary/systemd install, or rolling back after an upgrade.

## Prerequisites

- A current backup of `CONFIG_DIR`, `/data` in Compose.
- The same `p2pstream-data` volume or binary install data directory will remain mounted.
- Optional: a pinned image tag for repeatable deployments.
- Avoid `staging` for production upgrades unless you are intentionally validating the next release candidate.
- Avoid `nightly` for production upgrades unless you are intentionally testing unreleased development changes.

## Steps

:::warning Upgrading a Compose file from v0.1.49 or earlier
Older Compose files inject `PUBLIC_MAX_CONCURRENT_REQUESTS_PER_TARGET=256`, even when the setting is absent from `.env`. Before recreating the container, change that Compose default to `0` or add `PUBLIC_MAX_CONCURRENT_REQUESTS_PER_TARGET=0` to `.env`. This lets a busy target use the server-wide request ceiling and avoids the legacy quarter-cap.
:::

1. For Docker Compose, pull and restart:

   ```bash
   docker compose pull
   docker compose up -d
   ```

2. Follow logs after the restart:

   ```bash
   docker compose logs -f p2pstream
   ```

3. For repeatable deployments, pin a tag instead of `latest`:

   ```yaml
   image: ghcr.io/kirari04/p2pstream:vX.Y.Z
   ```

4. Prefer an immutable staging prerelease for repeatable pre-release validation:

   ```yaml
   image: ghcr.io/kirari04/p2pstream:vX.Y.Z-staging.N
   ```

   Every staging branch build receives a unique SemVer prerelease, matching signed Linux agent assets, and an immutable image tag. The convenience `staging` alias moves only when one of those signed prereleases is published. Staging management UIs identify their channel clearly and generate matching pinned Linux agent installer commands.

5. Use the Docker-only `nightly` tag only for development validation:

   ```yaml
   image: ghcr.io/kirari04/p2pstream:nightly
   ```

   `nightly` is rebuilt from the `dev` branch and can change without a stable release.

6. For binary/systemd installs:

   ```bash
   sudo install -m 0755 p2pstream /usr/local/bin/p2pstream
   sudo systemctl restart p2pstream
   ```

7. Use the same server and agent tag when you want server and agent capabilities to move together.

   After the Yamux tunnel transport change, server and agent versions must match. Old WebSocket agents are incompatible with Yamux-tunnel servers, and Yamux agents are incompatible with old WebSocket-only servers.

8. For installations created before the route-target model, public backend configuration is migrated into route targets automatically. Old public backend CRUD/API surfaces are removed, and existing cache entries are discarded because cache keys are target-aware.

9. The route-target-only observability migration resets retained proxy request events and proxy rollups so legacy backend IDs are removed. Agent stats history is not reset.

## Verification

After upgrade:

- Management UI loads.
- **Overview** shows proxy running.
- Expected listeners are running under **Proxy → Listeners**.
- If agents are configured, enabled agents reconnect under **Agents → Fleet**.
- If ACME is used, certificate statuses are ready under **TLS**.
- A test request succeeds for each important hostname.

## Troubleshooting

| Symptom                                            | Check                                                                                |
| -------------------------------------------------- | ------------------------------------------------------------------------------------ |
| Container restarts repeatedly                      | Read `docker compose logs -f p2pstream`.                                             |
| Agent does not reconnect after transport upgrade     | Upgrade server and agents to matching versions; old WebSocket agents are incompatible. |
| Public listener missing                            | Confirm the same `/data` volume is mounted.                                          |
| Rollback needed                                    | Switch `compose.yaml` back to the previous image tag and run `docker compose up -d`. |

## Next Steps

- [Backup and restore](./backup-restore)
- [Docker reference](../reference/docker)
- [Troubleshooting](./troubleshooting)

# Upgrades

Upgrade the p2pstream image or binary while keeping the same persistent data directory.

## Use This When

Use this when moving to a new container tag, updating a binary/systemd install, or rolling back after an upgrade.

## Prerequisites

- A current backup of `CONFIG_DIR`, `/data` in Compose.
- A backed-up `SECRETS_ENCRYPTION_KEY` if stored secret encryption is enabled.
- The same `p2pstream-data` volume or binary install data directory will remain mounted.
- Optional: a pinned image tag for repeatable deployments.
- Avoid `staging` for production upgrades unless you are intentionally validating the next release candidate.
- Avoid `nightly` for production upgrades unless you are intentionally testing unreleased development changes.

## Steps

1. For Docker Compose, pull and restart:

   ```bash
   docker compose pull
   docker compose up -d
   ```

2. Follow logs after the restart:

   ```bash
   docker compose logs -f p2pstream
   ```

   When `SECRETS_ENCRYPTION_KEY` is configured, startup validates encrypted database secrets before listeners are registered. Existing plaintext rows are encrypted while `SECRETS_ENCRYPTION_REQUIRED=false`. With required mode enabled, plaintext rows fail startup. Rows encrypted with a key listed in `SECRETS_ENCRYPTION_PREVIOUS_KEYS` are rewrapped to the current key during the restart; keep previous keys configured until that startup succeeds.

3. For repeatable deployments, pin a tag instead of `latest`:

   ```yaml
   image: ghcr.io/kirari04/p2pstream:vX.Y.Z
   ```

4. Use the mutable `staging` tag only for pre-release validation:

   ```yaml
   image: ghcr.io/kirari04/p2pstream:staging
   ```

   `staging` is rebuilt from the `staging` branch and can change before the final release. Staging management UIs generate matching staging Linux agent installer commands.

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
- Expected listeners are running.
- Agents reconnect.
- ACME certificate statuses are ready.
- A test request succeeds for each important hostname.

## Troubleshooting

| Symptom                                            | Check                                                                                |
| -------------------------------------------------- | ------------------------------------------------------------------------------------ |
| Container restarts repeatedly                      | Read `docker compose logs -f p2pstream`.                                             |
| Agent does not reconnect after transport upgrade     | Upgrade server and agents to matching versions; old WebSocket agents are incompatible. |
| Startup fails while initializing secret storage     | Restore the matching `SECRETS_ENCRYPTION_KEY`, or include the old key in `SECRETS_ENCRYPTION_PREVIOUS_KEYS` during rotation. |
| Public listener missing                            | Confirm the same `/data` volume is mounted.                                          |
| Rollback needed                                    | Switch `compose.yaml` back to the previous image tag and run `docker compose up -d`. |

## Next Steps

- [Backup and restore](./backup-restore)
- [Docker reference](../reference/docker)
- [Troubleshooting](./troubleshooting)

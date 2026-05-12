# Upgrades

p2pstream stores runtime state in `/data`, mounted from the `p2pstream-data` Docker volume in the recommended Compose setup. Keep the same volume when upgrading.

## Docker Compose upgrade

```bash
docker compose pull
docker compose up -d
```

Follow logs after the restart:

```bash
docker compose logs -f p2pstream
```

## Pinned versions

For repeatable deployments, pin a tag instead of `latest` in `compose.yaml`:

```yaml
image: ghcr.io/kirari04/p2pstream:v0.1.0
```

Use the same tag for agents when you want server and agent binaries to move together.

## Advanced binary upgrade

Binary/systemd installs are an advanced deployment path. To upgrade one:

1. Download the new release archive.
2. Verify `checksums.txt`.
3. Replace `/usr/local/bin/p2pstream`.
4. Restart the systemd service.

```bash
sudo install -m 0755 p2pstream /usr/local/bin/p2pstream
sudo systemctl restart p2pstream
```

## Post-upgrade checks

- Management UI loads.
- Overview shows proxy running.
- Expected listeners are running.
- Agents reconnect.
- ACME certificate statuses are ready.
- A test request succeeds for each important hostname.

## Rollback

Keep the previous image tag or binary archive available. If rollback is needed, switch `compose.yaml` back to the previous image tag and run:

```bash
docker compose up -d
```

Use the same `p2pstream-data` volume. Back up the volume before major upgrades.

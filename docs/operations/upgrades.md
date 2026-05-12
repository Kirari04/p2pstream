# Upgrades

p2pstream stores runtime state in `/data`, so image and binary upgrades should keep the same data directory.

## Docker upgrade

```bash
docker compose pull
docker compose up -d
```

For a single `docker run` container:

```bash
docker pull ghcr.io/kirari04/p2pstream:latest
docker stop p2pstream
docker rm p2pstream
docker run -d \
  --name p2pstream \
  --restart unless-stopped \
  -p 80:80 \
  -p 443:443 \
  -p 8081:8081 \
  -v p2pstream-data:/data \
  ghcr.io/kirari04/p2pstream:latest
```

## Pinned versions

For repeatable deployments, pin a tag instead of `latest`:

```text
ghcr.io/kirari04/p2pstream:v0.1.0
```

Use the same tag for agents when you want server and agent binaries to move together.

## Binary upgrade

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

Keep the previous image tag or binary archive available. If rollback is needed, stop the service, restore the old binary/image, and start with the same `/data`.

Back up `/data` before major upgrades.

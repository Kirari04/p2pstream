# Backup and Restore

Back up the full `CONFIG_DIR`, usually `/data` in Docker deployments.

## What to back up

Include:

```text
/data/p2pstream.db
/data/p2pstream.db-wal
/data/p2pstream.db-shm
/data/certs/
```

The database stores proxy config, users, sessions, agent registry, and observability. The cert directory stores management TLS and public TLS material.

## Simple Docker backup

The safest simple backup is to stop the container, copy the volume, then start it again.

```bash
docker stop p2pstream

docker run --rm \
  -v p2pstream-data:/data:ro \
  -v "$PWD:/backup" \
  debian:bookworm-slim \
  tar -C /data -czf /backup/p2pstream-data.tar.gz .

docker start p2pstream
```

Stopping the container avoids copying SQLite while WAL files are changing.

## Restore

Create a fresh volume and extract the backup into it:

```bash
docker volume create p2pstream-data-restored

docker run --rm \
  -v p2pstream-data-restored:/data \
  -v "$PWD:/backup" \
  debian:bookworm-slim \
  tar -C /data -xzf /backup/p2pstream-data.tar.gz
```

Start p2pstream with that volume:

```bash
docker run -d \
  --name p2pstream \
  --restart unless-stopped \
  -p 80:80 \
  -p 443:443 \
  -p 8081:8081 \
  -v p2pstream-data-restored:/data \
  ghcr.io/kirari04/p2pstream:latest
```

## Restore checks

After restore:

1. Open management.
2. Confirm public listeners are running.
3. Confirm TLS certificate mappings are ready.
4. Confirm agents reconnect.
5. Send a test request through each important public hostname.

## Management CA warning

If you restore without `/data/certs/management`, agents that trust the previous auto-generated CA will fail TLS verification. Restore the management CA or update each agent with the new CA.

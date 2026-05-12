# Backup and Restore

Back up the full `CONFIG_DIR`, mounted at `/data` from the `p2pstream-data` Docker volume in the recommended Compose setup.

## What to back up

Include:

```text
/data/p2pstream.db
/data/p2pstream.db-wal
/data/p2pstream.db-shm
/data/certs/
```

The database stores proxy config, users, sessions, agent registry, and observability. The cert directory stores management TLS and public TLS material.

## Simple Compose backup

The safest simple backup is to stop the service, copy the volume, then start it again.

```bash
docker compose stop p2pstream

docker run --rm \
  -v p2pstream-data:/data:ro \
  -v "$PWD:/backup" \
  debian:bookworm-slim \
  tar -C /data -czf /backup/p2pstream-data.tar.gz .

docker compose start p2pstream
```

Stopping the container avoids copying SQLite while WAL files are changing.

## Restore

Stop Compose and recreate the named data volume:

```bash
docker compose down
docker volume rm p2pstream-data
docker volume create p2pstream-data
```

Extract the backup into the restored volume:

```bash
docker run --rm \
  -v p2pstream-data:/data \
  -v "$PWD:/backup" \
  debian:bookworm-slim \
  tar -C /data -xzf /backup/p2pstream-data.tar.gz
```

Start p2pstream with the restored volume:

```bash
docker compose up -d
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

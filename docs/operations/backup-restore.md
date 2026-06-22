# Backup and Restore

Back up and restore the full `CONFIG_DIR`, mounted at `/data` from the `p2pstream-data` Docker volume in the recommended Compose setup.

## Use This When

Use this before upgrades, host moves, disaster recovery tests, or any change that could replace the persistent data volume.

## Prerequisites

- Shell access to the Docker host or binary install host.
- Enough storage for the SQLite database, WAL files, certificates, ACME state, and cache metadata.
- A maintenance window if you want the simplest consistent SQLite backup.
- Access to the configured `SECRETS_ENCRYPTION_KEY` or `SECRETS_ENCRYPTION_KEY_FILE` when stored secrets encryption is enabled.

## Steps

1. Include at least:

   ```text
   /data/p2pstream.db
   /data/p2pstream.db-wal
   /data/p2pstream.db-shm
   /data/certs/
   ```

   The database stores proxy config, users, sessions, agent registry, TLS metadata, and observability. The cert directory stores management TLS and public TLS material.

   If stored secrets encryption is enabled, back up the key material separately in your deployment secret manager. Do not rely on the `/data` backup to contain it; losing the key makes encrypted upstream credentials, DNS provider tokens, WAF secrets, and remote-environment tokens unrecoverable.

2. For the safest simple Compose backup, stop the service, copy the volume, then start it again:

   ```bash
   docker compose stop p2pstream

   docker run --rm \
     -v p2pstream-data:/data:ro \
     -v "$PWD:/backup" \
     debian:bookworm-slim \
     tar -C /data -czf /backup/p2pstream-data.tar.gz .

   docker compose start p2pstream
   ```

3. To restore, stop Compose and recreate the named data volume:

   ```bash
   docker compose down
   docker volume rm p2pstream-data
   docker volume create p2pstream-data
   ```

4. Extract the backup into the restored volume:

   ```bash
   docker run --rm \
     -v p2pstream-data:/data \
     -v "$PWD:/backup" \
     debian:bookworm-slim \
     tar -C /data -xzf /backup/p2pstream-data.tar.gz
   ```

5. Start p2pstream with the restored volume:

   ```bash
   # Ensure SECRETS_ENCRYPTION_KEY or SECRETS_ENCRYPTION_KEY_FILE matches the restored database when encryption is enabled.
   docker compose up -d
   ```

## Verification

After restore:

1. Open management.
2. Confirm public listeners are running.
3. Confirm TLS certificate mappings are ready.
4. Confirm agents reconnect.
5. Send a test request through each important public hostname.
6. If stored secrets encryption is enabled, run `p2pstream secrets status` against the restored configuration and confirm there are no missing-key or decrypt-failed rows.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| Agents fail TLS after restore | Restore `/data/certs/management` or update each agent with the new CA. |
| Server fails to initialize secret storage | Restore the matching current key via `SECRETS_ENCRYPTION_KEY` or `SECRETS_ENCRYPTION_KEY_FILE`; during key rotation, provide the old key in `SECRETS_ENCRYPTION_PREVIOUS_KEYS`. |
| Public TLS mappings are missing | Confirm `/data/certs/` and SQLite were restored together. |
| Login state changed | Sessions are stored in SQLite and depend on the restored database. |
| Cache files missing | Cache can refill; SQLite and certs are more critical than cached bodies. |

## Next Steps

- [Upgrades](./upgrades)
- [Database reference](../reference/database)
- [Security hardening](./security-hardening)

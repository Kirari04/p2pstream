# Docker Compose Details

Use Compose for the self-hosted p2pstream server. Compose restarts the container after host reboots and keeps runtime state in the named `p2pstream-data` volume.

## Compose file

The repository includes this root `compose.yaml`:

```yaml
services:
  p2pstream:
    image: ghcr.io/kirari04/p2pstream:latest
    container_name: p2pstream
    restart: unless-stopped
    environment:
      CONFIG_DIR: /data
      MANAGEMENT_PORT: "8081"
      PORT: "80"
      MANAGEMENT_PUBLIC_URL: "${MANAGEMENT_PUBLIC_URL:-https://localhost:8081}"
      MANAGEMENT_TLS_EXTRA_HOSTS: "${MANAGEMENT_TLS_EXTRA_HOSTS:-}"
    ports:
      - "${P2PSTREAM_HTTP_PORT:-80}:80"
      - "${P2PSTREAM_HTTPS_PORT:-443}:443"
      - "${P2PSTREAM_MANAGEMENT_PORT:-8081}:8081"
    volumes:
      - p2pstream-data:/data

volumes:
  p2pstream-data:
    name: p2pstream-data
```

Create `.env` from the example and set the public management URL:

```bash
cp .env.example .env
```

```dotenv
MANAGEMENT_PUBLIC_URL=https://proxy.example.com:8081
```

Start it:

```bash
docker compose up -d
```

Open:

```text
https://proxy.example.com:8081
```

Follow logs:

```bash
docker compose logs -f p2pstream
```

## Management URL and TLS

Set `MANAGEMENT_PUBLIC_URL` to the URL that browsers and agents should use for the management UI/API. This is especially important when:

- the container port is published to a different host port,
- the server is behind NAT,
- management is behind another reverse proxy,
- the auto-generated management certificate needs the public hostname or IP address.

Management HTTPS is enabled by default. When no certificate is provided, p2pstream creates a persisted local management CA and server certificate under `/data/certs/management`.

For extra certificate names in auto TLS mode, set `MANAGEMENT_TLS_EXTRA_HOSTS` in `.env`:

```dotenv
MANAGEMENT_TLS_EXTRA_HOSTS=proxy.example.com,192.0.2.10
```

Agents verify the management certificate with `MANAGEMENT_CA_FILE` or `MANAGEMENT_CA_PEM_BASE64`; they do not skip TLS verification by default.

## Port overrides

The default Compose file publishes:

| Host port | Container port | Use |
| --- | --- | --- |
| `80` | `80` | public HTTP listener and ACME HTTP-01 |
| `443` | `443` | public HTTPS listener and ACME TLS-ALPN-01 |
| `8081` | `8081` | management UI/API and agent HTTPS/WSS |

Override host ports in `.env` when needed:

```dotenv
P2PSTREAM_HTTP_PORT=8080
P2PSTREAM_HTTPS_PORT=8443
P2PSTREAM_MANAGEMENT_PORT=9443
MANAGEMENT_PUBLIC_URL=https://proxy.example.com:9443
```

Docker only exposes ports listed under `ports`. If you create an extra listener in the management UI, publish that port in Compose too.

Example for an additional listener on container port `8088`:

```yaml
ports:
  - "${P2PSTREAM_HTTP_PORT:-80}:80"
  - "${P2PSTREAM_HTTPS_PORT:-443}:443"
  - "${P2PSTREAM_MANAGEMENT_PORT:-8081}:8081"
  - "8088:8088"
```

If the server is behind NAT, forward public ports from the router or cloud firewall to the Docker host. Agents must reach the management URL, not the public listener URL.

## Persistent data

Keep the `p2pstream-data` volume mounted at `/data`. It contains:

- the SQLite database,
- management TLS certificates,
- public TLS certificates,
- generated ACME material.

Do not delete this volume during upgrades.

## Lifecycle

Stop and start the service:

```bash
docker compose stop p2pstream
docker compose start p2pstream
```

Restart after editing `.env` or `compose.yaml`:

```bash
docker compose up -d
```

Upgrade the image:

```bash
docker compose pull
docker compose up -d
```

For repeatable deployments, pin a release tag in `compose.yaml`:

```yaml
image: ghcr.io/kirari04/p2pstream:v0.1.0
```

## Next

- [Backup and restore](../operations/backup-restore)
- [Docker reference](../reference/docker)
- [Management TLS reference](../reference/management-tls)

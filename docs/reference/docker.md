# Docker Reference

Docker Compose is the recommended p2pstream server deployment path.

## Exact Fields And Defaults

Released images are published at:

```text
ghcr.io/kirari04/p2pstream
```

Common tags:

```text
latest
vX.Y.Z
```

The runtime image:

| Runtime detail | Value |
| --- | --- |
| Binary | `/app/p2pstream` |
| Management UI dist | `/app/web/management/dist` |
| `ENV` | `production` |
| `MANAGEMENT_UI_DIST_DIR` | `/app/web/management/dist` |
| `MANAGEMENT_PORT` | `8081` |
| `CONFIG_DIR` | `/data` |
| Volume | `/data` |
| Exposed ports | `80`, `443`, `8081` |
| Command | `/app/p2pstream server` |

The root Compose file maps:

```yaml
ports:
  - "${P2PSTREAM_HTTP_PORT:-80}:80"
  - "${P2PSTREAM_HTTPS_PORT:-443}:443"
  - "${P2PSTREAM_MANAGEMENT_PORT:-8081}:8081"
```

## Validation Rules

- Docker only publishes what Compose maps; creating a listener in the UI does not create a new host mapping.
- The application does not read a `PORT` environment variable for public listeners.
- Public listener ports are stored in SQLite and managed through **Proxy**.
- Use a pinned release tag instead of `latest` when repeatability matters.

## Runtime Effects

The runtime image creates a non-root `p2pstream` user and grants the binary `cap_net_bind_service` so it can bind low ports. State is stored in `/data`, including SQLite, generated certificates, ACME material, and default public cache storage.

`MANAGEMENT_UI_DISABLED=true` stops serving the browser UI from the management listener. The ConnectRPC API and agent WebSocket remain available.

## Examples

Start the server:

```bash
cp .env.example .env
# edit MANAGEMENT_PUBLIC_URL in .env
docker compose up -d
docker compose logs -f p2pstream
```

Reset a forgotten password against the mounted `/data` database:

```bash
docker compose exec p2pstream p2pstream users reset-password admin
```

Generated agent container shape:

```yaml
services:
  p2pstream-agent:
    image: ghcr.io/kirari04/p2pstream:latest
    command: ["/app/p2pstream", "agent"]
    environment:
      MANAGEMENT_URL: "https://proxy.example.com:8081"
      MANAGEMENT_CA_PEM_BASE64: "..."
      AGENT_ID: "agent-..."
      AGENT_TOKEN: "..."
    restart: unless-stopped
```

## Related Tasks

- [Docker Compose quickstart](../getting-started/quickstart)
- [Docker Compose details](../getting-started/docker-compose)
- [Upgrades](../operations/upgrades)

# Docker Reference

Docker Compose is the recommended server deployment path for p2pstream. The repository includes a root `compose.yaml` that runs the management server, publishes the default public listener ports, and persists state in the `p2pstream-data` volume.

Start the server:

```bash
cp .env.example .env
# edit MANAGEMENT_PUBLIC_URL in .env
docker compose up -d
```

Follow logs:

```bash
docker compose logs -f p2pstream
```

## Image

Released images are published at:

```text
ghcr.io/kirari04/p2pstream
```

Common tags:

```text
latest
vX.Y.Z
```

For repeatable deployments, pin a release tag in `compose.yaml` instead of using `latest`.

## Runtime image

The runtime image:

- includes `/app/p2pstream`,
- includes the built management UI,
- sets `MANAGEMENT_UI_DIST_DIR=/app/web/management/dist`,
- sets `MANAGEMENT_PORT=8081`,
- sets `PORT=80`,
- sets `CONFIG_DIR=/data`,
- declares `/data` as a volume,
- exposes `80`, `443`, and `8081`,
- runs `/app/p2pstream server`.

## Published ports

The runtime image exposes `80`, `443`, and `8081`, but Docker only publishes what the Compose file maps.

```yaml
ports:
  - "${P2PSTREAM_HTTP_PORT:-80}:80"
  - "${P2PSTREAM_HTTPS_PORT:-443}:443"
  - "${P2PSTREAM_MANAGEMENT_PORT:-8081}:8081"
```

If you create listeners on ports other than `80` and `443`, publish them explicitly. Docker cannot expose ports created later in the management UI unless the host mapping exists.

## Agent container

The Agent Setup dialog can generate a Docker Compose service for an agent:

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

Use the generated `AGENT_ID`, `AGENT_TOKEN`, and CA material from the management UI.

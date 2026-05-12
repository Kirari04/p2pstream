# Docker Reference

Released images are published at:

```text
ghcr.io/kirari04/p2pstream
```

Common tags:

```text
latest
vX.Y.Z
```

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

## Server container

```bash
docker run -d \
  --name p2pstream \
  --restart unless-stopped \
  -p 80:80 \
  -p 443:443 \
  -p 8081:8081 \
  -v p2pstream-data:/data \
  ghcr.io/kirari04/p2pstream:latest
```

## Agent container

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

## Dynamic listener ports

If you create listeners on ports other than `80` and `443`, publish them explicitly. Docker cannot expose ports created later in the management UI unless the host mapping exists.

# Docker Compose

Use Compose when p2pstream should survive host reboots and retain state in a named volume.

## Compose file

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
      MANAGEMENT_PUBLIC_URL: "https://proxy.example.com:8081"
    ports:
      - "80:80"
      - "443:443"
      - "8081:8081"
    volumes:
      - p2pstream-data:/data

volumes:
  p2pstream-data:
```

Start it:

```bash
docker compose up -d
```

Open:

```text
https://proxy.example.com:8081
```

## NAT and published ports

Docker only exposes ports listed under `ports`. If you create an extra listener in the management UI, publish that port in Compose too.

Example for an additional listener on container port `8088`:

```yaml
ports:
  - "80:80"
  - "443:443"
  - "8081:8081"
  - "8088:8088"
```

If the server is behind NAT, forward public ports from the router or cloud firewall to the host. Agents must reach the management URL, not the public listener URL.

## Management URL

Set `MANAGEMENT_PUBLIC_URL` to the URL that browsers and agents should use for the management UI/API. This is especially important when:

- the container port is published to a different host port,
- the server is behind NAT,
- management is behind another reverse proxy,
- the auto-generated management certificate needs extra names.

For extra certificate names in auto TLS mode, set `MANAGEMENT_TLS_EXTRA_HOSTS` to a comma-separated list.

## Upgrade

```bash
docker compose pull
docker compose up -d
```

Keep the `/data` volume mounted. It contains the SQLite database and generated certificates.

## Next

- [Backup and restore](../operations/backup-restore)
- [Docker reference](../reference/docker)
- [Management TLS reference](../reference/management-tls)

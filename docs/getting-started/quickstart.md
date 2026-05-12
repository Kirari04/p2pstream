# Quickstart

This starts the management server, public HTTP listener, and public HTTPS listener in one container. Configuration, SQLite, and generated certificates are persisted in `/data`.

## Start the container

```bash
docker volume create p2pstream-data

docker run -d \
  --name p2pstream \
  --restart unless-stopped \
  -p 80:80 \
  -p 443:443 \
  -p 8081:8081 \
  -v p2pstream-data:/data \
  ghcr.io/kirari04/p2pstream:latest
```

Open the management UI:

```text
https://your-server:8081
```

The management server uses HTTPS by default. If you did not provide a certificate, p2pstream creates a local management CA and server certificate under `/data/certs/management`. Your browser will warn about the certificate until you trust that local CA or place management behind your own trusted TLS endpoint.

## Complete first setup

The first admin user can be created only while the setup window is open. The setup window lasts 5 minutes after server start when no users exist.

1. Open `https://your-server:8081`.
2. Create the admin user.
3. Use a password with at least 12 characters.
4. Log in and open the Management page.

If the setup window expires before any user exists, restart the container and try again.

## Replace the default backend

On a new database, p2pstream seeds:

| Object | Default |
| --- | --- |
| Backend | `default` -> `https://httpbin.org` |
| HTTP listener | `public-http` on `:80` |
| HTTPS listener | `public-https` on `:443` |
| HTTPS fallback certificate | self-signed mapping for `p2pstream.local` |

Before putting DNS on the server, replace the default backend with your real upstream or disable the listeners.

## Firewall checklist

Open only the ports you need:

| Port | Use |
| --- | --- |
| `80/tcp` | public HTTP listener and ACME HTTP-01 |
| `443/tcp` | public HTTPS listener and ACME TLS-ALPN-01 |
| `8081/tcp` | management UI/API and agent connections |

If management is reachable through NAT, Docker port publishing, or another reverse proxy, set `MANAGEMENT_PUBLIC_URL` so generated agent setup commands contain the correct URL.

## Next

- [Docker Compose](./docker-compose)
- [First login details](./first-login)
- [Publish a service](../guides/publish-a-service)

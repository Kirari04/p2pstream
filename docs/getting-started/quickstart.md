# Docker Compose Quickstart

This starts the management server, public HTTP listener, and public HTTPS listener in one container. Configuration, SQLite, and generated certificates are persisted in the `p2pstream-data` Docker volume.

## Start with Compose

From the repository root, copy the example environment file and set the management URL that browsers and agents will use:

```bash
cp .env.example .env
```

Edit `.env`:

```dotenv
MANAGEMENT_PUBLIC_URL=https://your-server:8081
```

Start p2pstream:

```bash
docker compose up -d
docker compose logs -f p2pstream
```

Open the management UI:

```text
https://your-server:8081
```

The management server uses HTTPS by default. If you did not provide a certificate, p2pstream creates a local management CA and server certificate under `/data/certs/management`. Your browser will warn about the certificate until you trust that local CA or place management behind your own trusted TLS endpoint.

If management is reachable through NAT, Docker port publishing, or another reverse proxy, `MANAGEMENT_PUBLIC_URL` must contain the externally reachable URL so generated agent setup commands contain the correct URL and certificate names.

## Complete first setup

The first admin user can be created only while the setup window is open. The setup window lasts 5 minutes after server start when no users exist.

1. Open `https://your-server:8081`.
2. Create the admin user.
3. Use a password with at least 12 characters.
4. Log in and open the Proxy page when you are ready to configure listeners, backends, and routes.

If the setup window expires before any user exists, restart the container and try again:

```bash
docker compose restart p2pstream
```

If you later forget the admin password, reset it with `p2pstream users reset-password USERNAME` against the same persisted `/data` database. See [First login](./first-login#if-you-lock-yourself-out) for the recovery command.

## Default welcome site

On a new database, p2pstream seeds:

| Object | Default |
| --- | --- |
| Backend | `default` static welcome page |
| HTTP listener | `public-http` on `:80` |
| HTTPS listener | `public-https` on `:443` |
| Routes | catch-all `/` routes to the `default` backend |
| HTTPS fallback certificate | self-signed mapping for `p2pstream.local` |

The seeded backend serves a local `Welcome to p2pstream proxy` page. Replace the backend, add routes, or disable listeners when you are ready to publish real traffic.

## Firewall checklist

Open only the ports you need:

| Port | Use |
| --- | --- |
| `80/tcp` | public HTTP listener and ACME HTTP-01 |
| `443/tcp` | public HTTPS listener and ACME TLS-ALPN-01 |
| `8081/tcp` | management UI/API and agent connections |

Docker only publishes the ports listed in `compose.yaml`. If you add listeners on other ports in the management UI, publish those ports in Compose too.

## Next

- [Docker Compose details](./docker-compose)
- [First login details](./first-login)
- [Publish a service](../guides/publish-a-service)

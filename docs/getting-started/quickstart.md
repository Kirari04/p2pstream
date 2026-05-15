# Docker Compose Quickstart

Start p2pstream with Docker Compose, persist runtime state in the `p2pstream-data` volume, and open the management UI over HTTPS.

## Use This When

Use this path for the normal self-hosted server deployment on a VPS, home lab host, or small private fleet. Compose starts one server container with the management UI/API on `8081`, a seeded HTTP listener on `80`, and a seeded HTTPS listener on `443`.

## Prerequisites

- Docker Engine with the Docker Compose plugin.
- Host ports `80`, `443`, and `8081` available, or adjusted host port mappings in `.env`.
- A management hostname or IP address that browsers and agents can reach.

## Steps

1. Clone the repository and enter it:

   ```bash
   git clone https://github.com/Kirari04/p2pstream.git
   cd p2pstream
   ```

2. Copy the example environment file:

   ```bash
   cp .env.example .env
   ```

3. Edit `.env` so generated browser links and agent snippets use the real management URL:

   ```dotenv
   MANAGEMENT_PUBLIC_URL=https://your-server:8081
   ```

4. Start the server:

   ```bash
   docker compose up -d
   docker compose logs -f p2pstream
   ```

5. Open management:

   ```text
   https://your-server:8081
   ```

The management server uses HTTPS by default. In auto mode, p2pstream creates a local management CA and server certificate under `/data/certs/management`; browsers warn until you trust that CA or place management behind trusted TLS.

## Verification

The first browser visit should show **Setup Admin**. After setup, **Overview** should load and the **Proxy** page should show the seeded `public-http` and `public-https` listeners plus the `default` static backend.

On a new database, p2pstream seeds:

| Object | Default |
| --- | --- |
| Backend | `default` static welcome page |
| HTTP listener | `public-http` on `:80` |
| HTTPS listener | `public-https` on `:443` |
| Routes | catch-all `/` routes to the `default` backend |
| HTTPS fallback certificate | self-signed mapping for `p2pstream.local` |

The seeded backend serves a local `Welcome to p2pstream proxy` page. Replace it or add more specific routes before publishing real traffic.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| Browser cannot connect | Confirm `docker ps`, host firewall, and `P2PSTREAM_MANAGEMENT_PORT`. |
| Certificate warning | Expected with auto management TLS until the generated CA is trusted. |
| Agent snippets use the wrong URL | Fix `MANAGEMENT_PUBLIC_URL` and restart with `docker compose up -d`. |
| Public listener does not answer | Confirm the listener exists in **Proxy** and the port is published by Compose. |

See [Troubleshooting](../operations/troubleshooting) for route, TLS, agent, and cache-specific checks.

## Next Steps

- [First login](./first-login)
- [Docker Compose details](./docker-compose)
- [Publish a service](../guides/publish-a-service)

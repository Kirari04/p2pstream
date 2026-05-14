# p2pstream

p2pstream is a self-hosted public reverse proxy with a web management UI, optional remote agents, static and forwarded backends, TLS automation, WAF challenges, rate limits, traffic shaping, and live traffic tracing.

## Quick Start With Docker Compose

Prerequisite: Docker Engine with the Docker Compose plugin installed. See Docker's install docs if Docker is not already available on your server: <https://docs.docker.com/engine/install/>.

```bash
git clone https://github.com/Kirari04/p2pstream.git
cd p2pstream

cp .env.example .env
# edit MANAGEMENT_PUBLIC_URL in .env, for example:
# MANAGEMENT_PUBLIC_URL=https://your-server:8081

docker compose up -d
docker compose logs -f p2pstream
```

Open the management UI:

```text
https://your-server:8081
```

`your-server` must match the externally reachable hostname or IP address, and should match `MANAGEMENT_PUBLIC_URL` in `.env`.

## First Login

The first admin user can be created only when no users exist and the setup window is open. The setup window lasts 5 minutes after server start.

If the setup window expires before any user exists, restart the container:

```bash
docker compose restart p2pstream
```

If you forget the admin password later, reset it inside the running container so the command uses the persisted `/data` volume:

```bash
docker compose exec p2pstream p2pstream users reset-password admin
```

## What Compose Starts

| Host port | Purpose |
| --- | --- |
| `80` | Public HTTP listener and ACME HTTP-01 |
| `443` | Public HTTPS listener and ACME TLS-ALPN-01 |
| `8081` | Management UI/API and agent connections |

Runtime state is stored in the named Docker volume `p2pstream-data`. It contains the SQLite database plus generated management, public TLS, and ACME material. Keep this volume during upgrades or server moves unless you intentionally want to reset the instance.

## Common Operations

```bash
docker compose logs -f p2pstream
docker compose restart p2pstream
docker compose pull
docker compose up -d
docker compose down
```

`docker compose down` stops and removes the container and network, but it does not remove the named `p2pstream-data` volume. For repeatable deployments, pin a release tag in `compose.yaml` instead of using `latest`.

Published images are available from GitHub Container Registry:

```text
ghcr.io/kirari04/p2pstream:latest
```

## Default Deployment Notes

The default Compose file publishes ports `80`, `443`, and `8081`. Override host ports in `.env` when needed:

```dotenv
P2PSTREAM_HTTP_PORT=80
P2PSTREAM_HTTPS_PORT=443
P2PSTREAM_MANAGEMENT_PORT=8081
```

If management is behind NAT or another reverse proxy, set `MANAGEMENT_PUBLIC_URL` to the external URL so browser links and generated agent setup snippets are correct.

Management HTTPS is enabled by default. Without your own trusted certificate, p2pstream generates a local management CA and server certificate, so browsers may show a certificate warning until you trust that CA or place management behind trusted TLS.

## Documentation

Full self-hosting and operations documentation is available at <https://kirari04.github.io/p2pstream/>.

- [Docker Compose quickstart](https://kirari04.github.io/p2pstream/getting-started/quickstart)
- [First login](https://kirari04.github.io/p2pstream/getting-started/first-login)
- [Publish a service](https://kirari04.github.io/p2pstream/guides/publish-a-service)
- [Backup and restore](https://kirari04.github.io/p2pstream/operations/backup-restore)
- [WAF reference](https://kirari04.github.io/p2pstream/reference/waf)

## Agent Install

Create an agent in the management UI. The setup dialog gives you an `AGENT_ID` and one-time `AGENT_TOKEN`, then provides either a Linux systemd installer or a Docker Compose snippet.

Agents connect to `MANAGEMENT_PUBLIC_URL`, usually `https://your-server:8081`. If p2pstream generated the management TLS certificate, use the CA material from the setup dialog so the agent can verify management HTTPS.

## Local Development

```bash
make dev
make test
make build
make docker-build
make docker-smoke
```

## Releases

GitHub Actions verifies the project, publishes a multi-arch Linux container to GHCR, creates the next patch tag from `main`, and attaches Linux `amd64` and `arm64` binary release archives plus `checksums.txt`.

No open-source license has been selected yet. Public visibility does not grant additional license rights.

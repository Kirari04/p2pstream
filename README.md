<p align="center">
  <img src="docs/public/logo-mark.svg" width="96" height="96" alt="p2pstream logo">
</p>

# p2pstream

p2pstream is a self-hosted public reverse proxy with a web management UI, optional remote agents, route-owned proxy/static targets, TLS automation, WAF challenges, public asset caching, rate limits, traffic shaping, and live traffic tracing.

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

| Host port | Purpose                                    |
| --------- | ------------------------------------------ |
| `80`      | Public HTTP listener and ACME HTTP-01      |
| `443`     | Public HTTPS listener and ACME TLS-ALPN-01 |
| `8081`    | Management UI/API and agent connections    |

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

Use `latest` or a pinned `vX.Y.Z` tag for stable deployments. The `nightly` tag is rebuilt from the `dev` branch for testing unreleased changes.

## Default Deployment Notes

The default Compose file publishes ports `80`, `443`, and `8081`. Override host ports in `.env` when needed:

```dotenv
P2PSTREAM_HTTP_PORT=80
P2PSTREAM_HTTPS_PORT=443
P2PSTREAM_MANAGEMENT_PORT=8081
```

If management is behind NAT or another reverse proxy, set `MANAGEMENT_PUBLIC_URL` to the external URL so browser links and generated agent setup snippets are correct.

For API-only management, set `MANAGEMENT_UI_DISABLED=true`; agents and the management API keep working, but the browser UI is not served. Agents use an authenticated Yamux tunnel over management TLS; if management is behind another reverse proxy, allow HTTP/1.1 upgrade streaming for `p2pstream-yamux` on `/agent/tunnel`.

Management HTTPS is enabled by default. Without your own trusted certificate, p2pstream generates a local management CA and server certificate, so browsers may show a certificate warning until you trust that CA or place management behind trusted TLS.

## Documentation

Full self-hosting and operations documentation is available at <https://kirari04.github.io/p2pstream/>.

- [Docker Compose quickstart](https://kirari04.github.io/p2pstream/getting-started/quickstart)
- [First login](https://kirari04.github.io/p2pstream/getting-started/first-login)
- [Publish a service](https://kirari04.github.io/p2pstream/guides/publish-a-service)
- [Response templates reference](https://kirari04.github.io/p2pstream/reference/response-templates)
- [Backup and restore](https://kirari04.github.io/p2pstream/operations/backup-restore)
- [WAF reference](https://kirari04.github.io/p2pstream/reference/waf)
- [Cache reference](https://kirari04.github.io/p2pstream/reference/cache)

## Agent Install

Create an agent in the management UI. The setup dialog gives you an `AGENT_ID` and one-time `AGENT_TOKEN`, then provides either a Linux systemd installer or a Docker Compose snippet.

Agents connect to `MANAGEMENT_PUBLIC_URL`, usually `https://your-server:8081`. If p2pstream generated the management TLS certificate, use the CA material from the setup dialog so the agent can verify management HTTPS.

For shell-installed agents, uninstall and full-purge commands are documented in the [systemd operations guide](https://kirari04.github.io/p2pstream/operations/systemd#uninstall-agent).

## Local Development

```bash
make dev
make test
make build
make docker-build
make docker-smoke
```

Development happens on the `dev` branch. Open normal feature and dependency PRs against `dev`; merge `dev` into `main` only when you want to publish a stable release.

## Releases

GitHub Actions verifies the project, publishes a multi-arch Linux container to GHCR, creates the next patch tag from `main`, and attaches Linux `amd64` and `arm64` binary release archives plus `checksums.txt`. The `main` branch is release-only; merging `dev` into `main` publishes the next stable release.

A scheduled nightly workflow builds the current `dev` branch and publishes the Docker-only `ghcr.io/kirari04/p2pstream:nightly` tag. Nightly images are for development validation and should not be used as repeatable production pins.

## License

p2pstream is licensed under the GNU Affero General Public License version 3 or later (`AGPL-3.0-or-later`). See [LICENSE](LICENSE) for the complete license text and [NOTICE](NOTICE) for the project notice.

Official binaries, Docker images, and release assets provide corresponding source through the matching tagged GitHub source archive. Running management instances also expose a source offer at:

```text
/.well-known/p2pstream/source
```

<p align="center">
  <img src="docs/public/logo-mark.svg" width="96" height="96" alt="p2pstream logo">
</p>

# p2pstream

p2pstream is a self-hosted public reverse proxy with a web management UI, optional remote agents, route-owned proxy/static targets, built-in local-user and forward-auth access policies, TLS automation, WAF challenges, public asset caching, rate limits, traffic shaping, and live traffic tracing.

## Quick Start With Docker Compose

Prerequisites: Linux amd64/arm64 and Docker Engine with Compose. See [Docker installation](https://docs.docker.com/engine/install/).

For a new installation, the optional setup script creates the two files, detects an IPv4 address, starts Compose, and prints the management URL and initial setup token:

```bash
curl -fsSL https://raw.githubusercontent.com/Kirari04/p2pstream/main/scripts/setup-server.sh | sh
```

Add `sh -s -- --host proxy.example.com` in place of `sh` to use your domain. The script refuses existing deployments; it does not install Docker or change firewall rules. See the [quickstart](docs/getting-started/quickstart.md#optional-one-command-setup) for staging and port overrides.

The script URL becomes available when the helper reaches that release channel. Until then, use the two-file setup below. Run later Compose commands from the deployment directory printed by the helper; its private `.env` also retains the initial setup token.

Or set up the files yourself. Save [compose.yaml](compose.yaml) and [.env.example](.env.example) in one directory, naming the latter `.env`. Set your address in `.env`:

```dotenv
MANAGEMENT_PUBLIC_URL=https://your-server:8081
```

Then start the server from that directory:

```bash
docker compose up -d
docker compose logs -f p2pstream
```

That's the complete installation: `compose.yaml` and `.env`. No installer, Python, host updater, or `release.json` is required. The image defaults to `ghcr.io/kirari04/p2pstream:latest`. To use staging, set `P2PSTREAM_IMAGE=ghcr.io/kirari04/p2pstream:staging` in `.env`; an exact release tag or digest works too. See the [quickstart](docs/getting-started/quickstart.md) for the copyable files and port settings.

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

Edit `compose.yaml` and `.env` directly, then run `docker compose up -d` to apply settings. To update the selected image channel:

```bash
docker compose pull p2pstream
docker compose up -d p2pstream
docker compose logs --tail=100 p2pstream
```

Supported database migrations run automatically at startup. Keep the `p2pstream-data` volume and your `.env`; back up before upgrading. Exact version tags and digests stay pinned until you change `P2PSTREAM_IMAGE`. See [Upgrades](docs/operations/upgrades.md) for compatibility notes and [Backup and restore](docs/operations/backup-restore.md).

If an old server updater still manages your deployment, follow [its transition procedure](docs/operations/upgrades.md#leave-server-updater-enrollment). A standalone deployment with only an abandoned owner marker follows the [normal image upgrade path](docs/operations/upgrades.md#standalone-deployments-with-leftover-updater-state); no marker deletion is required.

`docker compose down` removes the container and network, but preserves the named volume. Avoid `down -v` unless intentionally deleting the installation.

Published channels are `ghcr.io/kirari04/p2pstream:latest` (stable), `:staging` (prereleases), and `:nightly` (development). Staging also publishes exact `vX.Y.Z-staging.N` tags. For a reproducible deployment, select an exact tag or digest in `.env`.

## Default Deployment Notes

The default Compose file publishes ports `80`, `443`, and `8081`. Override host ports in `.env` when needed:

```dotenv
P2PSTREAM_HTTP_PORT=80
P2PSTREAM_HTTPS_PORT=443
P2PSTREAM_MANAGEMENT_PORT=8081
```

If management is behind NAT or another reverse proxy, set `MANAGEMENT_PUBLIC_URL` to the external URL so browser links and generated agent setup snippets are correct.

For API-only management, set `MANAGEMENT_UI_DISABLED=true` in `.env`, then run `docker compose up -d`. Agents and the management API keep working, but the browser UI is not served. Agents use an authenticated Yamux tunnel over management TLS; if management is behind another reverse proxy, allow HTTP/1.1 upgrade streaming for `p2pstream-yamux` on `/agent/tunnel`.

Management HTTPS is enabled by default. Without your own trusted certificate, p2pstream generates a local management CA and server certificate, so browsers may show a certificate warning until you trust that CA or place management behind trusted TLS.

## Documentation

Full self-hosting and operations documentation is available at <https://kirari04.github.io/p2pstream/>.

- [Docker Compose quickstart](https://kirari04.github.io/p2pstream/getting-started/quickstart)
- [First login](https://kirari04.github.io/p2pstream/getting-started/first-login)
- [Publish a service](https://kirari04.github.io/p2pstream/guides/publish-a-service)
- [Response templates reference](https://kirari04.github.io/p2pstream/reference/response-templates)
- [Backup and restore](https://kirari04.github.io/p2pstream/operations/backup-restore)
- [WAF reference](https://kirari04.github.io/p2pstream/reference/waf)
- [Identity-aware access](https://kirari04.github.io/p2pstream/reference/access-control)
- [Cache reference](https://kirari04.github.io/p2pstream/reference/cache)

## Agent Install

Create an agent from **Agents** in the management UI. The **Install Agent** modal gives you an `AGENT_ID` and one-time `AGENT_TOKEN`, then provides Linux install, Docker Compose, and CLI snippets.

Copy the Linux command and run it on the agent host. It includes the issued credentials, downloads the installer and architecture-specific binary from the selected GitHub release, and verifies their SHA-256 checksums before installation. No token prompts or local file paths are required. Advanced options support independently verified local files. Managed updates pin an exact release or prerelease; other installs can resolve `latest` once to an exact release.

Existing Linux agents have **More → Reinstall / Repair**, which reuses their host token and includes managed-update setup when available. **Agents → Updates → Enable managed updates** performs the one-time enrollment while keeping the existing tunnel running. **Rotate token** remains a separate credential action. All setup actions share a dialog with an editable Management URL. Remote environments use their saved address when the server advertises localhost. Managed campaigns verify exact GitHub releases and SHA-256 manifests, preserve route quorum during activation, and roll back locally on failure. See [Managed agent updates](docs/operations/managed-agent-updates.md).

Agents connect through `MANAGEMENT_URL`, usually `https://your-server:8081`. The server's `MANAGEMENT_PUBLIC_URL` supplies that URL to generated setup snippets. If p2pstream generated the management TLS certificate, use the CA material from the **Install Agent** modal so the agent can verify management HTTPS.

For shell-installed agents, uninstall and full-purge commands are documented in the [systemd operations guide](https://kirari04.github.io/p2pstream/operations/systemd#uninstall-agent).

## Local Development

```bash
make dev
make test
make verify
make build
make docker-build
make docker-smoke
```

`make dev` and `make run` bind management to `127.0.0.1` by default; Vite also binds loopback. Explicit `MANAGEMENT_BIND_ADDRESS` settings in the environment or `.env` are preserved for intentional remote access. Existing database-configured public listeners are unchanged.

Without an explicit `AGENT_TOKEN`, these targets use Python 3 to generate a random 256-bit token once at `${CONFIG_DIR:-p2pstream-data}/.local-dev/agent-token`. Its directory is private (`0700`) and the token file is `0600`, ignored by Git, and published atomically without printing the credential. Keep this file with your development database: bootstrap startup updates the stored agent token hash, so deleting the file or changing from an explicit token to the generated default changes that agent's credential on the next start. The first upgrade from the old public development fallback intentionally replaces that insecure token once. An explicit `AGENT_TOKEN` (at least 32 characters) always takes precedence and is never written to this file. Unsafe or malformed saved files cause an error rather than silent replacement.

Browser test and documentation screenshot runners generate separate setup, agent, and administrator credentials per invocation and share them through the runner environment with their backend and workers. Helpers require those values and have no static fallback. Fixture listeners bind loopback; disposable databases keep the production welcome seed but disable its default public listeners before any socket opens. Tests explicitly enable their own loopback listeners. `bun run e2e:sites` uses only a built UI and a disposable Go server in a private temporary directory; run `bun run build` first. The default E2E and screenshot configurations also start Vite. Treat retained traces and test reports as private: they can contain credentials from that disposable run.

`make verify` is a clean-tree CI-style gate: run it with no staged, unstaged, or untracked files. It regenerates code and fails if generation leaves any tracked diff or untracked generated file, then runs installer syntax and lifecycle checks, Go tests and vet, management UI unit tests, typecheck, and production build. GitHub Actions runs those checks and also builds the runtime Docker image.

For benchmark packets, validation receipts, and investigation handoffs, follow
[Evidence storage and handoff](docs/development/evidence.md). Raw evidence is
maintained in a separate private repository.

Database migrations use the embedded Goose library at runtime. Existing databases must already have Goose migration history; see [Upgrades](docs/operations/upgrades.md) before using an older database or backup. For one-off migration scaffolding, run the pinned CLI outside the tracked tool set, for example `go run github.com/pressly/goose/v3/cmd/goose@v3.27.1 create <name> sql`.

Development happens on the `dev` branch. Open normal feature and dependency PRs against `dev`, merge `dev` into `staging` to publish staging binaries/images for validation, and merge `staging` into `main` only when you want to publish a stable release.

## Releases

GitHub Actions verifies the project and publishes each release in one automatic workflow. A `staging` push publishes a unique `vX.Y.Z-staging.N` prerelease; stable publishing is dispatched from `main` with an exact `vX.Y.Z`. Both channels build Linux `amd64`/`arm64` binaries and images, generate and verify a canonical SHA-256 manifest, publish immutable version/commit aliases, create the GitHub release, verify its uploaded assets, and then move only the matching channel alias (`staging` or `latest`). GitHub Releases is the managed agent updater's trusted distribution boundary; no separate release-signing ceremony or key is required.

A scheduled nightly workflow builds the current `dev` branch and publishes the Docker-only `ghcr.io/kirari04/p2pstream:nightly` tag. Nightly images are for development validation and should not be used as repeatable production pins.

## License

p2pstream is licensed under the GNU Affero General Public License version 3 or later (`AGPL-3.0-or-later`). See [LICENSE](LICENSE) for the complete license text and [NOTICE](NOTICE) for the project notice.

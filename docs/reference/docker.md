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
sha-abcdef0
staging
staging-sha-abcdef0
nightly
nightly-sha-abcdef0
```

Stable releases publish `latest`, a version tag such as `vX.Y.Z`, and a commit tag such as `sha-abcdef0` from the `main` branch. The mutable `staging` tags are pre-release images built from the `staging` branch and are intended for validation before a stable release. The `nightly` tags are Docker-only development images built from the `dev` branch; use them for testing unreleased changes, not for repeatable production deployments.

The runtime image:

| Runtime detail           | Value                      |
| ------------------------ | -------------------------- |
| Binary                   | `/app/p2pstream`           |
| Management UI dist       | `/app/web/management/dist` |
| Legal files              | `/app/legal`               |
| `ENV`                    | `production`               |
| `MANAGEMENT_UI_DIST_DIR` | `/app/web/management/dist` |
| `MANAGEMENT_PORT`        | `8081`                     |
| `CONFIG_DIR`             | `/data`                    |
| Volume                   | `/data`                    |
| Exposed ports            | `80`, `443`, `8081`        |
| Command                  | `/app/p2pstream server`    |

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
- Treat `staging` as mutable. It follows the current `staging` branch and can change before the final release.
- Treat `nightly` as unstable. It follows the current `dev` branch and can change without a release note.

## Runtime Effects

The runtime image creates a non-root `p2pstream` user and grants the binary `cap_net_bind_service` so it can bind low ports. State is stored in `/data`, including SQLite, generated certificates, ACME material, and default public cache storage.

`MANAGEMENT_UI_DISABLED=true` stops serving the browser UI from the management listener. The ConnectRPC API and agent Yamux tunnel remain available.

## License and Source

The runtime image is licensed as `AGPL-3.0-or-later` and includes license files under `/app/legal`. The image also carries OCI labels for the license, source repository, revision, and version.

Every management listener exposes the corresponding source offer at:

```text
/.well-known/p2pstream/source
```

The endpoint remains available even when `MANAGEMENT_UI_DISABLED=true`. If you modify p2pstream and provide network access to that modified version, AGPL section 13 requires that users interacting with it remotely have an opportunity to receive the corresponding source for your modified version.

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

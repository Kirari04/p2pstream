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
vX.Y.Z-staging.N
staging
staging-sha-abcdef0
nightly
nightly-sha-abcdef0
```

Stable releases publish `latest`, a version tag such as `vX.Y.Z`, and a commit tag such as `sha-abcdef0` from the `main` branch. Staging releases publish an immutable version tag such as `vX.Y.Z-staging.N`, a channel-specific commit tag, and the moving `staging` convenience alias only after the same build, manifest, and publication checks. The `nightly` tags are Docker-only development images built from the `dev` branch; use them for testing unreleased changes, not for repeatable production deployments.

Stable and staging release manifests record the exact multi-platform OCI index digest and its Linux `amd64`/`arm64` child descriptors. Pin that published digest, rather than a mutable channel tag, when the deployment must retain the same content identity. Docker pulls are authenticated by GHCR and content-addressed by the OCI digest.

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

The default Compose file maps:

```yaml
ports:
  - "${P2PSTREAM_HTTP_PORT:-80}:80"
  - "${P2PSTREAM_HTTPS_PORT:-443}:443"
  - "${P2PSTREAM_MANAGEMENT_PUBLISH_ADDRESS:-}:${P2PSTREAM_MANAGEMENT_PORT:-8081}:8081"
```

## Validation Rules

- Docker only publishes what Compose maps; creating a listener in the UI does not create a new host mapping.
- The application does not read a `PORT` environment variable for public listeners.
- Public listener ports are stored in SQLite and managed through **Proxy -> Listeners**.
- The release deployment package pins an OCI digest. Use a digest for repeatability; moving aliases are discovery conveniences.
- Treat `staging` as a mutable convenience alias; select an exact `vX.Y.Z-staging.N` release and pin its OCI digest for a repeatable deployment.
- Treat `nightly` as unstable. It follows the current `dev` branch and can change without a release note.

## Release installation assets

Stable and staging releases provide a checksummed `p2pstream_<version>_docker.py` preparation tool and `p2pstream_<version>_docker.tar.gz` bundle. The release manifest binds the bundle's hash and size and records the server's OCI image digests. Preparation requires an exact release and a destination directory; it creates operator-owned `compose.yaml` and `.env` files with the selected image pinned by digest. It does not start containers or manage subsequent settings and upgrades.

Edit those files directly and use `docker compose up -d` to apply configuration. For a software upgrade, prepare the selected release in a separate directory, back up the full data volume and deployment inputs, then update the existing deployment's image digest. See [Upgrades](../operations/upgrades).

Release publication uploads all assets to a draft, downloads and verifies them, and then publishes. Enable [GitHub release immutability](https://docs.github.com/en/code-security/how-tos/secure-your-supply-chain/establish-provenance-and-integrity/prevent-release-changes) in repository settings to enforce locked tags/assets at publication. The workflow reports whether GitHub actually marked the release immutable. Existing releases are not retroactively renamed or modified. OCI digests pin content; version names alone do not guarantee GitHub assets are immutable. Publisher and GitHub HTTPS remain the trust source; this workflow does not independently verify release attestations or implement publisher signatures.

## Runtime Effects

The runtime image creates a non-root `p2pstream` user and grants the binary `cap_net_bind_service` so it can bind low ports. State is stored in `/data`, including SQLite, generated certificates, ACME material, and default public cache storage.

Set `MANAGEMENT_UI_DISABLED=true` in `.env`, then run `docker compose up -d` to stop serving the browser UI from the management listener. The ConnectRPC API and agent Yamux tunnel remain available.

## License and Source

The runtime image is licensed as `AGPL-3.0-or-later` and includes license files under `/app/legal`. The image also carries OCI labels for the license, source repository, revision, and version.

Every management listener exposes the corresponding source offer at:

```text
/.well-known/p2pstream/source
```

The endpoint remains available even when `MANAGEMENT_UI_DISABLED=true`. If you modify p2pstream and provide network access to that modified version, AGPL section 13 requires that users interacting with it remotely have an opportunity to receive the corresponding source for your modified version.

## Examples

Prepare deployment inputs through the [release download quickstart](../getting-started/quickstart), then start the server:

```bash
cd p2pstream
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
      MANAGEMENT_TRUST_FILE: "/data/management-ca.pem"
      AGENT_ID: "agent-..."
      AGENT_TOKEN: "..."
    volumes:
      - p2pstream-agent-state:/data
    restart: unless-stopped
volumes:
  p2pstream-agent-state:
```

## Related Tasks

- [Docker Compose quickstart](../getting-started/quickstart)
- [Docker Compose details](../getting-started/docker-compose)
- [Upgrades](../operations/upgrades)

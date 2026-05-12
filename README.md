# p2pstream

p2pstream is a public reverse proxy and management server with optional remote agents. It can serve static responses, forward traffic directly from the server host, or route traffic through registered agents with per-backend load balancing, rate limits, traffic shaping, TLS automation, and live traffic tracing.

## Documentation

Self-hosting and operations documentation is available at <https://kirari04.github.io/p2pstream/>.

## Local Development

Install dependencies and start the Go server plus the management UI:

```bash
make dev
```

Run the backend test suite:

```bash
make test
```

Build the frontend and backend binaries:

```bash
make build
```

Build the local runtime container:

```bash
make docker-build
```

## Docker

Released images are published to GitHub Container Registry:

```bash
docker pull ghcr.io/kirari04/p2pstream:latest
```

Run the management server with persistent data in `/data`:

```bash
docker run --rm \
  --name p2pstream \
  -p 80:80 \
  -p 443:443 \
  -p 8081:8081 \
  -v p2pstream-data:/data \
  ghcr.io/kirari04/p2pstream:latest
```

The runtime image exposes:

- `80` for public HTTP listeners.
- `443` for public HTTPS listeners.
- `8081` for the HTTPS management API and UI.

Management HTTPS is enabled by default. When no certificate is provided, p2pstream creates a persisted local management CA and server certificate under `/data/certs/management`. Agents verify the management certificate with `MANAGEMENT_CA_FILE` or `MANAGEMENT_CA_PEM_BASE64`; they do not skip TLS verification by default. Plain HTTP management mode is only available with `MANAGEMENT_TLS_MODE=off` and `MANAGEMENT_ALLOW_INSECURE_HTTP=true`.
Set `MANAGEMENT_PUBLIC_URL=https://your-host:8081` when the management UI is reached through NAT, Docker port publishing, or a reverse proxy, so Agent Setup generates the correct URL and certificate names.

## Agent Install

Create an agent in the management UI. The setup dialog returns a generated `AGENT_ID` and a one-time `AGENT_TOKEN`.

For a Linux systemd agent, use the one-line installer shown by the Agent Setup dialog. The command has this shape:

```bash
curl -fsSL https://raw.githubusercontent.com/Kirari04/p2pstream/main/scripts/install-agent.sh | sudo env MANAGEMENT_URL='https://management.example.com:8081' MANAGEMENT_CA_PEM_BASE64='...' AGENT_ID='agent-...' AGENT_TOKEN='...' P2PSTREAM_REPOSITORY='Kirari04/p2pstream' bash
```

The installer downloads the latest Linux release binary, verifies its checksum, installs `/usr/local/bin/p2pstream`, writes `/etc/p2pstream/agent.env`, and enables `p2pstream-agent.service`.
If `MANAGEMENT_CA_PEM_BASE64` is present, the installer writes it to `/etc/p2pstream/management-ca.pem` and configures the agent with `MANAGEMENT_CA_FILE=/etc/p2pstream/management-ca.pem`.

## Agent Docker Compose

The Agent Setup dialog can also generate a Docker Compose service:

```yaml
services:
  p2pstream-agent:
    image: ghcr.io/kirari04/p2pstream:latest
    command: ["/app/p2pstream", "agent"]
    environment:
      MANAGEMENT_URL: "https://management.example.com"
      MANAGEMENT_CA_PEM_BASE64: "..."
      AGENT_ID: "agent-..."
      AGENT_TOKEN: "..."
    restart: unless-stopped
```

## Releases

GitHub Actions verifies the project, publishes a multi-arch Linux container to GHCR, creates the next patch tag from `main`, and attaches Linux `amd64` and `arm64` binary release archives plus `checksums.txt`.

No open-source license has been selected yet. Public visibility does not grant additional license rights.

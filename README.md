# p2pstream

p2pstream is a public reverse proxy and management server with optional remote agents. It can serve static responses, forward traffic directly from the server host, or route traffic through registered agents with per-backend load balancing, rate limits, traffic shaping, TLS automation, and live traffic tracing.

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
- `8081` for the management API and UI.

## Agent Install

Create an agent in the management UI. The setup dialog returns a generated `AGENT_ID` and a one-time `AGENT_TOKEN`.

For a Linux systemd agent, use the one-line installer shown by the Agent Setup dialog. The command has this shape:

```bash
curl -fsSL https://raw.githubusercontent.com/Kirari04/p2pstream/main/scripts/install-agent.sh | sudo env MANAGEMENT_URL='https://management.example.com' AGENT_ID='agent-...' AGENT_TOKEN='...' P2PSTREAM_REPOSITORY='Kirari04/p2pstream' bash
```

The installer downloads the latest Linux release binary, verifies its checksum, installs `/usr/local/bin/p2pstream`, writes `/etc/p2pstream/agent.env`, and enables `p2pstream-agent.service`.

## Agent Docker Compose

The Agent Setup dialog can also generate a Docker Compose service:

```yaml
services:
  p2pstream-agent:
    image: ghcr.io/kirari04/p2pstream:latest
    command: ["/app/p2pstream", "agent"]
    environment:
      MANAGEMENT_URL: "https://management.example.com"
      AGENT_ID: "agent-..."
      AGENT_TOKEN: "..."
    restart: unless-stopped
```

## Releases

GitHub Actions verifies the project, publishes a multi-arch Linux container to GHCR, creates the next patch tag from `main`, and attaches Linux `amd64` and `arm64` binary release archives plus `checksums.txt`.

No open-source license has been selected yet. Public visibility does not grant additional license rights.

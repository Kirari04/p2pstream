# CLI Reference

The runtime image and release archive contain one binary:

```bash
p2pstream
```

## Server

```bash
p2pstream server
```

The server command reads configuration from `.env` and environment variables. It starts:

- management UI/API on `MANAGEMENT_PORT`, default `8081`,
- public listeners from the SQLite configuration,
- ACME renewal scheduling when the database is available.

Common server invocation:

```bash
CONFIG_DIR=/var/lib/p2pstream \
MANAGEMENT_PUBLIC_URL=https://proxy.example.com:8081 \
p2pstream server
```

## Agent

```bash
p2pstream agent [flags]
```

| Flag | Env var | Description |
| --- | --- | --- |
| `--management-url` | `MANAGEMENT_URL` | HTTPS URL of management server. |
| `--agent-token` | `AGENT_TOKEN` | Bearer token from Agent Setup. |
| `--agent-id` | `AGENT_ID` | Generated agent public ID. |
| `--agent-name` | `AGENT_NAME` | Optional display name. |
| `--management-ca-file` | `MANAGEMENT_CA_FILE` | PEM CA bundle for management HTTPS. |
| `--management-ca-pem-base64` | `MANAGEMENT_CA_PEM_BASE64` | Base64 PEM CA bundle for management HTTPS. |
| `--tls-cert-file` | `AGENT_TLS_CERT_FILE` | Client certificate for management mTLS. |
| `--tls-key-file` | `AGENT_TLS_KEY_FILE` | Client private key for management mTLS. |
| `--allow-insecure-management` | `AGENT_ALLOW_INSECURE_MANAGEMENT` | Permit HTTP management URL. |

Example:

```bash
p2pstream agent \
  --management-url https://proxy.example.com:8081 \
  --management-ca-file /etc/p2pstream/management-ca.pem \
  --agent-id agent-abc123 \
  --agent-token "$AGENT_TOKEN"
```

If no management URL is provided, the agent guesses `https://<local-route-ip>:8081`. Production agents should use an explicit URL.

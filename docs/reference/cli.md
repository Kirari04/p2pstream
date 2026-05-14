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

Set `MANAGEMENT_UI_DISABLED=true` to serve only the management API and agent WebSocket on the management listener.

Common server invocation:

```bash
CONFIG_DIR=/var/lib/p2pstream \
MANAGEMENT_PUBLIC_URL=https://proxy.example.com:8081 \
p2pstream server
```

## Users

```bash
p2pstream users reset-password USERNAME [flags]
```

`users reset-password` is an offline recovery command. It updates the management user directly in the configured SQLite database and revokes that user's active login sessions. Run it on the host or container that can access the same `CONFIG_DIR` or `DATABASE_URL` used by the server.

Flags:

| Flag | Description |
| --- | --- |
| `--database-url` | Override `DATABASE_URL` for this operation. |
| `--password-env` | Read the new password from the named environment variable. |
| `--password-file` | Read the new password from a file. |

Interactive reset:

```bash
CONFIG_DIR=/var/lib/p2pstream \
p2pstream users reset-password admin
```

Noninteractive reset from an environment variable:

```bash
RESET_PASSWORD='new long password value' \
p2pstream users reset-password admin --password-env RESET_PASSWORD
```

Reset from a secret file:

```bash
p2pstream users reset-password admin --password-file /run/secrets/admin-password
```

Reset against an explicit database:

```bash
p2pstream users reset-password admin \
  --database-url 'file:/var/lib/p2pstream/p2pstream.db?mode=rwc'
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

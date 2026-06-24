# CLI Reference

The runtime image and release archive contain one binary: `p2pstream`.

Release archives also include `LICENSE`, `NOTICE`, `SOURCE.txt`, and third-party legal notices. p2pstream is licensed under `AGPL-3.0-or-later`; see [License](./license).

## Exact Commands And Flags

Start the server:

```bash
p2pstream server
```

Reset a management user's password:

```bash
p2pstream users reset-password USERNAME [flags]
```

| Flag | Description |
| --- | --- |
| `--database-url` | Override `DATABASE_URL` for this operation. |
| `--password-env` | Read the new password from the named environment variable. |
| `--password-file` | Read the new password from a file. |

Generate, inspect, and reconcile stored-secrets encryption:

```bash
p2pstream secrets generate-key [flags]
p2pstream secrets status [flags]
p2pstream secrets rewrap [flags]
```

| Command | Flag | Description |
| --- | --- | --- |
| `secrets generate-key` | `--format` | `env` or `json`; defaults to `env`. |
| `secrets status` | `--database-url` | Override `DATABASE_URL` for this operation. |
| `secrets status` | `--format` | `table` or `json`; defaults to `table`. |
| `secrets status` | `--batch-size` | Rows to scan per database batch; app-owned key files are scanned separately. |
| `secrets rewrap` | `--database-url` | Override `DATABASE_URL` for this operation. |
| `secrets rewrap` | `--format` | `table` or `json`; defaults to `table`. |
| `secrets rewrap` | `--batch-size` | Rows to scan or update per database batch; app-owned key files are scanned separately. |
| `secrets rewrap` | `--dry-run` | Report planned encryption and rewrap changes without writing. |
| `secrets rewrap` | `--yes` | Confirm writing encryption or rewrap changes. |

Start an agent:

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

## Validation Rules

- `users reset-password` requires a valid username and a password with at least 12 characters.
- Use only one password source: prompt, `--password-env`, or `--password-file`.
- `secrets rewrap` requires either `--dry-run` or `--yes`.
- `secrets rewrap --yes` requires a current direct key from `SECRETS_ENCRYPTION_KEY` or `SECRETS_ENCRYPTION_KEY_FILE`, or `SECRETS_ENCRYPTION_PROVIDER=vault-transit` with valid Vault settings.
- `secrets status` and `secrets rewrap` print counts, provider, and key IDs, not plaintext secret values.
- `agent` requires `AGENT_ID` and `AGENT_TOKEN`.
- Agent HTTP management URLs are rejected unless `--allow-insecure-management` or `AGENT_ALLOW_INSECURE_MANAGEMENT` is set.

## Runtime Effects

`p2pstream server` reads `.env` and environment variables, starts management on `MANAGEMENT_PORT`, starts public listeners from SQLite configuration, and starts ACME scheduling when available.

The server command also reads `SECRETS_ENCRYPTION_PROVIDER`, the direct-key settings, the Vault Transit settings, `SECRETS_ENCRYPTION_PREVIOUS_KEYS`, and `SECRETS_ENCRYPTION_REQUIRED` before registering listeners. If encrypted database rows or app-owned private-key files cannot be decrypted, or the configured provider cannot be reached, startup fails.

`secrets status` opens the same SQLite database, scans app-owned private-key files under `CONFIG_DIR/certs`, and reports plaintext, current-key, rewrap-needed, missing-key, invalid, and decrypt-failed counts by secret purpose. It does not print plaintext secret values or private-key material. `secrets rewrap --dry-run` performs the same preflight without writing. `secrets rewrap --yes` writes directly to SQLite and app-owned key files, so run it during a maintenance window or before starting the server when you want explicit operator-controlled rewrap instead of startup reconciliation.

`users reset-password` updates the configured SQLite database directly and revokes active sessions for that user. Run it where the same `CONFIG_DIR` or `DATABASE_URL` is available.

If no management URL is provided to the agent, it guesses `https://<local-route-ip>:8081`; production agents should use an explicit URL from the Agent Setup dialog.

## Examples

Server:

```bash
CONFIG_DIR=/var/lib/p2pstream \
MANAGEMENT_PUBLIC_URL=https://proxy.example.com:8081 \
SECRETS_ENCRYPTION_KEY_FILE=/etc/p2pstream/secrets-encryption.key \
p2pstream server
```

Generate a secrets-encryption key:

```bash
p2pstream secrets generate-key
```

Inspect and dry-run stored-secret reconciliation:

```bash
CONFIG_DIR=/var/lib/p2pstream \
SECRETS_ENCRYPTION_KEY_FILE=/etc/p2pstream/secrets-encryption.key \
p2pstream secrets status

CONFIG_DIR=/var/lib/p2pstream \
SECRETS_ENCRYPTION_KEY_FILE=/etc/p2pstream/secrets-encryption.key \
p2pstream secrets rewrap --dry-run
```

Inspect and rewrap with Vault Transit:

```bash
CONFIG_DIR=/var/lib/p2pstream \
SECRETS_ENCRYPTION_PROVIDER=vault-transit \
SECRETS_ENCRYPTION_VAULT_ADDR=https://vault.example.com \
SECRETS_ENCRYPTION_VAULT_TOKEN_FILE=/etc/p2pstream/vault-token \
SECRETS_ENCRYPTION_VAULT_KEY=p2pstream \
p2pstream secrets status

CONFIG_DIR=/var/lib/p2pstream \
SECRETS_ENCRYPTION_PROVIDER=vault-transit \
SECRETS_ENCRYPTION_VAULT_ADDR=https://vault.example.com \
SECRETS_ENCRYPTION_VAULT_TOKEN_FILE=/etc/p2pstream/vault-token \
SECRETS_ENCRYPTION_VAULT_KEY=p2pstream \
p2pstream secrets rewrap --dry-run
```

Interactive password reset:

```bash
CONFIG_DIR=/var/lib/p2pstream \
p2pstream users reset-password admin
```

Noninteractive reset:

```bash
RESET_PASSWORD='new long password value' \
p2pstream users reset-password admin --password-env RESET_PASSWORD
```

Agent:

```bash
p2pstream agent \
  --management-url https://proxy.example.com:8081 \
  --management-ca-file /etc/p2pstream/management-ca.pem \
  --agent-id agent-abc123 \
  --agent-token "$AGENT_TOKEN"
```

## Related Tasks

- [First login](../getting-started/first-login)
- [Release binary](../getting-started/binary)
- [Expose a home lab app](../guides/expose-a-home-lab-app)

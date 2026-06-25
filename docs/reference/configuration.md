# Configuration Reference

p2pstream loads `.env` when present, then environment variables, and derives defaults for SQLite, certificate, cache, and management URL settings.

## Exact Fields And Defaults

Public proxy listener ports are stored in SQLite and managed through the management UI/API. A new database seeds HTTP `80` and HTTPS `443`. Docker host port publishing is controlled by Compose variables such as `P2PSTREAM_HTTP_PORT`.

### Server Variables

Set these on the server process via `.env` or environment. They control management, storage, TLS, caching, and observability.

| Variable                         | Default                      | Description                                                                                  |
| -------------------------------- | ---------------------------- | -------------------------------------------------------------------------------------------- |
| `MANAGEMENT_PORT`                | `8081`                       | Management UI/API and agent tunnel port.                                                     |
| `MANAGEMENT_BIND_ADDRESS`        | `0.0.0.0`                    | Management bind address. Set `127.0.0.1` only when local-only management is intentional.      |
| `CONFIG_DIR`                     | `p2pstream-data`             | Directory for default SQLite database and certificates. Docker sets `/data`.                 |
| `DATABASE_URL`                   | derived                      | SQLite DSN. When unset, uses `${CONFIG_DIR}/p2pstream.db` with WAL and foreign keys enabled. |
| `ENV`                            | `development`                | Use `production` for production logging/cookie behavior.                                     |
| `MANAGEMENT_UI_DISABLED`         | `false`                      | Disable browser UI; ConnectRPC APIs and the agent Yamux tunnel remain available.             |
| `MANAGEMENT_UI_DIST_DIR`         | `web/management/dist`        | Built management UI files. Runtime image sets `/app/web/management/dist`.                    |
| `MANAGEMENT_UI_DEV_PROXY`        | empty                        | Development-only management UI proxy target.                                                 |
| `MANAGEMENT_COOKIE_SECURE`       | `false`                      | Force Secure cookies even when other secure-cookie conditions are absent.                    |
| `MANAGEMENT_TLS_MODE`            | `auto`                       | `auto`, `provided`, or `off`.                                                                |
| `MANAGEMENT_TLS_CERT_FILE`       | empty                        | Management server certificate for `provided` mode.                                           |
| `MANAGEMENT_TLS_KEY_FILE`        | empty                        | Management server private key for `provided` mode.                                           |
| `MANAGEMENT_TLS_CLIENT_CA_FILE`  | empty                        | Optional CA used to verify agent client certificates.                                        |
| `MANAGEMENT_ALLOW_INSECURE_HTTP` | `false`                      | Required when `MANAGEMENT_TLS_MODE=off`.                                                     |
| `MANAGEMENT_PUBLIC_URL`          | derived                      | Must be an absolute `https://` URL. Used in generated agent setup snippets and browser links. |
| `MANAGEMENT_SETUP_TOKEN`         | generated                    | Optional first-admin setup token. If unset, a one-time token is generated and logged.         |
| `MANAGEMENT_ADVERTISE_HOST`      | detected                     | Hostname/IP used for auto-generated management certificates and default URL.                 |
| `MANAGEMENT_TLS_EXTRA_HOSTS`     | empty                        | Comma-separated extra DNS/IP names for auto management TLS.                                  |
| `PUBLIC_CACHE_DIR`               | `${CONFIG_DIR}/cache/public` | Disk directory for public cache body files.                                                  |
| `BOOTSTRAP_AGENT_ID`             | empty                        | Bootstrap agent public ID. Must be set with name and token.                                  |
| `BOOTSTRAP_AGENT_NAME`           | empty                        | Bootstrap agent display name.                                                                |
| `BOOTSTRAP_AGENT_TOKEN`          | empty                        | Bootstrap agent token. Stored as a hash.                                                     |
| `OBSERVABILITY_RETENTION_DAYS`   | `30`                         | Retention window for recorded observability data.                                            |
| `OBSERVABILITY_MAX_ROWS`         | `1000000`                    | Maximum retained proxy request events and agent stat rows. Set `0` to disable this cap.       |
| `LOGIN_THROTTLE_MAX_KEYS`        | `50000`                      | Maximum in-memory login throttle keys; active blocks are retained until expiry.              |
| `SECRETS_ENCRYPTION_PROVIDER`    | `direct`                     | Stored-secret encryption provider: `direct` or `vault-transit`.                              |
| `SECRETS_ENCRYPTION_KEY`         | empty                        | Optional 32-byte base64/base64url key used directly to encrypt stored secrets.                 |
| `SECRETS_ENCRYPTION_KEY_FILE`    | empty                        | Optional `0400`/`0600` file containing the current encryption key. Use instead of `SECRETS_ENCRYPTION_KEY`. |
| `SECRETS_ENCRYPTION_KEY_ID`      | derived                      | Optional stable identifier stored with encrypted secret metadata.                             |
| `SECRETS_ENCRYPTION_PREVIOUS_KEYS` | empty                      | Comma-separated `key_id:key` entries used to decrypt and rewrap old encrypted secrets.        |
| `SECRETS_ENCRYPTION_REQUIRED`    | `false`                      | Reject plaintext stored secrets during startup when set to `true`.                           |
| `SECRETS_ENCRYPTION_VAULT_ADDR`  | empty                        | Vault base URL for `SECRETS_ENCRYPTION_PROVIDER=vault-transit`.                              |
| `SECRETS_ENCRYPTION_VAULT_TOKEN` | empty                        | Vault token. Prefer `SECRETS_ENCRYPTION_VAULT_TOKEN_FILE` for production.                     |
| `SECRETS_ENCRYPTION_VAULT_TOKEN_FILE` | empty                  | Optional `0400`/`0600` file containing the Vault token. Use instead of `SECRETS_ENCRYPTION_VAULT_TOKEN`. |
| `SECRETS_ENCRYPTION_VAULT_MOUNT` | `transit`                    | Vault Transit mount path.                                                                     |
| `SECRETS_ENCRYPTION_VAULT_KEY`   | empty                        | Vault Transit key name used to wrap per-secret data-encryption keys.                          |
| `SECRETS_ENCRYPTION_VAULT_NAMESPACE` | empty                    | Optional Vault Enterprise namespace.                                                          |
| `SECRETS_ENCRYPTION_VAULT_TIMEOUT` | `5s`                       | Per-request timeout for Vault Transit calls during startup, CLI status/rewrap, and writes.    |
| `SECRETS_ENCRYPTION_VAULT_DEK_CACHE_MAX_ENTRIES` | `1024`      | Maximum unwrapped Vault DEKs retained in process memory. Set `0` with cache TTL `0s` to disable. |
| `SECRETS_ENCRYPTION_VAULT_DEK_CACHE_TTL` | `5m`              | Maximum age for cached unwrapped Vault DEKs. Set `0s` with max entries `0` to disable.        |

If every login throttle slot is occupied by an active block, new failed-login keys are not tracked until a blocked key expires or a login succeeds for an existing key.

### Secrets Encryption

Stored-secret encryption covers upstream credentials, sensitive upstream request headers, TLS DNS provider tokens, WAF captcha secrets, WAF cookie signing material, remote-environment access tokens, and app-owned private-key files under `${CONFIG_DIR}/certs`. App-owned key files include auto management TLS keys, uploaded/generated public TLS keys, and ACME account keys. Operator-provided key files such as `MANAGEMENT_TLS_KEY_FILE`, `AGENT_TLS_KEY_FILE`, and manual public TLS file paths outside the app-managed cert layout are not rewritten.

Existing plaintext database rows are encrypted during server startup before listeners are registered while `SECRETS_ENCRYPTION_REQUIRED=false`. Existing plaintext app-owned private-key files are encrypted in place when a current encryption key or provider is configured; if required mode is enabled and a key file cannot be encrypted or decrypted, startup fails before listeners are registered.

`SECRETS_ENCRYPTION_PROVIDER=direct` is the local compatibility mode. `SECRETS_ENCRYPTION_KEY` or `SECRETS_ENCRYPTION_KEY_FILE` is used directly as the AES-256-GCM key for secret values stored in SQLite and app-owned key files. Prefer `SECRETS_ENCRYPTION_KEY_FILE` when your deployment secret manager can mount the key as a regular file with no group/other permissions, such as `0400` or `0600`; this avoids putting the key in the process environment.

`SECRETS_ENCRYPTION_PROVIDER=vault-transit` enables KEK/DEK envelope encryption. p2pstream asks Vault Transit for a plaintext 256-bit data key and a Vault-wrapped copy of that data key, encrypts the secret locally with AES-256-GCM, stores the ciphertext plus wrapped data key in SQLite or the app-owned key file, and later asks Vault Transit to unwrap only that data key. Successful unwraps are cached in process memory by wrapped DEK and row-bound context for a bounded TTL so repeated configuration reloads can avoid repeated Transit decrypt calls. Vault key rotation can rewrap the stored data key without rewriting the secret ciphertext. Use a Vault Transit key backed by your normal Vault custody model, such as managed/HSM-backed keys where available.

Create the Transit key with key derivation enabled. p2pstream passes the row-bound secret AAD as Vault `context` on data-key, decrypt, and rewrap calls, so a non-derived Transit key is rejected during startup or CLI provider checks:

```bash
vault secrets enable transit
vault write transit/keys/p2pstream type=aes256-gcm96 derived=true
```

The Vault provider uses the Transit data-key, decrypt, key-read, and rewrap APIs documented by HashiCorp: <https://developer.hashicorp.com/vault/api-docs/secret/transit>.

Runtime components still decrypt those values into process memory when they need to proxy requests, load TLS certificates, issue certificates, verify WAF challenges, or call remote environments. Keep host, volume, and backup access restricted because encrypted files still contain operational metadata and are recoverable by anyone with the matching direct key or Vault Transit access.

Generate a key with one of:

```bash
p2pstream secrets generate-key
```

```bash
openssl rand -base64 32
```

```bash
openssl rand -base64 32 | tr '+/' '-_' | tr -d '='
```

Set `SECRETS_ENCRYPTION_REQUIRED=true` after the first successful migration when you want startup to fail if any stored secret is still plaintext or cannot be decrypted. If encrypted rows or app-owned key files already exist and no matching key is configured, startup fails because the stored contents cannot be safely decrypted.

Use `p2pstream secrets status` to inspect encrypted state without printing secret values. It also reports the last successful startup or `secrets rewrap --yes` reconciliation timestamp, provider/key ID, and aggregate database/file counts when available. Use `p2pstream secrets rewrap --dry-run` before a rotation, provider migration, or required-mode change to confirm how many database rows and app-owned key files would be encrypted or rewrapped. `p2pstream secrets rewrap --yes` writes directly to SQLite and app-owned key files; run it during a maintenance window or before starting the server. Server startup still performs the same reconciliation before listeners are registered.

For direct-key rotation, configure the new key as `SECRETS_ENCRYPTION_KEY` or `SECRETS_ENCRYPTION_KEY_FILE`, keep the old key in `SECRETS_ENCRYPTION_PREVIOUS_KEYS`, and run `p2pstream secrets rewrap --dry-run`. Then either run `p2pstream secrets rewrap --yes` during a maintenance window or restart the server and let startup rewrap stored secrets to the current key before listeners are registered. During that window both keys must be configured. After `p2pstream secrets status` shows no rows or files needing rewrap for the previous key, remove the previous key on a later restart. `SECRETS_ENCRYPTION_KEY_ID` should be stable for the lifetime of a key; when omitted, p2pstream derives one from the key. Previous entries use `key_id:key`; if the key ID contains `:`, the last `:` separates the key ID from the key value.

To migrate from direct mode to Vault Transit, set `SECRETS_ENCRYPTION_PROVIDER=vault-transit`, configure Vault, and put the old direct key in `SECRETS_ENCRYPTION_PREVIOUS_KEYS`. Run `p2pstream secrets rewrap --dry-run`, then `p2pstream secrets rewrap --yes` or restart the server. After status shows no rows or files with the old direct key ID, remove `SECRETS_ENCRYPTION_PREVIOUS_KEYS`.

For Vault Transit key rotation within the same Transit key name, rotate the key in Vault and run `p2pstream secrets rewrap --dry-run`. Rows whose wrapped data key uses an older Vault key version are reported as rewrap-needed. `p2pstream secrets rewrap --yes` updates the wrapped data key through Vault Transit and preserves the secret ciphertext.

Vault Transit remains a startup dependency. p2pstream checks the provider before registering listeners and unwraps stored DEKs when loading configuration snapshots. Steady-state proxy traffic uses the cached in-memory snapshot, and repeated reloads can reuse the bounded in-process DEK cache while entries remain valid. A cold cache, cache expiry, changed wrapped DEKs, or process restart still requires Vault availability.

### Agent Variables

Set these on each agent host via `/etc/p2pstream/agent.env` or the generated installer environment. The agent installer writes these automatically from the setup dialog.

| Variable                          | Description                                                          |
| --------------------------------- | -------------------------------------------------------------------- |
| `MANAGEMENT_URL`                  | Management server URL, for example `https://proxy.example.com:8081`. |
| `AGENT_ID`                        | Generated agent public ID from management.                           |
| `AGENT_TOKEN`                     | One-time generated or rotated token from management.                 |
| `AGENT_NAME`                      | Optional local display name.                                         |
| `MANAGEMENT_CA_FILE`              | PEM CA bundle used to verify management HTTPS.                       |
| `MANAGEMENT_CA_PEM_BASE64`        | Base64 PEM CA bundle used to verify management HTTPS.                |
| `AGENT_TLS_CERT_FILE`             | Optional client certificate for management mTLS.                     |
| `AGENT_TLS_KEY_FILE`              | Optional client private key for management mTLS.                     |
| `AGENT_ALLOW_INSECURE_MANAGEMENT` | Allows HTTP management URL when truthy.                              |

### Installer Variables

Set these as environment variables before running the Linux agent installer script. They control where the binary is placed and which release is downloaded.

| Variable                 | Default                    | Description                                                                  |
| ------------------------ | -------------------------- | ---------------------------------------------------------------------------- |
| `P2PSTREAM_REPOSITORY`   | `Kirari04/p2pstream`       | GitHub owner/repo used by the installer.                                     |
| `P2PSTREAM_VERSION`      | `latest`                   | Installer binary channel: `latest`, `staging`, or a release tag such as `vX.Y.Z`. |
| `P2PSTREAM_CONFIG_DIR`   | `/etc/p2pstream`           | Agent config directory created by installer.                                 |
| `P2PSTREAM_INSTALL_PATH` | `/usr/local/bin/p2pstream` | Binary install path.                                                         |
| `P2PSTREAM_SYSTEMD_DIR`  | `/etc/systemd/system`      | Systemd unit directory used by installer and uninstaller.                    |

## Validation Rules

- `MANAGEMENT_TLS_MODE` must be `auto`, `provided`, or `off`.
- `MANAGEMENT_TLS_CERT_FILE` and `MANAGEMENT_TLS_KEY_FILE` must be set together.
- `MANAGEMENT_TLS_MODE=provided` requires both cert and key files.
- `MANAGEMENT_TLS_MODE=off` requires `MANAGEMENT_ALLOW_INSECURE_HTTP=true`.
- `MANAGEMENT_PUBLIC_URL` must be absolute and must use `https`, unless management TLS is off and insecure HTTP is explicitly allowed.
- `MANAGEMENT_BIND_ADDRESS` defaults to all interfaces so agents and remote clients can connect. Set it to `127.0.0.1` only for local-only management or when a local reverse proxy fronts management.
- Bootstrap agent ID, name, and token must all be set together.
- `SECRETS_ENCRYPTION_PROVIDER` must be `direct` or `vault-transit`.
- `SECRETS_ENCRYPTION_KEY` must decode to exactly 32 bytes as base64 or base64url.
- Set only one of `SECRETS_ENCRYPTION_KEY` or `SECRETS_ENCRYPTION_KEY_FILE`.
- `SECRETS_ENCRYPTION_KEY_FILE` must be a regular file, contain a non-empty 32-byte base64/base64url key, and have no group/other permission bits. Use `0400` or `0600`.
- In direct mode, `SECRETS_ENCRYPTION_REQUIRED=true` and `SECRETS_ENCRYPTION_PREVIOUS_KEYS` require a current key via `SECRETS_ENCRYPTION_KEY` or `SECRETS_ENCRYPTION_KEY_FILE`.
- In Vault Transit mode, do not set `SECRETS_ENCRYPTION_KEY`, `SECRETS_ENCRYPTION_KEY_FILE`, or `SECRETS_ENCRYPTION_KEY_ID`; use `SECRETS_ENCRYPTION_PREVIOUS_KEYS` only for decrypting old direct-mode rows and app-owned key files during migration.
- Vault Transit mode requires `SECRETS_ENCRYPTION_VAULT_ADDR`, `SECRETS_ENCRYPTION_VAULT_KEY`, and exactly one of `SECRETS_ENCRYPTION_VAULT_TOKEN` or `SECRETS_ENCRYPTION_VAULT_TOKEN_FILE`.
- The configured Vault Transit key must be created with `derived=true`.
- `SECRETS_ENCRYPTION_VAULT_TOKEN_FILE` must be a regular non-empty file with no group/other permission bits. Use `0400` or `0600`.
- `SECRETS_ENCRYPTION_VAULT_ADDR` must use `https`, except loopback `http` endpoints for local Vault development or tests.
- Vault DEK cache max entries and TTL must both be positive or both be zero. Max entries must be at most `10000`; TTL must be at most `1h`.
- `SECRETS_ENCRYPTION_PREVIOUS_KEYS` entries must be `key_id:key`, and key IDs must be unique.
- Agent boolean parsing accepts `1`, `true`, `yes`, `y`, and `on`.
- Linux agent installs require `AGENT_TLS_CERT_FILE` and `AGENT_TLS_KEY_FILE` together, require user-supplied TLS files to be readable, and reject CA/client-certificate settings with HTTP management URLs.

## Runtime Effects

`CONFIG_DIR` is created with `0700` permissions. The managed certificate directory is `${CONFIG_DIR}/certs`. SQLite database directories are created or tightened to `0700`, and database/WAL/SHM files are set to `0600`. If `DATABASE_URL` is unset, p2pstream also migrates a legacy local `p2pstream.db` into `${CONFIG_DIR}/p2pstream.db` when needed.

Management session cookies are Secure when management TLS is enabled, `ENV=production`, or `MANAGEMENT_COOKIE_SECURE=true`.

## Examples

Compose `.env`:

```dotenv
MANAGEMENT_PUBLIC_URL=https://proxy.example.com:8081
MANAGEMENT_BIND_ADDRESS=0.0.0.0
MANAGEMENT_TLS_EXTRA_HOSTS=proxy.example.com,192.0.2.10
SECRETS_ENCRYPTION_KEY=replace-with-32-byte-base64-key
# Or use SECRETS_ENCRYPTION_KEY_FILE=/run/secrets/p2pstream-secrets-key
SECRETS_ENCRYPTION_KEY_ID=primary-2026-06
SECRETS_ENCRYPTION_REQUIRED=true
P2PSTREAM_HTTP_PORT=80
P2PSTREAM_HTTPS_PORT=443
P2PSTREAM_MANAGEMENT_PORT=8081
```

Vault Transit `.env`:

```dotenv
SECRETS_ENCRYPTION_PROVIDER=vault-transit
SECRETS_ENCRYPTION_VAULT_ADDR=https://vault.example.com
SECRETS_ENCRYPTION_VAULT_TOKEN_FILE=/run/secrets/p2pstream-vault-token
SECRETS_ENCRYPTION_VAULT_MOUNT=transit
SECRETS_ENCRYPTION_VAULT_KEY=p2pstream
SECRETS_ENCRYPTION_REQUIRED=true
```

Compose defaults `MANAGEMENT_BIND_ADDRESS` to `0.0.0.0` inside the container; set it in `.env` to a narrower address only when the management service should not listen on every container interface.

Binary/systemd server environment:

```ini
CONFIG_DIR=/var/lib/p2pstream
MANAGEMENT_BIND_ADDRESS=0.0.0.0
MANAGEMENT_PUBLIC_URL=https://proxy.example.com:8081
ENV=production
SECRETS_ENCRYPTION_KEY=replace-with-32-byte-base64-key
# Or use SECRETS_ENCRYPTION_KEY_FILE=/etc/p2pstream/secrets-encryption.key
SECRETS_ENCRYPTION_KEY_ID=primary-2026-06
SECRETS_ENCRYPTION_REQUIRED=true
```

Vault Transit server environment:

```ini
CONFIG_DIR=/var/lib/p2pstream
MANAGEMENT_PUBLIC_URL=https://proxy.example.com:8081
ENV=production
SECRETS_ENCRYPTION_PROVIDER=vault-transit
SECRETS_ENCRYPTION_VAULT_ADDR=https://vault.example.com
SECRETS_ENCRYPTION_VAULT_TOKEN_FILE=/etc/p2pstream/vault-token
SECRETS_ENCRYPTION_VAULT_MOUNT=transit
SECRETS_ENCRYPTION_VAULT_KEY=p2pstream
SECRETS_ENCRYPTION_REQUIRED=true
```

## Related Tasks

- [Docker Compose details](../getting-started/docker-compose)
- [Systemd](../operations/systemd)
- [Management TLS reference](./management-tls)

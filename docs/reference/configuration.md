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
| `SECRETS_ENCRYPTION_KEY`         | empty                        | Optional 32-byte base64/base64url key used directly to encrypt stored upstream/API credentials. |
| `SECRETS_ENCRYPTION_KEY_ID`      | derived                      | Optional stable identifier stored with encrypted secret metadata.                             |
| `SECRETS_ENCRYPTION_PREVIOUS_KEYS` | empty                      | Comma-separated `key_id:key` entries used to decrypt and rewrap old encrypted secrets.        |
| `SECRETS_ENCRYPTION_REQUIRED`    | `false`                      | Reject plaintext stored secrets during startup when set to `true`.                           |

If every login throttle slot is occupied by an active block, new failed-login keys are not tracked until a blocked key expires or a login succeeds for an existing key.

### Secrets Encryption

`SECRETS_ENCRYPTION_KEY` enables versioned direct AES-256-GCM encryption for stored upstream credentials, sensitive upstream request headers, TLS DNS provider tokens, WAF captcha secrets, WAF cookie signing material, and remote-environment access tokens. Existing plaintext rows are encrypted during server startup before listeners are registered while `SECRETS_ENCRYPTION_REQUIRED=false`.

This setting protects secret values stored in SQLite. It is not KEK/DEK envelope encryption: the configured key encrypts secret values directly and is parsed from process configuration at startup. Runtime components still decrypt those values into process memory when they need to proxy requests, issue certificates, verify WAF challenges, or call remote environments. Certificate private-key files under `CONFIG_DIR/certs` remain file-backed and depend on host, volume, and backup access controls.

Generate a key with one of:

```bash
openssl rand -base64 32
```

```bash
openssl rand -base64 32 | tr '+/' '-_' | tr -d '='
```

Set `SECRETS_ENCRYPTION_REQUIRED=true` after the first successful migration when you want startup to fail if any stored secret is still plaintext. If encrypted rows already exist and no matching key is configured, startup fails because the database contents cannot be safely decrypted.

For rotation, restart the server with the new key as `SECRETS_ENCRYPTION_KEY`, keep the old key in `SECRETS_ENCRYPTION_PREVIOUS_KEYS`, and let startup rewrap stored secrets to the current key before listeners are registered. During that startup window both keys must be configured; after a successful rewrap, remove the previous key on a later restart. `SECRETS_ENCRYPTION_KEY_ID` should be stable for the lifetime of a key; when omitted, p2pstream derives one from the key. Previous entries use `key_id:key`; if the key ID contains `:`, the last `:` separates the key ID from the key value.

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
- `SECRETS_ENCRYPTION_KEY` must decode to exactly 32 bytes as base64 or base64url.
- `SECRETS_ENCRYPTION_REQUIRED=true` requires `SECRETS_ENCRYPTION_KEY`.
- `SECRETS_ENCRYPTION_PREVIOUS_KEYS` requires `SECRETS_ENCRYPTION_KEY`; every entry must be `key_id:key`, and key IDs must be unique.
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
SECRETS_ENCRYPTION_KEY_ID=primary-2026-06
SECRETS_ENCRYPTION_REQUIRED=true
P2PSTREAM_HTTP_PORT=80
P2PSTREAM_HTTPS_PORT=443
P2PSTREAM_MANAGEMENT_PORT=8081
```

Compose defaults `MANAGEMENT_BIND_ADDRESS` to `0.0.0.0` inside the container; set it in `.env` to a narrower address only when the management service should not listen on every container interface.

Binary/systemd server environment:

```ini
CONFIG_DIR=/var/lib/p2pstream
MANAGEMENT_BIND_ADDRESS=0.0.0.0
MANAGEMENT_PUBLIC_URL=https://proxy.example.com:8081
ENV=production
SECRETS_ENCRYPTION_KEY=replace-with-32-byte-base64-key
SECRETS_ENCRYPTION_KEY_ID=primary-2026-06
SECRETS_ENCRYPTION_REQUIRED=true
```

## Related Tasks

- [Docker Compose details](../getting-started/docker-compose)
- [Systemd](../operations/systemd)
- [Management TLS reference](./management-tls)

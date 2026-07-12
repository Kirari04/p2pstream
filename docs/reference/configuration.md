# Configuration Reference

p2pstream loads `.env` when present, then environment variables, and derives defaults for SQLite, certificate, cache, GeoIP, and management URL settings.

## Exact Fields And Defaults

Public proxy listener ports are stored in SQLite and managed through **Proxy -> Listeners** in the management UI or through the management API. A new database seeds HTTP `80` and HTTPS `443`. Docker host port publishing is controlled separately by Compose variables such as `P2PSTREAM_HTTP_PORT`.

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
| `TUNNEL_MAX_STREAM_WINDOW_BYTES` | `2097152`                    | Maximum Yamux receive window per tunnel stream. Raise for high-RTT/high-bandwidth agent links. |
| `TUNNEL_MAX_CONCURRENT_REQUESTS` | `64`                         | Maximum concurrent public agent-route requests and live agent tunnel streams across the server. |

If every login throttle slot is occupied by an active block, new failed-login keys are not tracked until a blocked key expires or a login succeeds for an existing key.

### Agent Variables

Set these on each agent host via `/etc/p2pstream/agent.env` or the generated installer environment. The agent installer writes the generated setup values automatically; optional local hardening values such as `AGENT_ALLOW_TARGETS` can be supplied before running the installer or added to the env file afterward.

When tunnel window or concurrency values are supplied to the installer, they are written to `agent.env` and the effective last numeric assignments are preserved by later reinstalls that do not provide replacements. Preservation is preflighted before installer mutations and fails closed if the existing environment file is unreadable or uses unsupported or multiline syntax. Supplying both numeric values explicitly avoids reading them from the old file, but the installer may still read that file to preserve `AGENT_ALLOW_TARGETS` unless the allowlist is also supplied or explicitly cleared.

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
| `TUNNEL_MAX_STREAM_WINDOW_BYTES`  | Maximum Yamux receive window per tunnel stream. Defaults to `2097152`. |
| `TUNNEL_MAX_CONCURRENT_REQUESTS`  | Maximum concurrent requests handled by this agent. Defaults to `64`. |
| `AGENT_ALLOW_TARGETS`             | Optional tunnel destination allowlist entries separated by commas or whitespace. |

### Installer Variables

Set these as environment variables before running the Linux agent installer script. They control where the binary is placed and which release is downloaded.

| Variable                 | Default                    | Description                                                                  |
| ------------------------ | -------------------------- | ---------------------------------------------------------------------------- |
| `P2PSTREAM_REPOSITORY`   | `Kirari04/p2pstream`       | GitHub owner/repo used by the installer.                                     |
| `P2PSTREAM_VERSION`      | `latest`                   | Installer binary channel: `latest`, `staging`, or a release tag such as `vX.Y.Z`. |
| `P2PSTREAM_CONFIG_DIR`   | `/etc/p2pstream`           | Agent config directory created by installer.                                 |
| `P2PSTREAM_INSTALL_PATH` | `/usr/local/bin/p2pstream` | Binary install path.                                                         |
| `P2PSTREAM_SYSTEMD_DIR`  | `/etc/systemd/system`      | Systemd unit directory used by installer and uninstaller.                    |
| `AGENT_CLEAR_ALLOW_TARGETS` | `false`                 | Explicitly remove a previously installed local destination allowlist.        |

## Validation Rules

- `MANAGEMENT_TLS_MODE` must be `auto`, `provided`, or `off`.
- `MANAGEMENT_TLS_CERT_FILE` and `MANAGEMENT_TLS_KEY_FILE` must be set together.
- `MANAGEMENT_TLS_MODE=provided` requires both cert and key files.
- `MANAGEMENT_TLS_MODE=off` requires `MANAGEMENT_ALLOW_INSECURE_HTTP=true`.
- `MANAGEMENT_PUBLIC_URL` must be absolute and must use `https`, unless management TLS is off and insecure HTTP is explicitly allowed.
- `MANAGEMENT_BIND_ADDRESS` defaults to all interfaces so agents and remote clients can connect. Set it to `127.0.0.1` only for local-only management or when a local reverse proxy fronts management.
- `TUNNEL_MAX_STREAM_WINDOW_BYTES` must be at least `262144` and at most `67108864`.
- `TUNNEL_MAX_CONCURRENT_REQUESTS` must be between `1` and `2048`. Its product with `TUNNEL_MAX_STREAM_WINDOW_BYTES` cannot exceed `536870912` bytes. The server holds a slot for every live Yamux stream, including pooled idle HTTP connections, and the agent holds a slot for every handled stream, so the product bounds attacker-controlled aggregate receive windows at both ends.
- Bootstrap agent ID, name, and token must all be set together.
- Agent boolean parsing accepts `1`, `true`, `yes`, `y`, and `on`.
- Linux agent installs require `AGENT_TLS_CERT_FILE` and `AGENT_TLS_KEY_FILE` together, require user-supplied TLS files to be readable, and reject CA/client-certificate settings with HTTP management URLs.
- Agent target allowlist entries are exact hostnames, IP literals, or CIDR prefixes with optional ports or port ranges. When unset, the agent trusts the management server to choose any tunnel destination.
- Reinstall preserves the effective last single-line `AGENT_ALLOW_TARGETS` assignment when no replacement is supplied. It fails closed if the existing environment file cannot be read or contains unsupported or multiline assignment syntax. Set `AGENT_CLEAR_ALLOW_TARGETS=true` to remove it explicitly; it cannot be combined with `AGENT_ALLOW_TARGETS`.

## Runtime Effects

`CONFIG_DIR` is created or tightened to `0700`. The managed certificate directory is `${CONFIG_DIR}/certs`; an enabled managed GeoLite2 Country database is stored under `${CONFIG_DIR}/geoip`, whose directory and database file are tightened to `0700` and `0600`. SQLite directories created by p2pstream use `0700`, and database/WAL/SHM files are set to `0600`. When `DATABASE_URL` points into an existing directory, p2pstream preserves that directory's mode; secure that directory and its backups separately. If `DATABASE_URL` is unset, p2pstream also migrates a legacy local `p2pstream.db` into `${CONFIG_DIR}/p2pstream.db` when needed.

Management session cookies are Secure when management TLS is enabled, `ENV=production`, or `MANAGEMENT_COOKIE_SECURE=true`.

When either the public-request cap or live-stream cap is reached, additional public agent-route requests receive `503 Service Unavailable` and the agent is not marked passively unhealthy. Environment and health-check dials share the server live-stream cap. Lower the stream window or concurrency limit for tighter memory bounds; raise either only while keeping their documented aggregate budget.

## Examples

Compose `.env`:

```dotenv
MANAGEMENT_PUBLIC_URL=https://proxy.example.com:8081
MANAGEMENT_BIND_ADDRESS=0.0.0.0
MANAGEMENT_TLS_EXTRA_HOSTS=proxy.example.com,192.0.2.10
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
```

## Related Tasks

- [Docker Compose details](../getting-started/docker-compose)
- [Systemd](../operations/systemd)
- [Management TLS reference](./management-tls)

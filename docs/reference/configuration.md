# Configuration Reference

p2pstream reads `.env` if present, then environment variables.

## Server variables

| Variable | Default | Description |
| --- | --- | --- |
| `PORT` | `80` | Legacy/default public HTTP port value. Public listeners are primarily configured in the management UI. |
| `MANAGEMENT_PORT` | `8081` | Management UI/API port. |
| `CONFIG_DIR` | `p2pstream-data` | Directory for default SQLite database and certificates. Docker sets this to `/data`. |
| `DATABASE_URL` | `file:${CONFIG_DIR}/p2pstream.db?...` | SQLite DSN. Leave unset for the managed default. |
| `ENV` | `development` | Use `production` to mark session cookies secure. |
| `MANAGEMENT_UI_DIST_DIR` | `web/management/dist` | Built management UI files. Runtime image sets `/app/web/management/dist`. |
| `MANAGEMENT_UI_DEV_PROXY` | empty | Development-only management UI proxy target. |
| `MANAGEMENT_COOKIE_SECURE` | `false` | Force Secure cookies even when `ENV` is not `production`. |
| `MANAGEMENT_TLS_MODE` | `auto` | `auto`, `provided`, or `off`. |
| `MANAGEMENT_TLS_CERT_FILE` | empty | Management server certificate for `provided` mode. |
| `MANAGEMENT_TLS_KEY_FILE` | empty | Management server private key for `provided` mode. |
| `MANAGEMENT_TLS_CLIENT_CA_FILE` | empty | Optional CA used to verify agent client certificates. |
| `MANAGEMENT_ALLOW_INSECURE_HTTP` | `false` | Required when `MANAGEMENT_TLS_MODE=off`. |
| `MANAGEMENT_PUBLIC_URL` | derived | Public management URL used in generated agent setup snippets. |
| `MANAGEMENT_ADVERTISE_HOST` | detected | Hostname/IP used for auto-generated management certificates and default URL. |
| `MANAGEMENT_TLS_EXTRA_HOSTS` | empty | Comma-separated extra DNS/IP names for auto management TLS. |
| `BOOTSTRAP_AGENT_ID` | empty | Bootstrap agent public ID. Must be set with name and token. |
| `BOOTSTRAP_AGENT_NAME` | empty | Bootstrap agent display name. |
| `BOOTSTRAP_AGENT_TOKEN` | empty | Bootstrap agent token. Stored as a hash. |
| `OBSERVABILITY_RETENTION_DAYS` | `30` | Retention window for recorded request and agent observability data. |

`TARGET_ORIGIN` exists in the configuration struct for older/default behavior, but production routing is managed through public backends in the management UI.

## Agent variables

| Variable | Description |
| --- | --- |
| `MANAGEMENT_URL` | Management server URL, for example `https://proxy.example.com:8081`. |
| `AGENT_ID` | Generated agent public ID from the management UI. |
| `AGENT_TOKEN` | One-time generated or rotated token from the management UI. |
| `AGENT_NAME` | Optional agent display name for local command use. |
| `MANAGEMENT_CA_FILE` | PEM CA bundle used to verify management HTTPS. |
| `MANAGEMENT_CA_PEM_BASE64` | Base64 PEM CA bundle used to verify management HTTPS. |
| `AGENT_TLS_CERT_FILE` | Optional client certificate for management mTLS. |
| `AGENT_TLS_KEY_FILE` | Optional client private key for management mTLS. |
| `AGENT_ALLOW_INSECURE_MANAGEMENT` | Allows HTTP management URL when set to a truthy value. |

## Installer variables

| Variable | Default | Description |
| --- | --- | --- |
| `P2PSTREAM_REPOSITORY` | `Kirari04/p2pstream` | GitHub owner/repo used by the installer. |
| `P2PSTREAM_VERSION` | `latest` | Release tag such as `v0.1.0`, or `latest`. |
| `P2PSTREAM_CONFIG_DIR` | `/etc/p2pstream` | Agent config directory created by installer. |
| `P2PSTREAM_INSTALL_PATH` | `/usr/local/bin/p2pstream` | Binary install path. |

## Truthy agent booleans

Agent boolean env parsing accepts:

```text
1, true, yes, y, on
```

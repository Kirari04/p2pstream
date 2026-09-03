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
| `MANAGEMENT_SETUP_TOKEN`         | generated                    | Optional first-admin setup token. Configured values require at least 32 random characters; if unset, a one-time token is generated and logged. |
| `MANAGEMENT_TRUSTED_PROXY_CIDRS` | empty                        | Comma/space-separated CIDRs allowed to supply the management client-IP header. Keep empty for direct management access. |
| `MANAGEMENT_CLIENT_IP_HEADER`    | `X-Forwarded-For`            | Client-IP header accepted only from a peer in `MANAGEMENT_TRUSTED_PROXY_CIDRS`.                |
| `MANAGEMENT_CLIENT_IP_MODE`      | `trusted_chain`              | `trusted_chain` for an append-only trusted chain, or `single_ip` when the edge overwrites one IP. |
| `MANAGEMENT_ADVERTISE_HOST`      | detected                     | Hostname/IP used for auto-generated management certificates and default URL.                 |
| `MANAGEMENT_TLS_EXTRA_HOSTS`     | empty                        | Comma-separated extra DNS/IP names for auto management TLS.                                  |
| `PUBLIC_CACHE_DIR`               | `${CONFIG_DIR}/cache/public` | Disk directory for public cache body files.                                                  |
| `PUBLIC_MAX_HEADER_BYTES`        | `65536`                      | Maximum public request-header bytes; range `16384`–`1048576`.                                |
| `PUBLIC_MAX_REQUEST_BODY_BYTES`  | `1073741824`                 | Maximum public request body bytes; range `1`–`1099511627776` (1 TiB).                        |
| `PUBLIC_REQUEST_BODY_IDLE_TIMEOUT_MILLIS` | `30000`            | Sliding public request-body idle timeout; range `5000`–`600000`. Active uploads may run longer. |
| `PUBLIC_MAX_CONCURRENT_REQUESTS` | `2048`                       | Server-wide in-flight public request ceiling; range `1`–`100000`.                            |
| `PUBLIC_MAX_CONCURRENT_REQUESTS_PER_TARGET` | `0`               | Per-target in-flight proxy request ceiling. `0` uses the global request ceiling; explicit range `1`–the global ceiling. |
| `PUBLIC_MAX_CONCURRENT_REQUESTS_PER_CLIENT` | `512`              | Per resolved-client in-flight request guard across HTTP/1 and HTTP/2. The automatic default narrows to a lower global request limit, and live resource pressure can lower it further to retain capacity for another client. `0` disables it; trusted proxy identity is honored only through the configured resolver. |
| `PUBLIC_MAX_CONCURRENT_CONNECTIONS` | `0`                       | Optional hard guard for accepted public TCP connections. `0` leaves connection count resource-governed; range `1`–`1000000`. |
| `PUBLIC_MAX_CONNECTIONS_PER_PEER` | `256`                        | Direct-peer pre-header/keep-alive connection guard. The automatic default narrows to a lower explicit global connection limit. Behind a trusted L4 proxy where many clients share one peer address, set both this and `PUBLIC_MAX_CONCURRENT_REQUESTS_PER_CLIENT` to `0` unless distinct trusted HTTP client identities are resolved. |
| `PUBLIC_MAX_CONNECTIONS_PER_TARGET` | `256`                    | Per-origin direct-upstream connection ceiling; range `1`–`65535`.                            |
| `BOOTSTRAP_AGENT_ID`             | empty                        | Bootstrap agent public ID. Must be set with name and token.                                  |
| `BOOTSTRAP_AGENT_NAME`           | empty                        | Bootstrap agent display name.                                                                |
| `BOOTSTRAP_AGENT_TOKEN`          | empty                        | Bootstrap agent token. Configured values must be at least 32 characters; generate them with a CSPRNG. Stored as a hash. |
| `AGENT_UPDATES_ENABLED`          | `false`                      | Enables the trusted stable-release catalog and managed update campaign API. Campaign creation remains fail-closed unless the configured root authenticates the catalog. |
| `AGENT_UPDATE_REPOSITORY`        | `Kirari04/p2pstream`         | Fixed GitHub `owner/repo` used to resolve exact immutable release assets. URLs and arbitrary hosts are rejected. |
| `AGENT_UPDATE_ROOT_FILE`         | empty                        | Canonical out-of-band Ed25519 threshold root metadata. Required when managed updates are enabled. The root published beside a release is audit-only and is never trusted automatically. |
| `AGENT_UPDATE_AUTHORITY_KEY_FILE` | `${CONFIG_DIR}/agent-update-management-authority.json` | Owner-only Ed25519 management command-authority key. It is generated only for pristine managed-update state, while its public identity is pinned in SQLite. Missing or mismatched existing-state keys disable managed updates instead of being replaced. Back it up with the database. |
| `AGENT_UPDATE_CATALOG_REFRESH_MILLIS` | `300000`              | Signed stable-catalog refresh interval; range `10000`–`3600000`. A last-known-good target is retained through transient network errors only until its signed expiry. |
| `AGENT_UPDATE_HTTP_TIMEOUT_MILLIS` | `15000`                   | End-to-end timeout for bounded catalog HTTP requests; range `1000`–`60000`. |
| `OBSERVABILITY_RETENTION_DAYS`   | `30`                         | Retention window for recorded observability data.                                            |
| `OBSERVABILITY_MAX_ROWS`         | `1000000`                    | Maximum retained proxy request events and agent stat rows. Set `0` to disable this cap.       |
| `LOGIN_THROTTLE_MAX_KEYS`        | `50000`                      | Total in-memory login throttle budget split between username and client-address trackers; active blocks are retained until expiry. |
| `TUNNEL_MAX_STREAM_WINDOW_BYTES` | `2097152`                    | Maximum Yamux receive window per tunnel stream in fixed mode. Adaptive mode caps the effective window to its lifetime stream charge. |
| `TUNNEL_MAX_CONCURRENT_REQUESTS` | `64`                         | Legacy/shared agent setting. It no longer limits aggregate server traffic. |
| `SERVER_TUNNEL_MAX_CONCURRENT_STREAMS` | `0`                  | `0` enables adaptive admission up to a `65536` implementation guard. An explicit value sets the server-wide upper bound; local memory/file-descriptor safety may still lower live admission. Range `1`–`65536`. |
| `SERVER_TUNNEL_MEMORY_SOFT_PERCENT` | `80`                       | Adaptive admission begins reducing new-stream growth at this memory utilization. |
| `SERVER_TUNNEL_MEMORY_HARD_PERCENT` | `90`                       | New streams are rejected at this utilization so the kernel does not reach OOM pressure. Maximum `99`. |
| `SERVER_TUNNEL_MEMORY_RECOVERY_PERCENT` | `75`                   | Pressure remains hysteretic until utilization falls below this value. |
| `SERVER_TUNNEL_MEMORY_SAMPLE_MILLIS` | `100`                     | Local cgroup/Go/host memory and process file-descriptor sampling interval; range `10`–`10000`. |
| `SERVER_TUNNEL_ESTIMATED_STREAM_BYTES` | `1310720`               | Conservative lifetime memory charge per live adaptive stream. It covers the adaptive Yamux receive window plus `768 KiB` of bounded socket/relay overhead; range `1048576`–`67108864`. |
| `SERVER_TUNNEL_MEMORY_PERCENT` | `50`                           | Deprecated compatibility variable; no longer used by adaptive admission. |
| `SERVER_TUNNEL_MEMORY_RESERVE_BYTES` | `536870912`             | Deprecated compatibility variable; no longer used by adaptive admission. |

If every login throttle slot is occupied by an active block, new failed-login keys are not tracked until a blocked key expires or a login succeeds for an existing key.

### Agent Variables

Set these on each agent host via `/etc/p2pstream/agent.env` or the generated installer environment. The agent permits loopback targets by default, which supports the common same-host service case without granting access to the rest of the agent's network. The setup dialog can write a narrower or broader allowlist when needed.

When tunnel window or concurrency values are supplied to the installer, they are written to `agent.env` and the effective last numeric assignments are preserved by later reinstalls that do not provide replacements. Preservation is preflighted before installer mutations and fails closed if the existing environment file is unreadable or uses unsupported or multiline syntax. Supplying both numeric values explicitly avoids reading them from the old file, but the installer may still read that file to preserve `AGENT_ALLOW_TARGETS` or `AGENT_ALLOW_ANY_TARGET` unless a destination policy is supplied or explicitly cleared.

| Variable                          | Description                                                          |
| --------------------------------- | -------------------------------------------------------------------- |
| `MANAGEMENT_URL`                  | Management server URL, for example `https://proxy.example.com:8081`. |
| `AGENT_ID`                        | Generated agent public ID from management.                           |
| `AGENT_TOKEN`                     | One-time generated or rotated token from management.                 |
| `AGENT_NAME`                      | Optional local display name.                                         |
| `MANAGEMENT_CA_FILE`              | PEM CA bundle used to verify management HTTPS.                       |
| `MANAGEMENT_CA_PEM_BASE64`        | Base64 PEM CA bundle used to verify management HTTPS.                |
| `MANAGEMENT_TRUST_FILE`            | Writable durable CA bundle used for acknowledged certificate rotation. The systemd installer sets this automatically. |
| `AGENT_TLS_CERT_FILE`             | Optional client certificate for management mTLS.                     |
| `AGENT_TLS_KEY_FILE`              | Optional client private key for management mTLS.                     |
| `AGENT_ALLOW_INSECURE_MANAGEMENT` | Allows HTTP management URL when truthy.                              |
| `TUNNEL_MAX_STREAM_WINDOW_BYTES`  | Maximum Yamux receive window per tunnel stream. Defaults to `2097152`. |
| `TUNNEL_MAX_CONCURRENT_REQUESTS`  | Optional fixed agent limit. When unset, the agent uses adaptive local memory and file-descriptor admission. It advertises an old-peer-safe `2048`, then raises the new/new negotiated implementation guard to `65536`; neither value reserves memory or states the current allowance. |
| `AGENT_ALLOW_TARGETS`             | Tunnel destination allowlist entries separated by commas or whitespace. When unset, only IPv4/IPv6 loopback destinations are allowed. |
| `AGENT_ALLOW_ANY_TARGET`          | Explicitly permit any destination reachable by the agent. Defaults to `false` and cannot be combined with `AGENT_ALLOW_TARGETS`. |

### Installer Variables

Set these as environment variables before running the Linux agent installer script. The installer accepts only local executable inputs; the repository is pinned for later signed managed-update downloads.

| Variable                 | Default                    | Description                                                                  |
| ------------------------ | -------------------------- | ---------------------------------------------------------------------------- |
| `P2PSTREAM_REPOSITORY`   | `Kirari04/p2pstream`       | GitHub owner/repo pinned for later managed-update metadata and artifacts. The installer itself performs no download. |
| `P2PSTREAM_VERSION`      | required                   | Exact stable release identity (`vX.Y.Z`) for a local Linux install. Mutable `latest` and `staging` values are rejected. Not required by the trust-repair-only path. |
| `P2PSTREAM_AGENT_BINARY_FILE` | required for install | Absolute path to the independently obtained raw Linux agent binary. The installer never downloads or extracts executable content. |
| `P2PSTREAM_AGENT_UPDATE_AUTHORITY_PUBLIC_KEY_BASE64` | required for managed updates | Canonical base64 of the separately pinned 32-byte Ed25519 management-authority public key. |
| `P2PSTREAM_AGENT_UPDATE_AUTHORITY_KEY_ID` | required for managed updates | Lowercase SHA-256 of the decoded management-authority public key. |
| `P2PSTREAM_AGENT_UPDATE_AUTHORITY_EPOCH` | required for managed updates | Positive authority epoch pinned during first enrollment; bootstrap cannot replace it. |
| `P2PSTREAM_CONFIG_DIR`   | `/etc/p2pstream`           | Agent config directory created by installer.                                 |
| `P2PSTREAM_INSTALL_PATH` | `/usr/local/bin/p2pstream` | Binary install path.                                                         |
| `P2PSTREAM_SYSTEMD_DIR`  | `/etc/systemd/system`      | Systemd unit directory used by installer and uninstaller.                    |
| `P2PSTREAM_AGENT_STATE_DIR` | `/var/lib/p2pstream-agent` | Writable durable agent state, including the rotated management CA bundle. |
| `P2PSTREAM_ENABLE_MANAGED_UPDATES` | `false`                | With `true`, performs the one-time Linux/systemd updater bootstrap. Existing tunnel credentials are preserved; no agent-token rotation is needed. |
| `P2PSTREAM_UPDATER_ENROLLMENT_TOKEN` | empty                | Short-lived, single-use updater enrollment token bound to one agent and the exact initial root/repository identity. It is never written to `agent.env`. |
| `P2PSTREAM_AGENT_UPDATE_ROOT_BASE64` | empty                 | Canonical root metadata supplied by the authenticated management setup handoff. It is parsed before installation and persisted root-owned; ordinary enrollment never overwrites an existing root. |
| `P2PSTREAM_EXISTING_TUNNEL_VERSION` | required when enrolling an existing unmanaged agent | Server-observed stable `vX.Y.Z` build identity for the tunnel binary being preserved during updater bootstrap. |
| `P2PSTREAM_EXISTING_TUNNEL_COMMIT` | required with existing tunnel version | Server-observed 40-character commit for the preserved tunnel binary. Both fields are pinned into bootstrap slot metadata so a rollback can prove the actual restored build. |
| `P2PSTREAM_REPAIR_TRUST` | `false` | With `true`, repair only the durable CA bundle from `MANAGEMENT_CA_PEM_BASE64` and restart an existing compatible service. |
| `AGENT_CLEAR_ALLOW_TARGETS` | `false`                 | Remove a preserved destination policy during reinstall, reverting to loopback-only defaults. |

## Validation Rules

- `MANAGEMENT_TLS_MODE` must be `auto`, `provided`, or `off`.
- `MANAGEMENT_TLS_CERT_FILE` and `MANAGEMENT_TLS_KEY_FILE` must be set together.
- `MANAGEMENT_TLS_MODE=provided` requires both cert and key files.
- `MANAGEMENT_TLS_MODE=off` requires `MANAGEMENT_ALLOW_INSECURE_HTTP=true`.
- `MANAGEMENT_PUBLIC_URL` must be absolute and must use `https`, unless management TLS is off and insecure HTTP is explicitly allowed.
- `MANAGEMENT_BIND_ADDRESS` defaults to all interfaces so agents and remote clients can connect. Set it to `127.0.0.1` only for local-only management or when a local reverse proxy fronts management.
- Configured `MANAGEMENT_SETUP_TOKEN` and `BOOTSTRAP_AGENT_TOKEN` values must contain at least 32 characters of cryptographically random data.
- `MANAGEMENT_TRUSTED_PROXY_CIDRS` accepts explicit CIDRs but rejects `/0` catch-all ranges. Headers from other peers are ignored. A trusted peer with a missing or malformed identity header cannot attempt login.
- Public header, body, idle-timeout, concurrency, and connection limits must stay within their documented bounds; the per-target request ceiling cannot exceed the global ceiling.
- Proxy target URLs are origins only: `http://` or `https://` plus host and optional port. Configure upstream authentication separately; paths, queries, fragments, and URL credentials are rejected.
- `TUNNEL_MAX_STREAM_WINDOW_BYTES` must be at least `262144` and at most `67108864`.
- `TUNNEL_MAX_CONCURRENT_REQUESTS` must be between `1` and `2048` when explicitly set on an agent. When it is absent, a new agent requests adaptive mode with the old-safe `2048` header. A new server acknowledges that capability and both sides use `65536` only as an implementation guard; an older peer safely retains its legacy negotiated limit. Older agents that omit capacity remain fixed at `64`.
- Adaptive mode samples every finite memory constraint from the process's leaf cgroup through its ancestors, plus a finite Go memory limit or host `MemAvailable` fallback, and process file-descriptor use versus `RLIMIT_NOFILE`. Every live stream retains a memory reservation for its full bounded receive window and two file descriptors for dual-stack dial bursts. Each sample admits only work whose lifetime reservations fit below the smallest absolute `90%` resource watermark. `80%` changes the state to soft pressure, `90%` rejects new work with retry guidance while existing streams drain, and recovery below `75%` prevents oscillation.
- A transient resource-sensor error retains the last restrictive sample for one second. Startup or prolonged sensor loss is reported as `unknown`/degraded and fails closed for new streams while existing work drains; it never opens the `65536` guard.
- Yamux receive credit is lazy, but it is still a reachable memory exposure. Adaptive mode reserves `SERVER_TUNNEL_ESTIMATED_STREAM_BYTES` for every live or closing stream until peer FIN/forced Yamux cleanup, subtracts `768 KiB` for bounded socket/relay overhead, and caps the effective receive window to the remainder (default `512 KiB`). After a pressured agent generation fully drains, the agent asynchronously returns unused Go heap pages to the OS before admitting the next full burst. The configured `2 MiB` default remains available to explicit fixed-capacity sessions.
- Queues remain globally and per-route bounded, opening concurrency remains below Yamux's finite `256` SYN backlog, and a small trusted-health opening share is protected. Cross-session capacity is work-conserving: one session may borrow idle capacity but yields released capacity to waiting sessions.
- Bootstrap agent ID, name, and token must all be set together.
- Managed updates require a pinned `owner/repo`, an unexpired threshold root, and management HTTPS. The catalog accepts only canonical signed stable manifests and monotonic security/version floors; mutable staging releases, arbitrary URLs, and unknown artifacts are never eligible campaign targets.
- Agent boolean parsing accepts `1`, `true`, `yes`, `y`, and `on`.
- Linux agent installs require `AGENT_TLS_CERT_FILE` and `AGENT_TLS_KEY_FILE` together, require user-supplied TLS files to be readable, and reject CA/client-certificate settings with HTTP management URLs.
- Agent target allowlist entries are exact hostnames, IP literals, or CIDR prefixes with optional ports or port ranges. When neither an allowlist nor `AGENT_ALLOW_ANY_TARGET=true` is set, the agent permits only `127.0.0.0/8` and `::1/128`. This keeps same-host services working while preventing default access to the surrounding network.
- Reinstall preserves the effective last single-line `AGENT_ALLOW_TARGETS` or `AGENT_ALLOW_ANY_TARGET` assignment when no replacement is supplied. It fails closed if the existing environment file cannot be read or contains unsupported or multiline assignment syntax. Set `AGENT_CLEAR_ALLOW_TARGETS=true` to discard the preserved policy and revert to loopback-only; it cannot be combined with an explicit replacement.

## Runtime Effects

`CONFIG_DIR` is created or tightened to `0700`. The managed certificate directory is `${CONFIG_DIR}/certs`; an enabled managed GeoLite2 Country database is stored under `${CONFIG_DIR}/geoip`, whose directory and database file are tightened to `0700` and `0600`. SQLite directories created by p2pstream use `0700`, and database/WAL/SHM files are set to `0600`. When `DATABASE_URL` points into an existing directory, p2pstream preserves that directory's mode; secure that directory and its backups separately. If `DATABASE_URL` is unset, p2pstream also migrates a legacy local `p2pstream.db` into `${CONFIG_DIR}/p2pstream.db` when needed.

On first configuration load after upgrade, legacy proxy target URLs containing ignored credentials, paths, queries, or fragments are normalized in SQLite to the origin that earlier releases actually used. A warning identifies the affected target without logging the removed value.

Management session cookies are Secure when management TLS is enabled, `ENV=production`, or `MANAGEMENT_COOKIE_SECURE=true`.

When the global public-request cap, per-target request cap, server-wide physical stream cap, or negotiated per-session agent stream cap is reached, additional requests receive `503 Service Unavailable` with `Retry-After: 1`; local capacity rejections do not mark a target or agent passively unhealthy. Pooled route-target transports retain reusable connections for 30 seconds, but the pooled lifetime budget is the authoritative global bound across all shards. This lets a hot target reuse its parallel connections without allowing idle streams to consume the reserved one-shot or health capacity. Under genuine total/public pressure, each blocked demand may retire one known-idle shard, preferring the selected agent, and waits for actual peer FIN before reusing its permit; a session-local limit may reclaim only that same session. Health checks use a protected stream budget and treat local admission timeouts as skipped checks rather than backend failures. Declared or streamed bodies above the configured body ceiling receive `413`, and stalled uploads receive `408`. Long-lived responses and continuously progressing uploads are not subject to a total-duration timeout. Environment dials use bounded one-shot public capacity; health-check dials use the protected control budget. Lower the stream window or concurrency limit for tighter memory bounds; raise either only while keeping their documented aggregate budget.

Accepted public sockets are resource-accounted before TLS and HTTP header parsing in both automatic and explicitly capped server modes. A direct peer may consume at most 90% of the current socket resource envelope, while the resolved-client request guard covers logical HTTP/1 and HTTP/2 concurrency; set the peer guard to `0` only when a trusted L4 proxy represents many downstream clients. Diagnostics separates live socket count, configured peer/hard-limit rejections, resource rejections, client-request rejections, retry/socket reservation bytes and descriptors, the raw sampled allowance, and the effective public/tunnel allowances. This distinction makes conservative pre-stream reservations visible instead of presenting them as unexplained `agent_server_capacity` failures.

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

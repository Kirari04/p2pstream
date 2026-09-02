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
| `--management-trust-file` | `MANAGEMENT_TRUST_FILE` | Writable durable CA bundle used for acknowledged management certificate rotation. |
| `--tls-cert-file` | `AGENT_TLS_CERT_FILE` | Client certificate for management mTLS. |
| `--tls-key-file` | `AGENT_TLS_KEY_FILE` | Client private key for management mTLS. |
| `--allow-insecure-management` | `AGENT_ALLOW_INSECURE_MANAGEMENT` | Permit HTTP management URL. |
| `--tunnel-max-stream-window-bytes` | `TUNNEL_MAX_STREAM_WINDOW_BYTES` | Maximum Yamux receive window per tunnel stream; adaptive mode may lower it to the lifetime stream charge. |
| `--tunnel-max-concurrent-requests` | `TUNNEL_MAX_CONCURRENT_REQUESTS` | Optional fixed request limit. When omitted, local nested-cgroup/host/Go memory and file-descriptor pressure control admission dynamically. |
| `--allow-target` | `AGENT_ALLOW_TARGETS` | Opt-in destination allowlist entry. Repeat the flag, or separate env entries with commas/whitespace. |
| `--allow-any-target` | `AGENT_ALLOW_ANY_TARGET` | Explicitly permit any destination reachable by the agent. |

## Validation Rules

- `users reset-password` requires a valid username and a password with at least 12 characters.
- Use only one password source: prompt, `--password-env`, or `--password-file`.
- `agent` requires `AGENT_ID` and `AGENT_TOKEN`.
- Agent HTTP management URLs are rejected unless `--allow-insecure-management` or `AGENT_ALLOW_INSECURE_MANAGEMENT` is set.
- `--tunnel-max-stream-window-bytes` must be at least `262144` and at most `67108864`.
- `--tunnel-max-concurrent-requests` must be between `1` and `2048`.
- When neither an allowlist nor `--allow-any-target`/`AGENT_ALLOW_ANY_TARGET=true` is set, the agent permits only IPv4 and IPv6 loopback destinations.
- `--allow-any-target` cannot be combined with `--allow-target` entries.
- Allow target entries are exact hostnames, IP literals, or CIDR prefixes with optional ports or port ranges, such as `myapp.internal:443`, `10.0.5.0/24:8080`, or `metrics.internal:9000-9010`. IPv6 entries with ports must use brackets, for example `[2001:db8::/64]:8443`.

## Runtime Effects

`p2pstream server` reads `.env` and environment variables, starts management on `MANAGEMENT_PORT`, starts public listeners from SQLite configuration, and starts ACME scheduling when available.

`users reset-password` updates the configured SQLite database directly and revokes active sessions for that user. Run it where the same `CONFIG_DIR` or `DATABASE_URL` is available.

If no management URL is provided to the agent, it guesses `https://<local-route-ip>:8081`; production agents should use the explicit URL from the **Agent Setup** modal opened from **Agents**.

## Examples

Server:

```bash
CONFIG_DIR=/var/lib/p2pstream \
MANAGEMENT_PUBLIC_URL=https://proxy.example.com:8081 \
p2pstream server
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
  --management-trust-file ./p2pstream-agent-state/management-ca.pem \
  --agent-id agent-abc123 \
  --agent-token "$AGENT_TOKEN" \
  --allow-target myapp.internal:443 \
  --allow-target 10.0.5.0/24:8080
```

## Related Tasks

- [First login](../getting-started/first-login)
- [Release binary](../getting-started/binary)
- [Expose a home lab app](../guides/expose-a-home-lab-app)

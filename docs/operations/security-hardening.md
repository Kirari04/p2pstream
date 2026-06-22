# Security Hardening

Restrict management access, protect persistent state, and scope public controls so the deployment is safer to operate.

## Use This When

Use this before exposing management beyond a private network, after adding agents, before publishing production hostnames, and during periodic self-hosting reviews.

## Prerequisites

- p2pstream is running with persistent `CONFIG_DIR`, `/data` in Compose.
- You know whether management must be reachable by remote agents, remote admins, or both.
- You have a backup path for the persistent data directory.

## Steps

1. Harden management access:

   - Keep management HTTPS enabled.
   - Keep `MANAGEMENT_BIND_ADDRESS=0.0.0.0` when agents or admins connect from other hosts.
   - Set `MANAGEMENT_BIND_ADDRESS=127.0.0.1` only when a local reverse proxy, VPN sidecar, or SSH tunnel fronts management.
   - Prefer firewall allowlists, VPN, or a private admin network for `8081`.
   - Set `MANAGEMENT_PUBLIC_URL` to the real management URL used by browsers and agents.
   - Set a deployment secret as `MANAGEMENT_SETUP_TOKEN` before first setup, or capture the generated startup token from trusted logs.
   - Use `ENV=production` or `MANAGEMENT_COOKIE_SECURE=true` when management is accessed over HTTPS.
   - For API-only management, set `MANAGEMENT_UI_DISABLED=true`; the ConnectRPC API and agent Yamux tunnel stay available.

2. Protect `/data`:

   - Back up the full `CONFIG_DIR`.
   - Restrict host, volume, and backup access to trusted administrators.
   - Treat database write access as administrative access, because the local CLI can reset management credentials.
   - Enable `SECRETS_ENCRYPTION_KEY` or `SECRETS_ENCRYPTION_KEY_FILE` to encrypt stored upstream/API credentials in SQLite.
   - Run `p2pstream secrets status` and `p2pstream secrets rewrap --dry-run` against the same `CONFIG_DIR` before enabling required mode or removing previous keys.
   - After the first successful encrypted startup, set `SECRETS_ENCRYPTION_REQUIRED=true` so startup fails instead of accepting plaintext stored secrets.
   - Store the secrets-encryption key outside `/data` in your deployment secret manager. Losing it makes encrypted database secrets unrecoverable.
   - Prefer `SECRETS_ENCRYPTION_KEY_FILE` when your deployment secret manager can mount a key file; otherwise protect process environments, systemd environment files, Docker inspect output, crash dumps, and child processes that can expose `SECRETS_ENCRYPTION_KEY`.
   - Treat live process memory as sensitive. p2pstream decrypts stored secrets in memory when runtime components need to use them.
   - Keep filesystem and backup access restricted even with database secret encryption enabled; TLS private-key files under `/data/certs` are protected by filesystem permissions, not by stored-secret encryption.
   - Protect database backups as secrets; encrypted backups still contain operational state, sessions, certificates, and metadata.

3. Harden agents:

   - Store generated agent tokens as secrets.
   - Rotate tokens if a host or setup snippet leaks.
   - Disable or delete unused agents.
   - Use agent mTLS with `MANAGEMENT_TLS_CLIENT_CA_FILE` when token-only auth is not enough.
   - Keep `AGENT_ALLOW_INSECURE_MANAGEMENT` unset except for isolated development.

4. Harden public TLS and upstreams:

   - Use ACME or trusted manual certificates for public hostnames.
   - Avoid relying on fallback self-signed public HTTPS certificates.
   - Avoid target `tls_skip_verify` except for controlled internal services while fixing the upstream certificate.
   - Back up `/data/certs/management` so agents can continue trusting the same management CA after restore.

5. Scope WAF, rate-limit, shaper, and cache rules by host/path/method so broad policies do not catch unrelated traffic.

## Verification

Review:

- `/data` is persistent and backed up.
- Management is HTTPS.
- Management exposure is intentional and firewall/VPN rules match that decision.
- First-admin setup token handling is documented for operators.
- Secrets encryption is enabled, `p2pstream secrets status` shows no plaintext or rewrap-needed rows, required mode is active after migration, and key material is backed up outside `/data`.
- `MANAGEMENT_PUBLIC_URL` is correct.
- Unused listeners and agents are disabled or deleted.
- Tracing is disabled after troubleshooting.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| Browser UI returns `404` | `MANAGEMENT_UI_DISABLED=true` intentionally disables only the browser UI. |
| Agents fail after restore | Restore the old management CA or update agent CA material. |
| Server fails with encrypted secret key errors | Restore the matching current key via `SECRETS_ENCRYPTION_KEY` or `SECRETS_ENCRYPTION_KEY_FILE`, or include the old key in `SECRETS_ENCRYPTION_PREVIOUS_KEYS` during rotation. |
| Everyone hits one rate-limit bucket | A front proxy may hide client IPs; place p2pstream at the edge, use `REMOTE_IP` when possible, or use only trusted application headers. Do not key on client-supplied forwarding headers. |
| WAF does not stop network saturation | WAF is HTTP-layer only; use upstream DDoS/network protection. |

## Next Steps

- [Backup and restore](./backup-restore)
- [Management TLS reference](../reference/management-tls)
- [WAF reference](../reference/waf)

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
   - Protect database backups as secrets; the SQLite database includes operational tokens and upstream credentials.

3. Harden agents:

   - Store generated agent tokens as secrets.
   - Rotate tokens if a host or setup snippet leaks.
   - Disable or delete unused agents.
   - Use agent mTLS with `MANAGEMENT_TLS_CLIENT_CA_FILE` when token-only auth is not enough.
   - Keep `AGENT_ALLOW_INSECURE_MANAGEMENT` unset except for isolated development.
   - Set `AGENT_ALLOW_TARGETS` or repeated `--allow-target` flags when an agent should expose only specific destinations. If unset, the agent trusts the management server to request any reachable `host:port`.

4. Harden public TLS and upstreams:

   - Use ACME or trusted manual certificates for public hostnames.
   - Avoid relying on fallback self-signed public HTTPS certificates.
   - Avoid target `tls_skip_verify` except for controlled internal services while fixing the upstream certificate.
   - Back up `/data/certs/management` so agents can continue trusting the same management CA after restore.

5. Scope WAF, rate-limit, shaper, and cache rules by host/path/method so broad policies do not catch unrelated traffic. Manage each policy type from its own tab under **Traffic Policy**.
6. Under **Traffic Policy → WAF → Visitor identity & GeoIP**, leave trusted-proxy providers disabled unless that provider actually connects to the public listener. When enabling one, firewall the origin to the provider's published ranges so clients cannot bypass the CDN.
7. In the same section, prefer a dedicated single-IP header for custom proxies when the last trusted proxy overwrites it. Use chain parsing only when every trusted hop and its CIDRs are understood.
8. For geo allow-only rules, keep unknown-country handling on **Apply rule** unless an intentional fail-open path is required.

## Verification

Review:

- `/data` is persistent and backed up.
- Management is HTTPS.
- Management exposure is intentional and firewall/VPN rules match that decision.
- First-admin setup token handling is documented for operators.
- `MANAGEMENT_PUBLIC_URL` is correct.
- Unused listeners and agents are disabled or deleted.
- Agents that need a constrained blast radius have explicit target allowlists.
- Tracing is disabled after troubleshooting.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| Browser UI returns `404` | `MANAGEMENT_UI_DISABLED=true` intentionally disables only the browser UI. |
| Agents fail after restore | Restore the old management CA or update agent CA material. |
| Everyone hits one rate-limit bucket | A front proxy may hide client IPs; place p2pstream at the edge, use `REMOTE_IP` when possible, or use only trusted application headers. Do not key on client-supplied forwarding headers. |
| WAF does not stop network saturation | WAF is HTTP-layer only; use upstream DDoS/network protection. |
| Client-IP headers are spoofed | p2pstream ignores them unless the network peer matches an explicitly trusted source; do not trust broad or catch-all CIDRs. |
| GeoIP is inaccurate | Country data is approximate. Use it as a traffic policy signal, not identity or proof of physical presence. |

## Next Steps

- [Backup and restore](./backup-restore)
- [Management TLS reference](../reference/management-tls)
- [WAF reference](../reference/waf)

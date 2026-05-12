# Security Hardening

Treat the management surface as administrative infrastructure. Public listeners can be internet-facing; management should be restricted.

## Management access

- Keep management HTTPS enabled.
- Do not expose `8081` publicly unless you need remote agents or remote administration.
- Prefer firewall allowlists, VPN, or a private admin network.
- Set `MANAGEMENT_PUBLIC_URL` to the real management URL used by browsers and agents.
- Use `ENV=production` or `MANAGEMENT_COOKIE_SECURE=true` when management is accessed over HTTPS.

## Data protection

Protect `/data`. It contains:

- SQLite database,
- user sessions,
- agent token hashes,
- public TLS certificate material,
- management CA and key when auto TLS is used.

Back up `/data` and restrict host access to trusted administrators.

## Agent security

- Store generated agent tokens as secrets.
- Rotate agent tokens if a host or snippet leaks.
- Disable or delete unused agents.
- Use agent mTLS with `MANAGEMENT_TLS_CLIENT_CA_FILE` when token-only auth is not enough.
- Keep `AGENT_ALLOW_INSECURE_MANAGEMENT` unset unless using isolated development.

## TLS practices

- Use ACME or trusted manual certificates for public hostnames.
- Avoid relying on fallback self-signed public HTTPS certificates.
- Avoid backend `tls_skip_verify` except for controlled internal services while fixing the upstream certificate.
- Back up `/data/certs/management` so agents can continue trusting the same management CA after restore.

## Least exposure

Open only required ports:

| Surface | Recommended exposure |
| --- | --- |
| Public HTTP/HTTPS | Internet-facing as needed. |
| Management `8081` | Private, VPN, allowlisted, or exposed only for agents. |
| Extra listeners | Publish only when actively used. |

## Review checklist

- [ ] `/data` is persistent and backed up.
- [ ] management is HTTPS.
- [ ] admin password is stored in a password manager.
- [ ] `MANAGEMENT_PUBLIC_URL` is correct.
- [ ] unused listeners are disabled.
- [ ] unused agents are disabled or deleted.
- [ ] tracing is disabled after troubleshooting.

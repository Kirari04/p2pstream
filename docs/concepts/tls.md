# TLS

p2pstream has separate TLS systems for management traffic and public listener traffic.

## What It Is

Management TLS protects the management UI/API and agent connections. Public TLS protects user traffic received by HTTPS listeners.

## When It Matters

TLS matters during first setup, agent installs, public certificate issuance, browser trust warnings, and restores. Losing the management CA changes what auto-TLS agents trust.

## Runtime Behavior

Management HTTPS is enabled by default.

| Mode | Configuration | Behavior |
| --- | --- | --- |
| `auto` | default | Generate and persist a local management CA and server certificate. |
| `provided` | `MANAGEMENT_TLS_CERT_FILE` and `MANAGEMENT_TLS_KEY_FILE` | Use your certificate and key. |
| `off` | requires `MANAGEMENT_ALLOW_INSECURE_HTTP=true` | Serve management over plain HTTP. |

Auto-generated management files are stored under `${CONFIG_DIR}/certs/management`, which is `/data/certs/management` in Compose. Agents verify management TLS with `MANAGEMENT_CA_FILE` or `MANAGEMENT_CA_PEM_BASE64`.

::: danger Avoid insecure management HTTP
Use `MANAGEMENT_TLS_MODE=off` only for isolated development or when another trusted local component terminates TLS and you fully understand the risk.
:::

Public HTTPS listeners select certificates by SNI. Certificate mappings belong to HTTPS listeners and can be:

- uploaded or file-path manual certificates,
- GUI-generated self-signed certificates,
- ACME-managed certificates.

ACME challenges:

| Challenge | Requirement |
| --- | --- |
| HTTP-01 | Public DNS hostname must reach the HTTP listener, usually port `80`. |
| TLS-ALPN-01 | Public DNS hostname must reach the HTTPS listener, usually port `443`. |
| DNS-01 | Enabled Cloudflare DNS credential. Required for wildcard certificates. |

If no configured certificate matches a public hostname, the HTTPS listener can serve a fallback self-signed certificate. Treat that as a public deployment failure.

## Common Mistakes

- Setting `MANAGEMENT_PUBLIC_URL` to a URL agents cannot reach.
- Forgetting `MANAGEMENT_TLS_EXTRA_HOSTS` for auto-generated certificate names.
- Using HTTP-01/TLS-ALPN-01 for wildcard certificates.
- Restoring without `/data/certs/management` and breaking existing agent trust.

## Related Links

- [ACME HTTP/TLS-ALPN](../guides/acme-http-tls-alpn)
- [ACME Cloudflare DNS](../guides/acme-cloudflare-dns)
- [Management TLS reference](../reference/management-tls)
- [Public TLS and ACME reference](../reference/public-tls-acme)

# TLS

p2pstream has separate TLS concerns for management traffic and public listener traffic.

## Management TLS

Management HTTPS is enabled by default.

| Mode | Configuration | Behavior |
| --- | --- | --- |
| `auto` | default | Generate and persist a local management CA and server certificate. |
| `provided` | `MANAGEMENT_TLS_CERT_FILE` and `MANAGEMENT_TLS_KEY_FILE` | Use your certificate and key. |
| `off` | requires `MANAGEMENT_ALLOW_INSECURE_HTTP=true` | Serve management over plain HTTP. |

Auto-generated files are stored under:

```text
/data/certs/management
```

Agents verify the management certificate with `MANAGEMENT_CA_FILE` or `MANAGEMENT_CA_PEM_BASE64`.

::: danger Avoid insecure management HTTP
Use `MANAGEMENT_TLS_MODE=off` only for isolated development or when another trusted local component terminates TLS and you fully understand the risk.
:::

## Public TLS

HTTPS listeners select certificates by SNI. Certificate mappings belong to HTTPS listeners and can be:

- manual uploads,
- manual file paths,
- ACME-managed certificates.

If no configured certificate matches a hostname, the HTTPS listener can serve a fallback self-signed certificate. Treat that as a failure for public clients.

## ACME challenges

| Challenge | Requirement |
| --- | --- |
| HTTP-01 | Public DNS hostname must reach the HTTP listener, usually port `80`. |
| TLS-ALPN-01 | Public DNS hostname must reach the HTTPS listener, usually port `443`. |
| DNS-01 | Cloudflare DNS credential is required. This is required for wildcard certificates. |

Use Let's Encrypt staging while testing automation, then switch to production.

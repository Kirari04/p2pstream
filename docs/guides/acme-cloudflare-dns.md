# Issue a Wildcard Certificate with Cloudflare DNS-01

DNS-01 is required for wildcard ACME certificates and useful when ports `80` or `443` cannot be used for validation.

## 1. Create a Cloudflare token

Create a Cloudflare API token with permission to edit DNS records for the target zone.

Record:

- Cloudflare zone ID,
- API token.

## 2. Add the DNS credential

Open **TLS -> DNS Credentials**.

Create:

| Field | Value |
| --- | --- |
| Name | `cloudflare-example` |
| Provider | Cloudflare |
| Zone ID | your Cloudflare zone ID |
| API token | your scoped token |
| Enabled | On |

The API token is stored and later shown as set, not echoed back in full.

## 3. Add the wildcard certificate

Open **TLS** and create:

| Field | Value |
| --- | --- |
| Listener | `public-https` |
| Hostname pattern | `*.example.com` |
| Method | `DNS-01` |
| CA | Let's Encrypt staging first |
| Email | your ACME account email |
| DNS credential | `cloudflare-example` |
| Enabled | On |

After the staging test works, switch the CA to Let's Encrypt production and renew.

## 4. Route wildcard hosts

Wildcard TLS only provides the certificate. You still need matching routes.

Example:

| Host pattern | Path prefix | Backend |
| --- | --- | --- |
| `app.example.com` | `/` | `app` |
| `media.example.com` | `/` | `media` |
| `*.example.com` | `/` | `fallback` |

Use specific exact-host routes before broad wildcard routes.

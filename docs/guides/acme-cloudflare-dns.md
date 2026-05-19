# Issue a Wildcard Certificate with Cloudflare DNS-01

Issue an ACME certificate through Cloudflare DNS-01 for wildcard hosts or deployments where validation ports cannot be exposed.

## Use This When

Use DNS-01 when you need `*.example.com`, or when HTTP-01 and TLS-ALPN-01 cannot reach p2pstream from the public internet.

## Prerequisites

- The domain is hosted in Cloudflare DNS.
- A Cloudflare API token that can edit DNS records for the target zone.
- The Cloudflare zone ID.

  :::tip Finding your Zone ID
  In the Cloudflare dashboard, select your domain. The Zone ID appears in the right-hand sidebar under **API**.
  :::

- An HTTPS listener such as `public-https`.

## Steps

1. In Cloudflare, create a scoped API token with DNS edit permission for the target zone.
2. Open **TLS -> DNS Credentials** and create:

   | Field | Value |
   | --- | --- |
   | Name | `cloudflare-example` |
   | Provider | Cloudflare |
   | Zone ID | your Cloudflare zone ID |
   | API token | your scoped token |
   | Enabled | On |

   The API token is stored server-side and later shown as set, not echoed back in full.

3. Open **TLS** and create the certificate mapping:

   | Field | Value |
   | --- | --- |
   | Listener | `public-https` |
   | Hostname pattern | `*.example.com` |
   | Method | `DNS-01` |
   | CA | Let's Encrypt staging first |
   | Email | your ACME account email |
   | DNS credential | `cloudflare-example` |
   | Enabled | On |

4. After staging issuance works, switch the CA to Let's Encrypt production and renew.

5. Create matching routes. Wildcard TLS only provides the certificate.

   | Host pattern | Path prefix | Backend |
   | --- | --- | --- |
   | `app.example.com` | `/` | `app` |
   | `media.example.com` | `/` | `media` |
   | `*.example.com` | `/` | `fallback` |

## Verification

The certificate status should become ready. Then test a routed wildcard host:

```bash
curl -I https://app.example.com
```

## Troubleshooting

| Symptom | Check |
| --- | --- |
| DNS credential rejected | Zone ID cannot be empty or contain whitespace/path characters. |
| Certificate issuance fails | Token must edit DNS records for the zone. |
| TLS works but route fails | Add or fix **Proxy -> Routes** for the hostname. |
| Apex host not covered | `*.example.com` does not cover `example.com`; add a separate mapping if needed. |

## Next Steps

- [ACME HTTP/TLS-ALPN](./acme-http-tls-alpn)
- [Public TLS and ACME reference](../reference/public-tls-acme)
- [Routing rules reference](../reference/routing-rules)

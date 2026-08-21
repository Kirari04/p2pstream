# Issue ACME Certificates with HTTP-01 or TLS-ALPN-01

Issue a trusted public certificate when the requested hostname reaches p2pstream on the required inbound validation port.

## Use This When

Use HTTP-01 or TLS-ALPN-01 when public DNS points directly to p2pstream and inbound port `80` or `443` is available.

## Prerequisites

| Challenge | Required public reachability |
| --- | --- |
| HTTP-01 | `http://hostname/.well-known/acme-challenge/...` reaches a p2pstream HTTP listener. |
| TLS-ALPN-01 | `https://hostname:443` reaches a p2pstream HTTPS listener. |

:::danger Public DNS required
The hostname must be a publicly resolvable fully-qualified domain name. `localhost`, bare IP addresses, and internal-only names will cause the ACME challenge to fail — the Let's Encrypt servers cannot reach them to validate ownership. Wildcards (`*.example.com`) also cannot use these methods; use [DNS-01](./acme-cloudflare-dns) instead.
:::

## Steps

1. Open **Proxy -> Listeners**.
2. For HTTP-01, ensure an HTTP listener is enabled and running on container port `80`; use **More -> Enable listener** and **Start** as needed.
3. For TLS-ALPN-01, ensure an HTTPS listener is enabled and running on container port `443`; use **More -> Enable listener** and **Start** as needed.
4. Open **TLS**, select **Add Certificate**, and create a certificate mapping:

   | Field | Value |
   | --- | --- |
   | Listener | `public-https` |
   | Hostname pattern | `app.example.com` |
   | Method | `HTTP-01` or `TLS-ALPN` |
   | CA | Let's Encrypt staging for testing, production when ready |
   | Email | your ACME account email |
   | Enabled | On |

   <figure class="doc-screenshot">
     <img src="../assets/new/tls_httpchallenge_letsencrypt_modal.png" alt="p2pstream Edit TLS Mapping drawer showing HTTP-01, Let's Encrypt staging, a hostname pattern, and HTTPS listener">
     <figcaption>The TLS mapping drawer ties the hostname pattern to an HTTPS listener and selects the HTTP-01 or TLS-ALPN validation method and Let's Encrypt CA environment.</figcaption>
   </figure>

## Verification

The certificate status should move from pending or renewing to ready.

Run:

```bash
curl -I https://app.example.com
```

## Troubleshooting

| Symptom | Check |
| --- | --- |
| Status stays error | Open **TLS** and read the mapping's **Last error** row. |
| HTTP-01 fails | DNS and firewall must send port `80` to the p2pstream HTTP listener. |
| TLS-ALPN-01 fails | DNS and firewall must send port `443` to the p2pstream HTTPS listener. |
| Wildcard rejected | Use DNS-01 with a Cloudflare DNS credential. |

## Next Steps

- [Issue a wildcard certificate with Cloudflare DNS-01](./acme-cloudflare-dns)
- [TLS](../concepts/tls)
- [Public TLS and ACME reference](../reference/public-tls-acme)

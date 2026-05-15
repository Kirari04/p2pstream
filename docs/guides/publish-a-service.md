# Publish a Service with a Direct Backend

Expose an upstream service that is reachable from the p2pstream server as a public HTTPS hostname.

## Use This When

Use a direct backend when the upstream target is reachable from the VPS or host running p2pstream.

Example:

| Role | Value |
| --- | --- |
| Upstream origin | `https://app.internal:8443` |
| Public URL | `https://app.example.com` |

## Prerequisites

- p2pstream is running and you can log in to management.
- Public DNS for `app.example.com` points to the p2pstream host.
- The p2pstream server/container can reach `https://app.internal:8443`.
- Docker publishes `443:443` if you use the default HTTPS listener.

## Steps

1. Open **Proxy** and use **Backends** to create or edit a backend:

   | Field | Value |
   | --- | --- |
   | Name | `app` |
   | Type | Proxy forward |
   | Forward mode | Direct |
   | Target origin | `https://app.internal:8443` |
   | TLS skip verify | Off unless this is a controlled internal certificate exception |
   | Enabled | On |

   If the upstream needs custom headers, use upstream request headers. If it needs HTTP basic auth, use upstream basic auth instead of manually adding `Authorization`.

2. In **Proxy**, use **Listeners** and keep or create an HTTPS listener:

   | Field | Value |
   | --- | --- |
   | Name | `public-https` |
   | Protocol | HTTPS |
   | Bind address | empty |
   | Port | `443` |
   | Default backend | `app` or another explicit fallback |
   | Enabled | On |

3. In **Proxy**, use **Routes** to create a specific route:

   | Field | Value |
   | --- | --- |
   | Listener | `public-https` |
   | Priority | `10` |
   | Host pattern | `app.example.com` |
   | Path prefix | `/` |
   | Action | Forward |
   | Backend | `app` |
   | Enabled | On |

4. Open **TLS** and add a certificate mapping for `app.example.com`.

   | Validation path | Use when |
   | --- | --- |
   | HTTP-01 | Port `80` reaches p2pstream. |
   | TLS-ALPN-01 | Port `443` reaches p2pstream. |
   | DNS-01 | You need wildcard certificates or cannot expose validation ports. |

## Verification

Run:

```bash
curl -I https://app.example.com
```

Then check **Overview** for request counts and status classes. If you need request-stage details, open **Traffic**, enable tracing, repeat the request, and inspect the flow.

<figure class="doc-screenshot">
  <img src="../assets/management_1.png" alt="p2pstream Proxy page showing listener and backend entries">
  <figcaption>Listeners accept public traffic; backends define where matching requests are sent.</figcaption>
</figure>

## Troubleshooting

| Symptom | Check |
| --- | --- |
| `502 Bad Gateway` | Test the target origin from the p2pstream server/container. |
| `503 Service Unavailable` | Confirm the route backend is enabled and available; check health status if health checks are enabled. |
| Fallback/self-signed certificate | Add or fix the **TLS** certificate mapping for the requested hostname. |
| Route does not match | Confirm listener, host pattern, path prefix, and priority. |

For frontend assets such as CSS, JavaScript, images, and fonts, configure public asset caching under **Traffic Policy -> Cache**. See [Public Asset Cache](../concepts/cache).

## Next Steps

- [Expose a home lab app](./expose-a-home-lab-app)
- [Routing](../concepts/routing)
- [Public TLS and ACME reference](../reference/public-tls-acme)

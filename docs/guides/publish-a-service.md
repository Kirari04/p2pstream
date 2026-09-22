# Publish a Service with a Direct Target

Expose an upstream service that is reachable from the p2pstream server as a public HTTPS hostname.

## Use This When

Use a direct proxy target when the upstream origin is reachable from the VPS or host running p2pstream.

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

1. Open **Proxy -> Listeners**. Keep the seeded HTTPS listener, or select **Add Listener** to create one:

   | Field | Value |
   | --- | --- |
   | Name | `public-https` |
   | Protocol | HTTPS |
   | Bind address | empty |
   | Port | `443` |
   | Enabled | On |

2. Open **Proxy -> Sites**, select **Add Site**, and save a draft with hostname `app.example.com` and the HTTPS listener from step 1. The Site stays open so you can complete its routes before publishing. Select **Add first route** in the Site workspace:

   | Field | Value |
   | --- | --- |
   | Priority | `10` |
   | Path prefix | `/` |
   | Action | Forward |
   | Enabled | On |

3. In the nested **Add Route** drawer, add a proxy target to that route. The Site supplies its hostnames and listener assignments:

   | Field | Value |
   | --- | --- |
   | Name | `app` |
   | Type | Proxy |
   | Transport | Direct |
   | URL | `https://app.internal:8443` |
   | Priority group | `0` |
   | Weight | `100` |
   | Skip TLS verify | Off unless this is a controlled internal certificate exception |
   | Enabled | On |

   Expand **Advanced target settings** to configure upstream request headers, upstream basic authentication, and target health checks. These settings stay with the target when its Site is assigned to another listener.

   <figure class="doc-screenshot">
     <img src="../assets/new/proxy_direct_route_modal.png" alt="p2pstream Edit Route drawer showing a direct proxy target with its URL, transport, timeout, weight, and TLS verification control">
     <figcaption>The route drawer keeps the match and its targets together. For a direct target, review the server-owned upstream URL, transport, response-header timeout, weight, and origin TLS verification policy before saving.</figcaption>
   </figure>

4. Save the route to return to the same Site. Use its readiness summary to configure certificate coverage for `app.example.com` on the HTTPS listener. Certificate mappings are also available under **TLS -> Add Certificate**.

   | Validation path | Use when |
   | --- | --- |
   | HTTP-01 | Port `80` reaches p2pstream. |
   | TLS-ALPN-01 | Port `443` reaches p2pstream. |
   | DNS-01 | You need wildcard certificates or cannot expose validation ports. |

   <figure class="doc-screenshot">
     <img src="../assets/new/tls_httpchallenge_letsencrypt_modal.png" alt="p2pstream Edit TLS Mapping drawer showing HTTP-01, Let's Encrypt staging, a hostname pattern, and HTTPS listener">
     <figcaption>The TLS mapping drawer binds the public hostname to the HTTPS listener and selects the ACME validation method and CA environment.</figcaption>
   </figure>

5. Review the Site readiness items and select **Publish**. Until publication, the draft and its routes do not claim the hostname or receive traffic. Readiness checks configuration; verify public DNS and upstream connectivity separately.

## Verification

Run:

```bash
curl -I https://app.example.com
```

Then check **Overview** for request counts and status classes. If you need request-stage details, open **Monitor -> Traffic**, enable **Tracing**, repeat the request, and inspect the selected route target in **Recent traces**.

<figure class="doc-screenshot">
  <img src="../assets/new/traffic_trace_request_details.png" alt="p2pstream Trace details drawer showing the request outcome, resolved listener, route and target, lifecycle, and policy decisions">
  <figcaption>The Trace details drawer confirms which listener, route, and target handled the request, whether cache or traffic-shaping policy was involved, and the retained request lifecycle.</figcaption>
</figure>

## Troubleshooting

| Symptom | Check |
| --- | --- |
| `502 Bad Gateway` | Test the target URL from the p2pstream server/container. |
| `503 Service Unavailable` | Confirm the route has an enabled available target; check target health if health checks are enabled. |
| Fallback/self-signed certificate | Add or fix the **TLS** certificate mapping for the requested hostname. |
| Route does not match | Confirm the Site is published and enabled, its hostname and listener assignments, and the route's path prefix and priority. |

For frontend assets such as CSS, JavaScript, images, and fonts, configure public asset caching under **Traffic Policy -> Cache**. See [Public Asset Cache](../concepts/cache).

## Next Steps

- [Expose a home lab app](./expose-a-home-lab-app)
- [Routing](../concepts/routing)
- [Public TLS and ACME reference](../reference/public-tls-acme)

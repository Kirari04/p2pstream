# Redirects and Static Responses

Return redirects or fixed local responses without forwarding the request to an upstream service.

## Use This When

Use redirects for host/path migrations. Use static responses for maintenance pages, health probes, or deliberate sink routes.

## Prerequisites

- A listener that receives the public request.
- A Site assigned to the listener, with the intended hostnames, and a clear path match.

## Steps

1. To redirect a whole host, open **Proxy -> Sites** and create a draft Site for `old.example.com` on `public-https`. Inside the Site workspace, select **Add first route** and configure:

   | Field | Value |
   | --- | --- |
   | Priority | `10` |
   | Path prefix | `/` |
   | Action | Redirect |
   | Mode | External origin |
   | Target | `https://new.example.com` |
   | Status | `308` |
   | Preserve path suffix | On |
   | Preserve query | On |

   Save the route, configure HTTPS certificate coverage for the Site, and publish it. This sends:

   ```text
   https://old.example.com/docs?a=1 -> https://new.example.com/docs?a=1
   ```

   <figure class="doc-screenshot">
     <img src="../assets/new/proxy_redirect_route_modal.png" alt="p2pstream Edit Route drawer showing an external-origin redirect with status, path-suffix, and query controls">
     <figcaption>The route drawer keeps redirect mode, target, status code, path-suffix preservation, and query handling with the match that triggers it.</figcaption>
   </figure>

2. To redirect a path on the same host, open the Site for `app.example.com`, add a route, and use same-host path mode:

   | Field | Value |
   | --- | --- |
   | Path prefix | `/old` |
   | Mode | Same host path |
   | Target | `/new` |
   | Status | `302` |

3. To serve a static maintenance response, open its Site under **Proxy -> Sites**, select the route's **Edit** action, then select **Add Target** and configure a static target:

   | Field | Value |
   | --- | --- |
   | Name | `maintenance` |
   | Type | Static |
   | Status | `503` |
   | Body | `Maintenance in progress` |
   | Enabled | On |

   Expand **Advanced target settings** to select a reusable **Generic body** template or add response headers such as `Content-Type` and `Retry-After`. Create reusable bodies under **Templates**, then select one in the route drawer.

   <figure class="doc-screenshot">
     <img src="../assets/new/proxy_static_response_target_modal.png" alt="p2pstream Edit Route drawer showing a static target with status, inline body, weight, priority group, and enabled controls">
     <figcaption>A static target returns directly from p2pstream. The visible route drawer controls its status and inline body for deliberate local responses such as maintenance pages, probes, or temporary sink routes.</figcaption>
   </figure>

   <figure class="doc-screenshot">
     <img src="../assets/new/edit_template_modal.png" alt="p2pstream Edit Response Template drawer showing a generic-body template editor and sandboxed preview">
     <figcaption>Generic response templates centralize reusable bodies for static targets, rate-limit responses, and WAF block responses while each caller keeps control of status and headers.</figcaption>
   </figure>

4. Give that route a lower priority number than the normal app route in the same Site:

   | Field | Value |
   | --- | --- |
   | Priority | `1` |
   | Path prefix | `/` |
   | Target | `maintenance` |

## Verification

Run:

```bash
curl -I https://old.example.com/docs?a=1
curl -i https://app.example.com/
```

Redirect routes should return `301`, `302`, `307`, or `308`. Static routes should return the configured status, body, and headers.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| Redirect target rejected | Same-host targets must be root-relative paths; external-origin targets must be HTTP/HTTPS origins. |
| Wrong route wins | Lower priority numbers run first. |
| Static route affects unintended paths | Narrow the route's path match inside its Site or disable the route after maintenance. |
| Template rejected | Static targets can only use generic body templates. |

## Next Steps

- [Routing](../concepts/routing)
- [Response templates reference](../reference/response-templates)
- [Routing rules reference](../reference/routing-rules)
- [Troubleshooting](../operations/troubleshooting#route-does-not-match)

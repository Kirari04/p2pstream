# Rate Limit a Route

Reject repeated requests before they reach route resolution and the upstream backend.

## Use This When

Use rate limits for login forms, expensive API endpoints, public probes, or client budgets that should fail fast with `429`.

## Prerequisites

- A route or hostname/path you can match precisely.
- A keying strategy that identifies clients correctly in your network layout.

## Steps

1. Open **Traffic Policy -> Rate Limits** and create a rule.

2. Configure the match:

   | Field | Value |
   | --- | --- |
   | Name | `login-limit` |
   | Priority | `10` |
   | Enabled | On |
   | Methods | `POST` |
   | Protocols | HTTPS |
   | Host patterns | `app.example.com` |
   | Path prefixes | `/login` |

3. Configure the algorithm. For login protection:

   | Field | Value |
   | --- | --- |
   | Algorithm | Sliding window |
   | Limit | `10` |
   | Window | `60000` ms |
   | Burst | `0` |

   For APIs that should allow short bursts, use token bucket:

   | Field | Value |
   | --- | --- |
   | Algorithm | Token bucket |
   | Limit | `120` |
   | Window | `60000` ms |
   | Burst | `240` |

4. Configure key parts. Default key is remote IP. Add key parts when you need a more specific budget:

   - remote IP + host,
   - remote IP + path,
   - header `Authorization` for authenticated API clients,
   - cookie or query parameter for known client identifiers.

5. Configure the response:

   | Field | Value |
   | --- | --- |
   | Status | `429` |
   | Content-Type | `text/plain; charset=utf-8` |
   | Body source | Inline |
   | Body | `Rate limit exceeded` |

   To reuse the same denial body across rules, open **Templates**, create a **Generic body** template, then set the rate-limit response body source to **Template** and select it. The rate-limit rule still controls the response status, content type, generated rate-limit headers, and custom response headers.

<figure class="doc-screenshot">
  <img src="../assets/new/edit_ratelimit_modal.png" alt="p2pstream rate-limit editor showing a route-specific match, sliding window algorithm, key parts, and custom response settings">
  <figcaption>The rate-limit editor is where the route match, client key, algorithm, budget, and denial response are reviewed before the rule starts rejecting matching requests.</figcaption>
</figure>

## Verification

Send repeated matching requests and watch **Overview -> Problem Signals** or **Traffic** tracing. A limited request should return `429` and should not reach route/backend selection.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| Every user is limited together | p2pstream may see one reverse-proxy IP; add key parts or place p2pstream at the edge. |
| Rule never fires | Confirm method, protocol, host pattern, path prefix, and priority. |
| Bursts are too large | Burst cannot exceed 10x limit and should be set intentionally. |
| Template option rejected | Rate-limit responses can only use generic body templates. |

## Next Steps

- [Limits and shaping](../concepts/limits-and-shaping)
- [Response templates reference](../reference/response-templates)
- [Rate limits reference](../reference/rate-limits)
- [Trace live traffic](./trace-live-traffic)

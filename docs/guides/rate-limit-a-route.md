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
   | Body | `Rate limit exceeded` |

## Verification

Send repeated matching requests and watch **Overview -> Problem Signals** or **Traffic** tracing. A limited request should return `429` and should not reach route/backend selection.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| Every user is limited together | p2pstream may see one reverse-proxy IP; add key parts or place p2pstream at the edge. |
| Rule never fires | Confirm method, protocol, host pattern, path prefix, and priority. |
| Bursts are too large | Burst cannot exceed 10x limit and should be set intentionally. |

## Next Steps

- [Limits and shaping](../concepts/limits-and-shaping)
- [Rate limits reference](../reference/rate-limits)
- [Trace live traffic](./trace-live-traffic)

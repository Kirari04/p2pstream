# Rate Limits Reference

Rate limit rules are global and evaluated after WAF rules and before traffic shapers and route resolution.

## Defaults and limits

| Setting | Default or limit |
| --- | --- |
| Name | `rate-limit` when empty |
| Limit | `60` |
| Window | `60000` ms |
| Window range | 1 second to 1 day |
| Response status | `429` |
| Response body | `Rate limit exceeded\n` |
| Default key | remote IP |
| Max key parts | `8` |
| Max value matchers | `32` |

## Algorithms

| Algorithm | Notes |
| --- | --- |
| Fixed window | Cheapest and easiest to understand. |
| Sliding window | Better fairness around window boundaries. |
| Token bucket | Allows bursts up to burst capacity. |
| Leaky bucket | Smooths bursty traffic. |

For token and leaky bucket rules, burst defaults to the effective limit when unset.

## Match fields

Rules can match:

- methods,
- protocols,
- host patterns,
- path prefixes,
- headers,
- cookies,
- query parameters.

Value matcher operators:

- present,
- equals,
- prefix,
- suffix,
- contains.

## Key parts

Key sources:

- remote IP,
- host,
- method,
- path,
- protocol,
- header,
- cookie,
- query parameter.

Use multiple key parts to avoid grouping unrelated clients together.

## Generated headers

Rate-limit responses include generated rate-limit metadata headers and any configured response headers.

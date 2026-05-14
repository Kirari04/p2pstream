# Limits and Shaping

Rate limits, WAF rules, and traffic shaping are global public proxy controls. WAF rules run first, then rate limits, then traffic shapers before normal route resolution and upstream forwarding.

| Layer | Runs | Typical use |
| --- | --- | --- |
| WAF | Before rate limits | Block, captcha, or queue traffic by HTTP match rules. |
| Rate limits | Before traffic shaping | Reject repeated requests with `429`. |
| Traffic shaping | Before route/backend forwarding | Slow upload and download streams without rejecting the request. |

## Rate limits

Rate limits run after the WAF and before route resolution. Matching can use:

- method,
- protocol,
- host pattern,
- path prefix,
- headers,
- cookies,
- query parameters.

Supported algorithms:

| Algorithm | Behavior |
| --- | --- |
| Fixed window | Counts requests in fixed time windows. |
| Sliding window | Counts recent requests across a moving window. |
| Token bucket | Allows bursts and refills over time. |
| Leaky bucket | Smooths bursts by draining over time. |

If no key parts are configured, the default key is remote IP.

::: warning Reverse proxy in front
If p2pstream sits behind another reverse proxy, the remote IP seen by p2pstream may be the proxy, not the original client. Design rate-limit keys accordingly.
:::

## Traffic shaping

Traffic shapers run after WAF and rate-limit checks. They can limit upload and download throughput in bytes per second.

Budget scope controls how buckets are shared:

| Scope | Behavior |
| --- | --- |
| Per key | Requests sharing the same key share the bandwidth budget. |
| Per request | Every request gets its own bandwidth budget. |

Rules can exempt the first bytes of a request or response so small requests stay responsive while large transfers are shaped.

## Priority

Within each policy type, rules are evaluated by priority, then ID. Lower priority numbers are evaluated first. A WAF rule that blocks, challenges, or queues a request prevents later rate-limit, shaper, route, and backend work for that request.

# Limits and Shaping

WAF rules, rate limits, traffic shapers, and cache rules are global public proxy controls.

## What It Is

These controls protect, slow, or cache public traffic around route/backend selection.

| Layer | Runs | Typical use |
| --- | --- | --- |
| WAF | Before rate limits | Block, captcha, or queue traffic by HTTP match rules. |
| Rate limits | Before traffic shaping | Reject repeated requests with `429`. |
| Traffic shaping | Before route/backend forwarding | Slow upload and download streams without rejecting the request. |
| Cache | After route/backend selection | Serve eligible public proxy-forward assets from proxy storage. |

## When It Matters

Use these controls when protecting login endpoints, controlling API budgets, slowing large transfers, queuing short traffic surges, or caching public static assets.

## Runtime Behavior

Evaluation order:

1. ACME HTTP challenge bypass
2. Reserved WAF endpoints
3. WAF rules
4. Rate limits
5. Traffic shapers
6. Route and backend selection
7. Cache rule evaluation and lookup
8. Upstream forwarding or cached response

Rate limits and traffic shapers can match method, protocol, host pattern, path prefix, headers, cookies, and query parameters. If no key parts are configured, remote IP is used.

Traffic shaping uses byte-per-second token buckets. `per_key` shares a bucket for requests with the same key; `per_request` gives each request its own bucket.

Cache rules store eligible public `GET`/`HEAD` responses for proxy-forward backends after route/backend selection. Cache hits still pass through WAF, rate limits, and traffic shaping first.

## Common Mistakes

- Using remote IP as the only key when p2pstream sits behind another reverse proxy.
- Creating broad rules with low priority numbers that catch unrelated traffic.
- Expecting cache hits to bypass rate limits or WAF.
- Allowing cookie-bearing cache requests on broad dynamic paths instead of precise static asset paths.

## Related Links

- [Rate limit a route](../guides/rate-limit-a-route)
- [Shape bandwidth](../guides/shape-bandwidth)
- [WAF](./waf)
- [Public asset cache](./cache)

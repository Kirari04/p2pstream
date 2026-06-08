# Limits and Shaping

WAF rules, rate limits, traffic shapers, and cache rules are global public proxy controls.

## What It Is

These controls protect, slow, or cache public traffic around route/target selection.

| Layer | Runs | Typical use |
| --- | --- | --- |
| WAF | Before rate limits | Block, captcha, or queue traffic by HTTP match rules. |
| Rate limits | Before traffic shaping | Reject repeated requests with `429`. |
| Traffic shaping | Before route/target forwarding | Slow upload and download streams without rejecting the request. |
| Cache | After route/target selection | Serve eligible public proxy assets from proxy storage. |

## When It Matters

Use these controls when protecting login endpoints, controlling API budgets, slowing large transfers, queuing short traffic surges, or caching public static assets.

## Runtime Behavior

Every public request passes through these layers in order. A request stopped at an earlier layer never reaches later ones.

Evaluation order:

1. ACME HTTP challenge bypass
2. Reserved WAF endpoints
3. WAF rules
4. Rate limits
5. Traffic shapers
6. Route and target selection
7. Cache rule evaluation and lookup
8. Origin forwarding or cached response

Policy matching uses request-only CEL `match_rule` expressions for method, protocol, host, path, remote IP/CIDR, headers, cookies, and query parameters. See [CEL Policy Matching](../reference/cel) for the shared matcher syntax, validation rules, and examples. Legacy `match` is removed from the public API; existing stored legacy rows are migrated automatically. If no key parts are configured, remote IP is used.

Cache `route_ids` and `target_ids` remain separate filters evaluated after route/target selection.

Traffic shaping uses byte-per-second token buckets. `per_key` shares a bucket for requests with the same key; `per_request` gives each request its own bucket.

Cache rules store eligible public `GET`/`HEAD` responses for proxy targets after route/target selection. Cache hits still pass through WAF, rate limits, and traffic shaping first.

<figure class="doc-screenshot">
  <img src="../assets/new/traffic_policies_waf_and_ratelimits.png" alt="p2pstream Traffic Policy page showing WAF rules and rate limits with priorities, matches, actions, and enabled state">
  <figcaption>The WAF and Rate Limits sections show the early policy layers that can block, challenge, queue, or reject traffic before routing.</figcaption>
</figure>

<figure class="doc-screenshot">
  <img src="../assets/new/traffic_policies_cache_and_trafficshaper.png" alt="p2pstream Traffic Policy page showing cache rules and traffic shapers with priorities, match summaries, rates, and cache controls">
  <figcaption>The Cache and Traffic Shapers sections show the later controls that shape matched streams or serve eligible proxy assets after target selection.</figcaption>
</figure>

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

# Rate Limits Reference

Rate limit rules are global public proxy rules evaluated after WAF rules and before traffic shapers and route resolution.

## Exact Fields And Defaults

| Setting | Default or limit |
| --- | --- |
| Name | `rate-limit` when empty |
| Limit | `60` |
| Window | `60000` ms |
| Response status | `429` |
| Response body source | Inline |
| Response body | `Rate limit exceeded\n` |
| Response content type | `text/plain; charset=utf-8` |
| Default key | remote IP |
| Max key parts | `8` |
| Max value matchers | `32` |

Algorithms:

| Algorithm | Notes |
| --- | --- |
| Fixed window | Cheapest and easiest to understand. |
| Sliding window | Better fairness around window boundaries. |
| Token bucket | Allows bursts up to burst capacity. |
| Leaky bucket | Smooths bursty traffic. |

## Validation Rules

- Limit must be at least `1`.
- Window must be between 1 second and 1 day.
- Burst must be non-negative and cannot exceed 10x limit.
- Response status must be between `400` and `599`.
- Template-mode responses require a selected `generic_body` response template.
- Header matcher names and response header names must be valid HTTP tokens.
- Protected generated headers such as `RateLimit-*`, `X-RateLimit-*`, `Retry-After`, `Content-Length`, and `Connection` cannot be configured as custom response headers.

Rules use request-only CEL `match_rule` rules. Empty match rules match every request. See [CEL Policy Matching](./cel) for variables, helper functions, builder behavior, limits, and examples.

Route data, backend data, backend health, and load-balancer state are not available inside rate-limit match CEL. Rate limits still run before route resolution.

Key sources:

- remote IP,
- host,
- method,
- path,
- protocol,
- header,
- cookie,
- query parameter.

<figure class="doc-screenshot">
  <img src="../assets/new/traffic_policies_waf_and_ratelimits.png" alt="p2pstream Traffic Policy rate limits section showing rule priority, match summaries, algorithms, budgets, and enabled state">
  <figcaption>The Rate Limits section shows the active budgets beside nearby WAF controls, making priority and match breadth easier to audit.</figcaption>
</figure>

<figure class="doc-screenshot">
  <img src="../assets/new/edit_ratelimit_modal.png" alt="p2pstream rate-limit rule editor showing match builder, algorithm, limit, window, burst, key parts, and response settings">
  <figcaption>The rate-limit editor configures the request match, key parts, algorithm, budget, and denial response served when the selected bucket is exhausted.</figcaption>
</figure>

## Runtime Effects

When a request exceeds the selected rule's budget, p2pstream returns the configured response and does not run traffic shaping, route resolution, backend selection, or cache lookup for that request.

When response body source is **Template**, p2pstream resolves the selected generic template body into the rule before serving the denial response. The rule's configured status, content type, generated rate-limit headers, and custom response headers still apply.

Token and leaky bucket burst defaults to the effective limit when unset.

## Examples

Login rule:

```text
Method: POST
Host pattern: app.example.com
Path prefix: /login
Algorithm: Sliding window
Limit: 10
Window: 60000 ms
Key: remote IP
```

## Related Tasks

- [Rate limit a route](../guides/rate-limit-a-route)
- [CEL Policy Matching](./cel)
- [Response templates reference](./response-templates)
- [Limits and shaping](../concepts/limits-and-shaping)
- [Troubleshooting rate limits](../operations/troubleshooting#rate-limits-affect-every-user)

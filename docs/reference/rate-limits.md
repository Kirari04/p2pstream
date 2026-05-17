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

Rules use request-only CEL `match_rule` rules. Empty match rules match every request.

`match_rule` is the only supported policy match shape. Legacy `match` is removed from the public API; existing stored legacy rows are migrated automatically to CEL/builder JSON.

Available CEL variables:

| Variable | Type | Notes |
| --- | --- | --- |
| `method` | string | Uppercase request method, such as `GET` or `POST`. |
| `protocol` | string | Listener protocol: `http` or `https`. |
| `host` | string | Normalized request host without port. |
| `path` | string | URL path. |
| `remote_ip` | string | Client remote IP. |
| `headers` | map string to list string | Header names are lowercase. Repeated headers keep all values. |
| `cookies` | map string to string | First cookie value by name. |
| `query` | map string to list string | Query parameter values by name. |

Helper functions:

- `host_match(host, pattern)` for exact and wildcard host patterns such as `*.example.com`.
- `path_prefix(path, prefix)` for path-prefix checks with segment boundaries.
- `cidr(remote_ip, cidr)` for IP range checks such as `198.51.100.0/24`.

CEL examples:

```cel
method == "POST" && host_match(host, "app.example.com") && path_prefix(path, "/login")
```

```cel
headers["x-plan"].exists(v, v == "free") || query["preview"].exists(v, v == "1")
```

```cel
!("session" in cookies) && path.matches("^/public/.+\\.(css|js)$")
```

```cel
cidr(remote_ip, "198.51.100.0/24")
```

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
- [Response templates reference](./response-templates)
- [Limits and shaping](../concepts/limits-and-shaping)
- [Troubleshooting rate limits](../operations/troubleshooting#rate-limits-affect-every-user)

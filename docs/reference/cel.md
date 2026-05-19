# CEL Policy Matching Reference

CEL policy matching is the shared request matcher used by WAF rules, rate-limit rules, traffic shaper rules, and cache rules.

## What It Is

`match_rule` is a request-only policy matcher. It can be configured with the management UI **Builder** mode or with raw CEL in **CEL** mode.

An empty or omitted `match_rule` matches any request.

Builder mode generates a CEL expression from grouped conditions. For non-empty builder rules, p2pstream stores both the structured `builder` and the generated `cel_expression` so the API can validate that both describe the same rule.

## Where It Runs

Policy match timing depends on the feature using the matcher:

| Feature | Match timing |
| --- | --- |
| WAF | Before rate limits, traffic shapers, route resolution, and forwarding. |
| Rate limits | After WAF, before traffic shaping and route resolution. |
| Traffic shapers | After WAF and rate limits, before route/backend forwarding. |
| Cache | After route/backend selection, before upstream forwarding. |

Cache `route_ids` and `backend_ids` are separate post-routing filters. They are not CEL variables.

## API Shape

`PublicPolicyMatchRule` has two fields:

| Field | Use |
| --- | --- |
| `cel_expression` | The CEL expression evaluated for each request. |
| `builder` | Structured groups and conditions that can generate a CEL expression. |

When both fields are supplied, `cel_expression` must exactly match the expression generated from `builder`.

Legacy `match` fields are removed from the public API. Existing stored legacy rows are migrated automatically to CEL/builder JSON.

## Variables

| Variable | Type | Notes |
| --- | --- | --- |
| `method` | string | Uppercase request method, such as `GET` or `POST`. |
| `protocol` | string | Listener protocol, `http` or `https`. |
| `host` | string | Normalized request host without port. |
| `path` | string | URL path. |
| `remote_ip` | string | Client remote IP parsed from the connection remote address. |
| `headers` | map string to list string | Header names are lowercase; repeated values are preserved. |
| `cookies` | map string to string | First cookie value by name. |
| `query` | map string to list string | Query parameter values by name. |

## Helper Functions

`host_match(host, pattern)` checks an exact or wildcard host pattern.

```text
host_match(host, "app.example.com")
host_match(host, "*.example.com")
```

`path_prefix(path, prefix)` checks a p2pstream path prefix. The prefix must start with `/`.

```text
path_prefix(path, "/api")
path_prefix(path, "/assets")
```

`cidr(remote_ip, cidr)` checks whether `remote_ip` is inside a valid IP prefix. Invalid IPs evaluate false. Literal invalid CIDR values are rejected during validation when p2pstream can see them statically.

```text
cidr(remote_ip, "198.51.100.0/24")
cidr(remote_ip, "2001:db8::/32")
```

## Common CEL Operators

| Expression | Meaning |
| --- | --- |
| `method == "POST"` | Exact string equality. |
| `method in ["GET", "HEAD"]` | Value is one of a list. |
| `path.startsWith("/api/")` | String prefix check. |
| `path.endsWith(".css")` | String suffix check. |
| `host.contains("example")` | String contains check. |
| `path.matches("^/assets/.+\\.js$")` | Regex match. |
| `"x-plan" in headers` | Header is present. Header names are lowercase. |
| `headers["x-plan"].exists(v, v == "free")` | Any repeated header value matches. |
| `query["version"].exists(v, v in ["v1", "v2"])` | Any query value matches. |
| `!("session" in cookies)` | Cookie is absent. |
| `A && B` | Both expressions must be true. |
| `A || B` | Either expression may be true. |
| `!(A)` | Expression must be false. |

## Builder Mode Mapping

Builder mode generates CEL from groups and conditions:

| Builder item | CEL behavior |
| --- | --- |
| `All` group | Joins child expressions with `&&`. |
| `Any` group | Joins child expressions with `||`. |
| `Not` group or condition | Wraps the generated expression with `!()`. |
| Header condition | Checks all repeated header values. |
| Query condition | Checks all repeated query values. |
| Cookie condition | Checks the first cookie value by name. |
| `Present` operator | Valid only for header, cookie, and query fields. |
| `CIDR` operator | Valid only for remote IP fields. |
| `Host pattern` operator | Valid only for host fields. |
| `Path prefix` operator | Values must start with `/`. |

Header and query conditions check all repeated values. Internally migrated legacy rules can preserve first-value behavior for old stored rows.

## Limits And Validation

| Limit | Value |
| --- | --- |
| CEL expression max size | `4096` bytes |
| Builder max groups and conditions | `64` total nodes |
| Condition max values | `64` |
| Condition value max size | `512` bytes |
| CEL evaluation cost limit | `20000` |

Expressions must compile and evaluate to bool. Regex literals are validated when p2pstream can see them statically.

Literal arguments receive targeted validation:

- `cidr(remote_ip, "...")` requires a valid CIDR prefix.
- `path_prefix(path, "...")` requires a prefix starting with `/`.
- `host_match(host, "...")` requires a non-empty host pattern.

## Examples

Login POST protection:

```text
method == "POST" && host_match(host, "app.example.com") && path_prefix(path, "/login")
```

Static asset cache matching:

```text
method in ["GET", "HEAD"] && host_match(host, "app.example.com") && path.matches("^/assets/.+\\.(css|js|png|webp|svg|woff2)$")
```

Header or query tier selection:

```text
headers["x-plan"].exists(v, v == "free") || query["tier"].exists(v, v == "free")
```

Bot user-agent match:

```text
headers["user-agent"].exists(v, v.matches("(?i)(bot|crawler)"))
```

Cookie absence:

```text
!("session" in cookies)
```

IPv4 CIDR:

```text
cidr(remote_ip, "198.51.100.0/24")
```

IPv6 CIDR:

```text
cidr(remote_ip, "2001:db8::/32")
```

Combined host, path, and method rule:

```text
method in ["GET", "POST"] && host_match(host, "*.example.com") && path_prefix(path, "/api")
```

## What CEL Cannot See

CEL policy matches cannot inspect:

- route data,
- backend data,
- backend health,
- load-balancer state,
- response data,
- cache result,
- request body.

For cache rules, route and backend scoping must use the rule's `route_ids` and `backend_ids` filters instead of CEL.

## Troubleshooting

| Symptom | Fix |
| --- | --- |
| Rule matches every request | Check whether `match_rule` is empty or the builder has no conditions. Empty matches mean any request. |
| Header rule does not match | Use lowercase header names such as `headers["x-plan"]`; p2pstream lowercases header keys. |
| Header or query value rule does not match | Use `.exists(v, ...)` because headers and query parameters are lists. |
| Regex is rejected | Compile the regex separately and escape backslashes correctly inside the CEL string. |
| Path prefix is rejected | Prefix values for `path_prefix` and builder path-prefix conditions must start with `/`. |
| CIDR is rejected or never matches | Use a valid CIDR prefix and confirm p2pstream sees the expected client IP. |
| Route or backend data is missing | CEL only sees request data. Use feature-specific route/backend filters where available. |

## Related Tasks

- [WAF reference](./waf)
- [Rate limits reference](./rate-limits)
- [Traffic shaping reference](./traffic-shaping)
- [Cache reference](./cache)

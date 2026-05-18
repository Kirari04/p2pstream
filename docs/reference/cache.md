# Cache Reference

Cache rules are global public proxy policy rules for public static assets.

## Exact Fields And Defaults

Cache rules run after route/backend selection and before forwarding a cache miss upstream.

| Field | Default | Description |
| --- | --- | --- |
| `name` | operator value | Rule label. |
| `priority` | `100` in database defaults | Lower numbers evaluate first. |
| `enabled` | `true` | Disabled rules are ignored. |
| `match_rule` | empty | Request-only CEL match rule. Empty matches every request. |
| `route_ids` | empty | Optional route filter. |
| `backend_ids` | empty | Optional backend filter. |
| `scope` | selected backend | Isolate by selected backend or route. |
| `ttl_mode` | `fixed` | `fixed` or `origin`. |
| `ttl_millis` | `3600000` | Rule TTL, or origin-TTL fallback. |
| `query_mode` | full query | `full`, `ignore`, `allowlist`, or `denylist`. |
| `query_params` | empty | Query names used by allowlist or denylist modes. |
| `vary_headers` | `Accept-Encoding` | Request headers included in the cache key. |
| `cache_status_codes` | `200`, `203`, `204`, `301`, `308` | Statuses that may be stored. |
| `max_object_bytes` | `104857600` | Maximum stored response size. |
| `add_cache_status_header` | false unless enabled | Adds `X-p2pstream-Cache`. |
| `allow_cookie_requests` | `false` | Allows matching requests with `Cookie` headers to use cache; cookie values are ignored and never stored. |

Storage defaults:

| Setting | Default |
| --- | --- |
| Disk directory | `${CONFIG_DIR}/cache/public`, or `PUBLIC_CACHE_DIR` |
| Max disk bytes | `1073741824` |
| Max memory bytes | `134217728` |
| Memory hot object max bytes | `262144` |
| Max entries | `100000` |
| Cleanup interval | `60000` ms |

## Validation Rules

p2pstream always bypasses cache for requests with `Authorization`, non-GET/HEAD methods, request bodies, `Range`, and upgrades.

Requests with `Cookie` bypass by default unless the matching rule enables `allow_cookie_requests`. Use that only for precise public static asset rules.

p2pstream refuses to store responses with `Set-Cookie`, `Cache-Control: no-store`, `private`, or `no-cache`, `Vary: *`, `Vary: Cookie`, `Vary: Authorization`, disallowed status codes, or bodies larger than the rule limit.

Configured Vary headers cannot be `Cookie`, `Authorization`, or `Set-Cookie`.

`match_rule` is the only supported policy match shape. Legacy `match` is removed from the public API; existing stored legacy rows are migrated automatically to CEL/builder JSON.

Cache rule matches inspect only request data through CEL.

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
method in ["GET", "HEAD"] && host_match(host, "app.example.com") && path_prefix(path, "/_nuxt/")
```

```cel
path.matches("^/assets/.+\\.(css|js|png|webp|svg|woff2)$")
```

```cel
headers["accept"].exists(v, v.contains("text/css")) || query["asset"].exists(v, v == "1")
```

```cel
!("session" in cookies) && cidr(remote_ip, "198.51.100.0/24")
```

Route data, backend data, backend health, and load-balancer state are not available inside cache match CEL. Cache-specific `route_ids` and `backend_ids` remain separate filters evaluated after route/backend selection.

## Runtime Effects

Request order:

1. ACME HTTP challenge bypass
2. Reserved WAF endpoints
3. WAF evaluation
4. Rate limits
5. Traffic shaper selection
6. Route/backend resolution
7. Cache rule evaluation and lookup
8. Cache hit response, or upstream forwarding and cache store
9. Final response

Cache hits still consume rate-limit buckets and still use traffic shaping. Redirect routes and static backends are not cached. `HEAD` requests can be served from a cached `GET` object, but `HEAD` does not create a new cache object.

Cache statuses in traces and events:

| Status | Meaning |
| --- | --- |
| `hit` | A valid cached object was served. |
| `miss` | A rule matched, no valid object was available, and the request was forwarded upstream. |
| `bypass` | Cache was skipped because a safety rule or request condition prevented lookup/store. |
| `expired` | A matching entry existed but was expired, so the request was forwarded upstream. |
| `stored` | A complete upstream response was committed to cache. |
| `store_failed` | p2pstream attempted to capture a miss response but did not commit it. |

## Examples

Static asset suffixes:

```text
.css
.js
.png
.jpg
.jpeg
.webp
.svg
.woff2
```

Nuxt-style rule:

```text
Host: app.example.com
Path prefix: /_nuxt/
Path suffixes: .js, .css, .png, .webp, .svg, .woff2
TTL mode: Origin TTL
Cache requests with Cookie headers: On only if those assets are public
```

## Related Tasks

- [Public asset cache](../concepts/cache)
- [Trace live traffic](../guides/trace-live-traffic)
- [Troubleshooting cache misses](../operations/troubleshooting#static-asset-is-not-cached)

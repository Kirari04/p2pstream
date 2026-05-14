# Cache Reference

Cache rules are global public proxy policy rules for public static assets. They run after route/backend selection and before forwarding a cache miss upstream.

## Request order

1. ACME HTTP challenge bypass
2. Reserved WAF endpoints
3. WAF evaluation
4. Rate limits
5. Traffic shaper selection
6. Route/backend resolution
7. Cache rule evaluation and lookup
8. Cache hit response, or upstream forwarding and cache store
9. Final response

Cache hits still consume rate-limit buckets and still use traffic shaping. They do not bypass WAF, rate limits, or shapers.

## Rule fields

| Field | Description |
| --- | --- |
| `name` | Operator label. |
| `priority` | Lower numbers evaluate first. |
| `enabled` | Disabled rules are ignored. |
| `match` | Method, protocol, host, path prefix, path suffix, header, cookie, and query matches. |
| `route_ids` | Optional route filter. |
| `backend_ids` | Optional backend filter. |
| `scope` | `selected_backend` isolates by selected backend; `route` shares across backends for the route. |
| `ttl_mode` | `fixed` or `origin`. |
| `ttl_millis` | Rule TTL, or origin-TTL fallback. Default `3600000`. |
| `query_mode` | `full`, `ignore`, `allowlist`, or `denylist`. |
| `query_params` | Query names used by allowlist or denylist modes. |
| `vary_headers` | Request headers included in the cache key. Default `Accept-Encoding`. |
| `cache_status_codes` | Statuses that may be stored. Default `200`, `203`, `204`, `301`, `308`. |
| `max_object_bytes` | Maximum stored response size. Default `104857600`. |
| `add_cache_status_header` | Adds `X-p2pstream-Cache: HIT` or `MISS`. |
| `allow_cookie_requests` | Allows matching requests with `Cookie` headers to use the cache. Default `false`. |

## Safe bypasses

p2pstream always bypasses cache for requests with `Authorization`. It also bypasses non-GET/HEAD methods, request bodies, `Range`, and upgrades.

Requests with `Cookie` bypass by default unless the matching rule enables `allow_cookie_requests`. Use that only for precise public static asset rules. Cookie values are ignored for cache keys and are never stored.

p2pstream refuses to store responses with `Set-Cookie`, `Cache-Control: no-store`, `private`, or `no-cache`, `Vary: *`, `Vary: Cookie`, `Vary: Authorization`, disallowed status codes, or bodies larger than the rule limit.

## TTL

Fixed TTL stores for the configured rule TTL.

Origin TTL reads `s-maxage`, then `max-age`, then `Expires`. If no usable origin TTL exists, the rule TTL is used. Origin denial headers are always respected.

## Cache key

The key includes listener protocol, normalized host, route or selected backend scope, normalized GET method for GET/HEAD sharing, path, query string according to query mode, configured vary headers, and origin `Vary` headers.

Raw cookies, authorization headers, and full cache keys are not stored in proxy event rows. Cookies and authorization headers are never included in cache keys.

## Storage settings

| Setting | Default |
| --- | --- |
| Disk directory | `${CONFIG_DIR}/cache/public`, or `PUBLIC_CACHE_DIR` |
| Max disk bytes | `1073741824` |
| Max memory bytes | `134217728` |
| Memory hot object max bytes | `262144` |
| Max entries | `100000` |
| Cleanup interval | `60000` ms |

## Example asset rule

Use a host match for the public domain, a path prefix such as `/assets/`, and suffixes:

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

Set fixed TTL to `3600000` ms for a one-hour cache, or use origin TTL when the upstream already sends cache headers.

For a Nuxt app, use a path prefix such as `/_nuxt/` with hashed asset suffixes. If logged-in browsers send cookies for those assets, enable `allow_cookie_requests` on that rule. Responses with `Set-Cookie`, private/no-store/no-cache, `Vary: Cookie`, or `Vary: Authorization` still will not be stored.

## Limitations

The first version does not cache redirect routes or static backends, does not serve stale-if-error, and does not implement stale-while-revalidate.

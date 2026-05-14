# Public Asset Cache

p2pstream can cache public proxy-forward responses on the proxy server. Cache rules are global traffic policy rules, evaluated after WAF, rate limits, traffic shaping, and route/backend selection. Cache hits still pass through those earlier policy layers before p2pstream serves the cached object.

The cache is designed for public static assets such as CSS, JavaScript, images, SVGs, and fonts. It is not a session or API response cache.

## Safe defaults

Requests are never cached when they include:

- `Authorization`,
- a request body,
- `Range`,
- WebSocket or other upgrade headers,
- methods other than `GET` or `HEAD`.

Requests with `Cookie` bypass cache by default. A cache rule can explicitly enable `allow_cookie_requests` for precise public asset matches, such as hashed JavaScript, CSS, images, or fonts. Cookie values are ignored for the cache key and are never stored.

Responses are never cached when they include:

- `Set-Cookie`,
- `Cache-Control: no-store`,
- `Cache-Control: private`,
- `Cache-Control: no-cache`,
- `Vary: *`,
- `Vary: Cookie`,
- `Vary: Authorization`,
- a status code not allowed by the rule,
- a body larger than the rule maximum object size.

There is no force-cache option for private or no-store responses.

## Cookie-tolerant asset rules

Logged-in browsers often send cookies on every same-site asset request, including files that are still public build artifacts. For those cases, open **Traffic Policy -> Cache**, add or edit a rule, and enable **Cache requests with Cookie headers** under **Cache behavior**.

This setting is per rule and defaults to off. Existing cache rules stay conservative until you explicitly enable it.

Use it only with precise public asset matches. For a Nuxt or similar hashed build output, a typical rule uses:

| Field | Value |
| --- | --- |
| Host | your public app domain |
| Path prefix | `/_nuxt/` |
| Path suffixes | `.js`, `.css`, `.png`, `.webp`, `.svg`, `.woff2` |
| TTL mode | Origin TTL when the upstream sends immutable asset headers |
| Cache requests with Cookie headers | On |

Even when cookie requests are allowed, p2pstream ignores cookie values for cache keys and does not store them. `Authorization` requests still always bypass cache. Responses with `Set-Cookie`, private cache directives, `Vary: Cookie`, `Vary: Authorization`, or `Vary: *` are still not stored.

## Rule matching

Cache rules use the same ordered-policy model as WAF, rate limits, and traffic shapers: enabled rules are evaluated by priority, then ID, and the first match wins.

Rules can match method, listener protocol, host pattern, path prefix, path suffix, headers, cookies, query parameters, route IDs, and backend IDs. Path suffixes are useful for asset rules such as:

```text
.css
.js
.png
.jpg
.webp
.svg
.woff2
```

When cookie matching is used in a rule, remember that cookie-bearing requests still bypass unless the same matching rule allows cookie requests.

## Vary and compression

The default configured Vary header is `Accept-Encoding`. This is expected for compressed assets and allows p2pstream to store separate gzip, br, or uncompressed variants for the same URL.

Do not configure sensitive Vary headers. Cache rules reject configured Vary headers named `Cookie`, `Authorization`, or `Set-Cookie`. Origin responses with `Vary: Cookie`, `Vary: Authorization`, or `Vary: *` are not stored because those responses declare personalized or unbounded variants.

## TTL modes

Fixed TTL uses the rule TTL for every stored object. The default is `3600000` ms, or one hour.

Origin TTL respects `Cache-Control: s-maxage`, `Cache-Control: max-age`, and `Expires`. If the origin does not provide a usable TTL, p2pstream falls back to the rule TTL.

## Storage

Cached bodies are stored under `PUBLIC_CACHE_DIR` when set, otherwise `${CONFIG_DIR}/cache/public`. Metadata is stored in SQLite and small hot objects can also be kept in memory.

Default budgets:

| Setting | Default |
| --- | --- |
| Disk cache | `1073741824` bytes |
| Memory cache | `134217728` bytes |
| Hot object limit | `262144` bytes |
| Max entries | `100000` |
| Cleanup interval | `60000` ms |

Writes use temporary files and are promoted only after the full response body is read successfully.

## Backends

Caching applies only to proxy-forward backends in the first version. Redirect routes and static backends are not cached.

Direct backend misses are fetched from the p2pstream server. Agent-pool misses are fetched through the selected agent, then cached on the p2pstream server. Cache storage does not retry a failed request and does not change the no same-request replay behavior.

`HEAD` requests can be served from a cached `GET` object, but `HEAD` does not create a new cache object.

## Purge

Operators can purge all cache entries, all entries for one cache rule, or entries matching a host and optional path prefix.

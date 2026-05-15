# Public Asset Cache

p2pstream can cache public proxy-forward responses on the proxy server for static assets.

## What It Is

Cache rules are global traffic policy rules evaluated after WAF, rate limits, traffic shaping, and route/backend selection. Cache hits still pass through those earlier policy layers before p2pstream serves the cached object.

The cache is designed for public static assets such as CSS, JavaScript, images, SVGs, and fonts. It is not a session or API response cache.

## When It Matters

Use cache rules when public frontend assets are repeatedly fetched through p2pstream and the upstream can safely share those assets between visitors.

## Runtime Behavior

Requests are never cached when they include:

- `Authorization`,
- a request body,
- `Range`,
- WebSocket or other upgrade headers,
- methods other than `GET` or `HEAD`.

Requests with `Cookie` bypass cache by default. A cache rule can explicitly enable `allow_cookie_requests` for precise public asset matches, such as hashed JavaScript, CSS, images, or fonts. Cookie values are ignored for the cache key and are never stored.

Responses are never cached when they include `Set-Cookie`, `Cache-Control: no-store`, `private`, or `no-cache`, `Vary: *`, `Vary: Cookie`, `Vary: Authorization`, a disallowed status code, or a body larger than the rule maximum object size.

The default configured Vary header is `Accept-Encoding`. Fixed TTL uses the rule TTL, default `3600000` ms. Origin TTL respects `s-maxage`, `max-age`, and `Expires`, falling back to the rule TTL when the origin has no usable TTL.

Cached bodies are stored under `PUBLIC_CACHE_DIR` when set, otherwise `${CONFIG_DIR}/cache/public`. Metadata is stored in SQLite.

## Common Mistakes

- Enabling cookie-tolerant caching on dynamic pages instead of precise asset paths like `/_nuxt/`.
- Expecting `Authorization` requests to use the cache.
- Trying to cache static backends or redirect routes.
- Treating `Vary: Accept-Encoding` as a problem; it is the normal compressed-asset variant key.

## Related Links

- [Cache reference](../reference/cache)
- [Trace live traffic](../guides/trace-live-traffic)
- [Troubleshooting cache misses](../operations/troubleshooting#static-asset-is-not-cached)

# Public Asset Cache

p2pstream can cache public proxy-forward responses on the proxy server for static assets.

## What It Is

Cache rules are global traffic policy rules evaluated after WAF, rate limits, traffic shaping, and route/target selection. Cache hits still pass through those earlier policy layers before p2pstream serves the cached object.

The cache is designed for public static assets such as CSS, JavaScript, images, SVGs, and fonts. It is not a session or API response cache.

## When It Matters

Use cache rules when public frontend assets are repeatedly fetched through p2pstream and the upstream can safely share those assets between visitors.

## Runtime Behavior

:::warning `Authorization` requests are never cached
Any request that includes an `Authorization` header always bypasses the cache, regardless of cache rule configuration. If you are caching API responses, ensure they do not require this header.
:::

Requests are never cached when they include:

- `Authorization`,
- a request body,
- `Range`,
- WebSocket or other upgrade headers,
- methods other than `GET` or `HEAD`.

Requests with `Cookie` always bypass shared cache. The legacy `allow_cookie_requests` field may still appear in older configuration, but it is preserved only for compatibility and has no runtime effect.

Responses are never cached when they include `Set-Cookie`, `Cache-Control: no-store`, `private`, or `no-cache`, `Vary: *`, `Vary: Cookie`, `Vary: Authorization`, a disallowed status code, or a body larger than the rule maximum object size.

The default configured Vary header is `Accept-Encoding`. Fixed TTL uses the rule TTL, default `3600000` ms. Origin TTL respects `s-maxage`, `max-age`, and `Expires`, falling back to the rule TTL when the origin has no usable TTL.

Cached bodies are stored under `PUBLIC_CACHE_DIR` when set, otherwise `${CONFIG_DIR}/cache/public`. Metadata is stored in SQLite.

Manage both parts under **Traffic Policy → Cache**. The filterable rules table determines eligibility and cache keys. The separate **Cache storage & operations** section controls shared storage budgets and cleanup, and contains the destructive purge action. Unsaved storage changes are kept distinct from the last server response so operators can review them before saving.

<figure class="doc-screenshot">
  <img src="../assets/new/cache_settings_section.png" alt="p2pstream Traffic Policy Cache tab showing a filterable cache-rules table and separate Cache storage and operations settings">
  <figcaption>The Cache tab separates response eligibility rules from global storage budgets, cleanup behavior, and purge operations.</figcaption>
</figure>

<figure class="doc-screenshot">
  <img src="../assets/new/edit_cache_modal.png" alt="p2pstream cache rule drawer showing match builder, route and target filters, TTL, query handling, vary headers, status codes, and object limits">
  <figcaption>The cache rule drawer exposes both request matching and cache-safety controls, including route/target filters, TTL mode, query-key behavior, vary headers, cookie handling, and object limits.</figcaption>
</figure>

## Common Mistakes

- Enabling cookie-tolerant caching on dynamic pages instead of precise asset paths like `/_nuxt/`.
- Expecting `Authorization` requests to use the cache.
- Trying to cache static targets or redirect routes.
- Treating `Vary: Accept-Encoding` as a problem; it is the normal compressed-asset variant key.

## Related Links

- [Cache reference](../reference/cache)
- [Trace live traffic](../guides/trace-live-traffic)
- [Troubleshooting cache misses](../operations/troubleshooting#static-asset-is-not-cached)

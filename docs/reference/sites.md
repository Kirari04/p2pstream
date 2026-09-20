# Sites and Domain Aliases

A Site is a virtual host boundary on one public listener. It owns one exact primary hostname, optional aliases, and the routes whose paths run inside that hostname set.

Configure Sites under **Proxy → Sites**. Existing listener-wide routes remain legacy routes; upgrades do not adopt or clone them automatically. Attach a route explicitly from its **Routing scope** field.

Creating a Site claims its hostnames immediately, even when the Site is disabled. The primary and Serve aliases return `404` until an attached route matches, and they do not fall through to a legacy route. On an enabled Site, Redirect aliases still return `308` to the primary. Create or attach the Site routes as part of the same operational change.

## Hostname bindings

| Binding | Rule | Behavior |
| --- | --- | --- |
| Primary | One exact hostname, required | Serves the Site and is the canonical redirect destination. |
| Serve alias | Exact hostname or one-label wildcard | Serves the same Site routes while preserving the actual alias hostname for policy, authentication, forwarding, and cache isolation. |
| Redirect alias | Exact hostname or one-label wildcard | Returns `308 Permanent Redirect` to the stored primary hostname and preserves the request path and query. |

Wildcard aliases match exactly one label. `*.example.com` matches `app.example.com`, but not `example.com` or `a.b.example.com`. A hostname can belong to only one Site on a listener.

## Routing isolation

When a request hostname matches a Site, only routes attached to that Site are considered. A missing path or disabled Site does not fall through to another Site, a listener-wide legacy route, or a catch-all route. This prevents a claimed hostname from accidentally reaching an unrelated backend.

Site routes use the Site hostname bindings; their legacy host-pattern field is not used. Path routes retain their existing priority and default-route behavior within the Site. Deleting a Site is blocked until its attached routes are detached or removed.

## HTTPS and SNI

Every HTTPS hostname needs effective certificate coverage in **TLS**. Coverage reflects the certificate mapping the live selector would use, including exact-over-wildcard precedence and leaf SAN and validity checks.

For managed DNS Sites, the HTTP Host and TLS SNI names must both be present and equal. A mismatch returns `421 Misdirected Request`. ACME challenge handling remains available before Site routing.

IP-literal Sites can be used on HTTP. On HTTPS, normal clients do not send IP literals as SNI, so an IP Site cannot select its certificate mapping; use a DNS hostname instead.

## Access-control host and cookie scope

Serve aliases keep the alias as the actual request host. When a local access provider uses an explicit allowed-host list, include every exact or wildcard Serve alias that may show the login page; listing only the primary does not authorize another alias.

Local-auth cookies are host-only unless a cookie domain is configured. A cookie domain can cover related subdomains, but it cannot share a session across unrelated alias domains. Keep the default host-only cookie when isolation is desired, or configure separate authentication expectations for unrelated Serve aliases. Redirect aliases do not serve the Site login flow.

## Compatibility notes

- Legacy routes keep their stored priority and host-matching behavior, but a newly claimed Site hostname stops reaching them immediately. Routes are never attached or cloned automatically.
- Site hostname matching uses strict canonical IDNA and one-label wildcard rules; it does not change legacy wildcard semantics.
- The actual requested alias remains the authority used by cache, policy, access-control, and forwarded-host logic. Redirect aliases are the only bindings that substitute the primary hostname.
- Site listeners are immutable after creation. Create a new Site to move hostname ownership to another listener.

## Related links

- [Routing rules](./routing-rules)
- [Listeners](../concepts/listeners)
- [Public TLS and ACME](./public-tls-acme)

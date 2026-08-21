# Identity-Aware Access

p2pstream can protect a public route with a reusable access policy. The first provider type is **forward auth**: p2pstream asks a separate identity service whether a request is allowed, optionally checks returned group membership, and forwards trusted identity headers to the selected upstream.

Manage providers and policies under **Traffic Policy -> Access**, then assign a policy in **Proxy -> Routes -> Access policy**.

## Recommended Deployment

Use an identity-aware forward-auth service such as oauth2-proxy, Authelia, or Authentik in front of your OIDC/OAuth provider:

```text
browser -> p2pstream -> forward-auth service -> OIDC identity provider
                     -> protected route target
```

The identity service owns browser login, OAuth/OIDC callbacks, session cookies, logout, MFA, and account lifecycle. p2pstream owns route matching, the allow/deny decision boundary, optional group authorization, and safe propagation of the resulting identity.

Native OIDC clients and local end-user accounts are not part of this release. Keeping the first implementation provider-neutral avoids storing identity-provider secrets or duplicating session and MFA logic in the proxy. They can be added later as provider types without changing route assignments or policy semantics.

## Request Contract

For a protected route, p2pstream sends a bodyless `GET` to the configured forward-auth URL. It copies only these browser headers:

- `Accept`
- `Authorization`
- `Cookie`
- `User-Agent`

It generates trusted request context in:

- `X-Forwarded-Method`
- `X-Forwarded-Uri`
- `X-Forwarded-Host`
- `X-Forwarded-Proto`
- `X-Forwarded-Port`
- `X-Forwarded-For`
- `X-Real-Ip`
- `X-Original-Url`
- `X-Auth-Request-Redirect`

The client cannot supply these forwarding values. Resolved client IP uses the listener's trusted-proxy configuration when present; otherwise it uses the direct peer.

## Response Contract

| Auth response | Proxy behavior |
| --- | --- |
| `2xx` | Authentication succeeds. Optional group requirements are evaluated and the request can continue. |
| `3xx` | The redirect, safe response headers, body, and `Set-Cookie` are returned to the browser. |
| `401` or `403` | The denial, body, `WWW-Authenticate`, and `Set-Cookie` are returned to the browser. |
| Other `4xx` | The denial is returned to the browser. |
| `5xx`, timeout, invalid response, or unavailable configuration | Access fails closed with `503`. |

Forward-auth response bodies and headers are size-limited. Redirects are not followed by p2pstream.

## Provider Fields

| Field | Behavior |
| --- | --- |
| Name | Unique management name. |
| Forward-auth URL | Absolute HTTP(S) URL without embedded credentials or a fragment. HTTPS is recommended. |
| Timeout | `100` through `30000` milliseconds; default `5000`. |
| Skip TLS verification | Disables certificate verification for the auth endpoint. Use only for a deliberately trusted private endpoint. |
| Subject, user, email, groups headers | Header names read from a successful auth response. Groups are comma-separated. |
| Forwarded identity headers | Explicit allowlist copied from the successful auth response to the protected upstream. Maximum 16. |

Authorization, cookie, request-framing, hop-by-hop, `Forwarded`, `X-Forwarded-*`, and `X-Real-IP` headers cannot be configured as forwarded identity headers.

## Policy Fields

Each policy selects one provider. With no required groups, any successful `2xx` identity check grants access. With required groups, matching is exact and case-sensitive:

- **Any listed group** grants access when at least one configured group is returned.
- **Every listed group** grants access only when every configured group is returned.

Group mismatches return `403`. Disabling an assigned policy or its provider returns `503`; it does not make the route public.

## Trusted Identity Headers

Before any upstream request is sent, p2pstream removes every identity header claimed by any configured access provider, whether or not that route is protected. On a protected route it then injects only the selected provider's values from the successful auth response. This prevents a client, a public route, or route target header configuration from smuggling identity claims such as `X-Auth-Request-User` to an application.

Applications must trust these headers only on traffic received from p2pstream. Restrict direct network access to protected origins so a client cannot bypass the proxy.

## Cache And Observability

Protected routes always bypass shared response caching, even if a cache rule otherwise matches. This prevents one user's response from being served to another identity.

Live traffic traces emit **Access granted** or **Access denied** stages with policy/provider metadata. Usernames, email addresses, subjects, session cookies, and group values are not added to trace attributes.

## Operational Notes

- Use HTTPS between p2pstream and the auth service unless both share a trusted local transport.
- Keep the auth endpoint highly available; failure intentionally denies access.
- Do not place the auth endpoint behind a route protected by the same policy, which would create a loop.
- Test sign-in redirects, refreshed cookies, group changes, logout, and provider outages before protecting a production route.
- Removing a policy or provider is blocked while another configured object still references it.

## Related Links

- [Routing rules](./routing-rules)
- [Security hardening](../operations/security-hardening)
- [Architecture](../concepts/architecture)
- [Trace live traffic](../guides/trace-live-traffic)

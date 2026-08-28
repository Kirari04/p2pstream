# Identity-Aware Access

p2pstream can protect a public route with a reusable access policy. Choose **Local users** for a self-contained setup, or **Forward auth** to delegate identity to an existing service. Both provider types feed the same optional group policy and trusted identity-header boundary.

Manage providers and policies under **Traffic Policy -> Access**, then assign a policy in **Proxy -> Routes -> Access policy**.

## Built-In Local Users

A local provider owns its own users and groups. Passwords are accepted only on create or explicit replacement, stored as bcrypt hashes, and never returned by the management API. Creating a provider does not create a default account: add at least one enabled user before assigning its policy to a route.

Choose one login mode:

| Mode | Behavior |
| --- | --- |
| Sign-in form | Unauthenticated browsers receive a p2pstream-hosted HTML form. Successful login creates an opaque, revocable, HTTP-only session cookie. |
| HTTP Basic | Clients receive a standard `WWW-Authenticate: Basic` challenge and send credentials on each request. |
| Form + Basic | Existing form sessions are accepted; requests that explicitly send Basic credentials can use them as well. Other visitors see the form. |

Form submissions use a short-lived HTTP-only CSRF cookie, exact-origin validation or a bounded one-time form nonce, and a size-limited form body. The session token is random; only its SHA-256 hash is stored. Session lookup checks the provider and user on every request. Editing or disabling a user revokes all of that user's sessions. Disabling a provider or changing its login mode, session lifetime, allowed hosts, or cookie settings revokes all provider sessions.

The provider's **Browser boundary** limits which routed hosts may authenticate. Exact hosts, IP addresses, and leading wildcards such as `*.example.com` are supported; an empty list permits every host already matched by an assigned route. Cookies are host-only by default. A configured cookie domain must be covered by every allowed host, cannot be a public suffix, and intentionally shares the session with matching subdomains. `SameSite=None` forces `Secure`; HTTPS listeners also enable `Secure` automatically. Cookie names can be rotated to recover from stale or conflicting browser state.

**Login protection** applies to both the form and HTTP Basic. The default policy permits 5 failures for one username and client address and 25 failures across all usernames from one client during a 15-minute window, then blocks the affected bucket for 5 minutes. Limits, window, and block duration are configurable per provider but cannot be disabled. Concurrent password checks consume the same bounded attempt budget, unknown users perform dummy bcrypt work, and blocked requests return `429 Too Many Requests` with `Retry-After`.

Each local provider selects a **Sign-in page** from **Configure -> Templates**. The seeded `local-access-login-default` works immediately, while additional local sign-in templates can customize the full HTML and CSS, the form, and all authentication-error states. Required action, CSRF, username, and password placeholders keep the customized form wired to the server. Dynamic values are HTML-escaped, scripts are blocked, and the form is restricted to same-origin submission. See [Response templates](./response-templates) for every available placeholder.

The form posts back to the protected URL using the reserved `__p2pstream_access_login=1` query parameter, then redirects to the same path with that parameter removed. `__p2pstream_access_logout=1` revokes the current session and redirects in the same way. Do not use these reserved query names for application behavior on a locally protected route.

Local identities forward `X-Auth-Request-User`, `X-Auth-Request-Preferred-Username`, and, when present, `X-Auth-Request-Groups`. After Basic authentication, the proxy removes the credential-bearing `Authorization` header before contacting the protected upstream, so the password is not leaked to the application. Form-authenticated requests can still carry a separate application `Authorization` header. User groups are exact and case-sensitive.

Local session lifetimes can be configured from 5 minutes through 30 days. Use HTTPS for every locally protected route: both form and Basic credentials are exposed on an unencrypted connection. Basic is useful for API clients, but the form is generally a better browser and password-manager experience.

## Forward-Auth Deployment

Use an identity-aware forward-auth service such as oauth2-proxy, Authelia, or Authentik in front of your OIDC/OAuth provider:

```text
browser -> p2pstream -> forward-auth service -> OIDC identity provider
                     -> protected route target
```

The identity service owns browser login, OAuth/OIDC callbacks, session cookies, logout, MFA, and account lifecycle. p2pstream owns route matching, the allow/deny decision boundary, optional group authorization, and safe propagation of the resulting identity.

Forward auth remains the recommended option when you need OIDC/OAuth, MFA, centralized account lifecycle, or identity shared with other services.

## Forward-Auth Request Contract

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

## Forward-Auth Response Contract

| Auth response | Proxy behavior |
| --- | --- |
| `2xx` | Authentication succeeds. Optional group requirements are evaluated and the request can continue. |
| `3xx` | The redirect, safe response headers, body, and `Set-Cookie` are returned to the browser. |
| `401` or `403` | The denial, body, `WWW-Authenticate`, and `Set-Cookie` are returned to the browser. |
| Other `4xx` | The denial is returned to the browser. |
| `5xx`, timeout, invalid response, or unavailable configuration | Access fails closed with `503`. |

Forward-auth response bodies and headers are size-limited. Redirects are not followed by p2pstream.

## Forward-Auth Provider Fields

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

Each policy selects one provider. With no required groups, any authenticated identity grants access. With required groups, matching is exact and case-sensitive:

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
- [Response templates](./response-templates)
- [Trace live traffic](../guides/trace-live-traffic)

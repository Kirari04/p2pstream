# Sites

A Site is the public routing boundary for a set of hostnames and routes. A Site owns its routes and can be assigned to more than one public listener. Every assigned listener uses the same site route list.

Configure sites under **Proxy → Sites**. Create or open a site, configure its hostnames and listeners, and add routes inside the site workspace. New sites are saved as drafts and do not affect traffic until they are published.

**Add site** is in the Proxy page header while the Sites tab is open. Above the site list, the filter box matches site names, hostnames (in ASCII or Unicode form) and assigned listener names. You can also narrow the list by state (published, draft, disabled), by readiness (needs attention or ready) and, when there is more than one listener, by listener. Filters only change what the list shows; they don't change any site.

## Lifecycle

| State | Hostname or default site ownership | Routing behavior |
| --- | --- | --- |
| Draft | No live claim on any listener | Never receives traffic. Draft conflicts appear in readiness and are checked again at Publish. |
| Published and enabled | Reserves its hostname patterns or the listener's default site slot | Serves its routes or configured listener redirect. |
| Published and disabled | Keeps its hostname patterns or default site slot reserved | Returns `404`; requests do not fall through to another site. |
| Published with no listener assignments | No listener-local claim | Keeps its configuration but receives no traffic. |

Publishing is atomic. p2pstream rechecks hostname ownership, default site ownership, listener redirects, routes, targets, and HTTPS requirements before making the complete site active. An unsuccessful publish leaves the draft inactive.

Edits explicitly saved to an already published site apply immediately after validation. Draft publication is separate from the site's enabled switch: disabling a published site stops it from serving while retaining its claims.

## Hostname modes

A Site uses one of two hostname modes:

- **Specific hostnames** serves one or more exact or wildcard hostname patterns. A canonical hostname is optional unless a redirect needs an exact destination.
- **Any unmatched hostname** makes the site a default site for each assigned listener. It needs no hostname or canonical hostname.

Wildcard patterns match exactly one label. `*.example.com` matches `app.example.com`, but it does not match `example.com` or `a.b.example.com`.

Published hostname ownership is enforced independently on each listener. One published site can use the same hostname on several assigned listeners. Two published sites cannot own the same normalized pattern on one listener. Exact ownership takes precedence over a matching wildcard; disabling the exact site retains that ownership and returns `404`.

A Site can serve several hostnames without selecting a primary. When a canonical hostname is configured, it must be exact. Redirect aliases and listener redirects that need a hostname destination cannot use a wildcard as that destination.

## Listener assignments

Each assignment selects one behavior:

| Behavior | Result |
| --- | --- |
| **Serve site** | The listener selects this site by hostname, or as its default site, and evaluates the site's routes. |
| **Redirect to HTTPS** | The listener redirects to a selected HTTPS Serve binding for the same site while preserving the configured path and query behavior. |

Redirect assignments are validated for loops and destination ownership. A named site can preserve the validated incoming claimed hostname when no redirect hostname is set. A Default site must provide an explicit exact redirect hostname because the incoming host is otherwise unconstrained.

Removing a listener assignment releases this site's hostname ownership or default site slot on that listener. The site and its routes remain available through its other assignments. Removing the last assignment leaves the site as inactive configuration that can be assigned again.

Deleting a listener removes only its assignments; sites and their routes remain, including sites left with no assignments. If the listener is an HTTPS redirect destination, first change or remove the assignments that redirect to it.

## Default Sites and route defaults

Each listener can have at most one published default site, including a disabled published default site. Drafts may propose a conflicting default site assignment, but they cannot publish until the conflict is resolved.

Hostname selection happens before path routing:

1. An exact hostname site is selected when one owns the request hostname.
2. Otherwise, a matching one-label wildcard site is selected.
3. Otherwise, the listener's default site is selected, if one exists.
4. With no enabled selected site, the request receives `404`.

After p2pstream selects a site, it evaluates only that site's routes. A Site's default route handles unmatched paths inside that site. It is different from the listener's default site, which handles hostnames not owned by a named site.

A missing path or disabled named site never falls through to the listener's default site. This keeps hostname ownership isolated from unrelated backends.

## Routes in a Site

Routes are created and edited inside their owning site. The site workspace lists each route's path, action or target summary, priority, default state, and enabled state. Saving or canceling a route editor returns to the same site workspace.

All assigned listeners use the same route list. The actual incoming listener remains available to listener policies, authentication, cache isolation, observability, and redirect behavior.

Route priorities are evaluated only within the selected site. Lower numbers run first, with route ID as the tie-breaker. A Site may have one default route.

## Readiness

The readiness summary distinguishes blockers, warnings, checks whose result is unknown, and checks that do not apply. It checks the persisted configuration used by Publish, including:

- listener assignments and redirect destinations;
- hostname and default site ownership conflicts;
- certificate coverage for each hostname on each HTTPS listener;
- route and target validity; and
- incomplete redirects or canonical-host requirements.

HTTP listeners do not require certificates. A Default site does not require a hostname. A redirect-only site does not require proxy targets, and a site without a default route can be intentional.

Readiness describes configuration only. It does not claim that public DNS resolves, that the listener is reachable from the internet, or that an origin is healthy unless p2pstream has evidence for that check.

## HTTPS, authority, and SNI

Every served HTTPS hostname needs effective certificate coverage in **TLS**. Coverage follows the certificate mapping used by the live selector, including exact-over-wildcard precedence, certificate validity, and SAN coverage.

For a managed DNS hostname, the HTTP authority and TLS SNI names must both be present and equal. A mismatch returns `421 Misdirected Request`. ACME challenge handling retains its earlier precedence.

IP-literal sites can be served over HTTP. Normal HTTPS clients do not send an IP literal as SNI, so use a DNS hostname for HTTPS sites.

## Access-control host and cookie scope

A Serve hostname remains the request host for policy, authentication, forwarding, and cache isolation. When a local access provider uses an allowed-host list, include every exact or wildcard site hostname that can show its login page.

Local-auth cookies are host-only unless a cookie domain is configured. A cookie domain can cover related subdomains, but it cannot share a session across unrelated domains. Listener or alias redirects do not serve the site login flow.

## Migrating standalone routes

Upgraded configurations keep existing standalone listener routes authoritative until an approved migration succeeds. They are available through the migration workflow and are not adopted merely because a site is created.

The migration preview is bound to a digest of the reviewed listeners, routes, targets, headers, existing site ownership, certificate mappings, and affected policy scopes. Apply rejects a stale digest and requires a new preview after any relevant change. It also requires explicit acknowledgement of listed authority or HTTPS SNI hardening warnings.

When equivalence can be established, migration creates published sites, preserves original route IDs where one route has one destination, and copies listener-wide fallback routes into named sites so path misses keep their previous outcome. Copied targets, headers, secrets, access-policy references, response-template references, cache scopes, and retry scopes are carried in the same transaction. Durable source-to-destination mappings identify copied routes and targets. Every copied route becomes independently editable after migration; the preview lists each copy before Apply.

The preview blocks conversion when it cannot preserve behavior. Examples include legacy wildcard depth, an equal-priority fallback whose copied ID would change ordering, hostname canonicalization differences, an HTTPS IP-literal hostname, or overlap with existing published site ownership. Resolve the listed routes or migrate a narrower set of listeners and preview again.

Site creation, route moves and copies, listener assignments, policy reference updates, provenance mappings, and removal of standalone eligibility are one transaction. A blocker, missing warning acknowledgement, stale preview, or database error leaves the standalone configuration authoritative.

New listeners require site-owned routes immediately. Existing listeners retain standalone compatibility until their migration succeeds; afterward, requests that create or detach a route without a site are rejected. Older clients must be updated to publish new draft sites and edit shared listener assignments. A scalar listener update cannot silently remove the other bindings from a shared site.

## Related links

- [Routing rules](./routing-rules)
- [Listeners](../concepts/listeners)
- [Public TLS and ACME](./public-tls-acme)

# Sites

A Site is the public routing boundary for a set of hostnames and routes. A Site owns its routes and can be assigned to more than one public listener. Every assigned listener uses the same Site route list.

Configure Sites under **Proxy → Sites**. Create or open a Site, configure its hostnames and listeners, and add routes inside the Site workspace. New Sites are saved as drafts and do not affect traffic until they are published.

## Lifecycle

| State | Hostname or Default Site ownership | Routing behavior |
| --- | --- | --- |
| Draft | No live claim on any listener | Never receives traffic. Draft conflicts appear in readiness and are checked again at Publish. |
| Published and enabled | Reserves its hostname patterns or the listener's Default Site slot | Serves its routes or configured listener redirect. |
| Published and disabled | Keeps its hostname patterns or Default Site slot reserved | Returns `404`; requests do not fall through to another Site. |
| Published with no listener assignments | No listener-local claim | Keeps its configuration but receives no traffic. |

Publishing is atomic. p2pstream rechecks hostname ownership, Default Site ownership, listener redirects, routes, targets, and HTTPS requirements before making the complete Site active. An unsuccessful publish leaves the draft inactive.

Edits explicitly saved to an already published Site apply immediately after validation. Draft publication is separate from the Site's enabled switch: disabling a published Site stops it from serving while retaining its claims.

## Hostname modes

A Site uses one of two hostname modes:

- **Specific hostnames** serves one or more exact or wildcard hostname patterns. A canonical hostname is optional unless a redirect needs an exact destination.
- **Any unmatched hostname** makes the Site a Default Site for each assigned listener. It needs no hostname or canonical hostname.

Wildcard patterns match exactly one label. `*.example.com` matches `app.example.com`, but it does not match `example.com` or `a.b.example.com`.

Published hostname ownership is enforced independently on each listener. One published Site can use the same hostname on several assigned listeners. Two published Sites cannot own the same normalized pattern on one listener. Exact ownership takes precedence over a matching wildcard; disabling the exact Site retains that ownership and returns `404`.

A Site can serve several hostnames without selecting a primary. When a canonical hostname is configured, it must be exact. Redirect aliases and listener redirects that need a hostname destination cannot use a wildcard as that destination.

## Listener assignments

Each assignment selects one behavior:

| Behavior | Result |
| --- | --- |
| **Serve Site** | The listener selects this Site by hostname, or as its Default Site, and evaluates the Site's routes. |
| **Redirect to HTTPS** | The listener redirects to a selected HTTPS Serve binding for the same Site while preserving the configured path and query behavior. |

Redirect assignments are validated for loops and destination ownership. A named Site can preserve the validated incoming claimed hostname when no redirect hostname is set. A Default Site must provide an explicit exact redirect hostname because the incoming host is otherwise unconstrained.

Removing a listener assignment releases this Site's hostname ownership or Default Site slot on that listener. The Site and its routes remain available through its other assignments. Removing the last assignment leaves the Site as inactive configuration that can be assigned again.

Deleting a listener removes only its assignments; Sites and their routes remain, including Sites left with no assignments. If the listener is an HTTPS redirect destination, first change or remove the assignments that redirect to it.

## Default Sites and route defaults

Each listener can have at most one published Default Site, including a disabled published Default Site. Drafts may propose a conflicting Default Site assignment, but they cannot publish until the conflict is resolved.

Hostname selection happens before path routing:

1. An exact hostname Site is selected when one owns the request hostname.
2. Otherwise, a matching one-label wildcard Site is selected.
3. Otherwise, the listener's Default Site is selected, if one exists.
4. With no enabled selected Site, the request receives `404`.

After p2pstream selects a Site, it evaluates only that Site's routes. A Site's default route handles unmatched paths inside that Site. It is different from the listener's Default Site, which handles hostnames not owned by a named Site.

A missing path or disabled named Site never falls through to the listener's Default Site. This keeps hostname ownership isolated from unrelated backends.

## Routes in a Site

Routes are created and edited inside their owning Site. The Site workspace lists each route's path, action or target summary, priority, default state, and enabled state. Saving or canceling a route editor returns to the same Site workspace.

All assigned listeners use the same route list. The actual incoming listener remains available to listener policies, authentication, cache isolation, observability, and redirect behavior.

Route priorities are evaluated only within the selected Site. Lower numbers run first, with route ID as the tie-breaker. A Site may have one default route.

## Readiness

The readiness summary distinguishes blockers, warnings, checks whose result is unknown, and checks that do not apply. It checks the persisted configuration used by Publish, including:

- listener assignments and redirect destinations;
- hostname and Default Site ownership conflicts;
- certificate coverage for each hostname on each HTTPS listener;
- route and target validity; and
- incomplete redirects or canonical-host requirements.

HTTP listeners do not require certificates. A Default Site does not require a hostname. A redirect-only Site does not require proxy targets, and a Site without a default route can be intentional.

Readiness describes configuration only. It does not claim that public DNS resolves, that the listener is reachable from the internet, or that an origin is healthy unless p2pstream has evidence for that check.

## HTTPS, authority, and SNI

Every served HTTPS hostname needs effective certificate coverage in **TLS**. Coverage follows the certificate mapping used by the live selector, including exact-over-wildcard precedence, certificate validity, and SAN coverage.

For a managed DNS hostname, the HTTP authority and TLS SNI names must both be present and equal. A mismatch returns `421 Misdirected Request`. ACME challenge handling retains its earlier precedence.

IP-literal Sites can be served over HTTP. Normal HTTPS clients do not send an IP literal as SNI, so use a DNS hostname for HTTPS Sites.

## Access-control host and cookie scope

A Serve hostname remains the request host for policy, authentication, forwarding, and cache isolation. When a local access provider uses an allowed-host list, include every exact or wildcard Site hostname that can show its login page.

Local-auth cookies are host-only unless a cookie domain is configured. A cookie domain can cover related subdomains, but it cannot share a session across unrelated domains. Listener or alias redirects do not serve the Site login flow.

## Migrating standalone routes

Upgraded configurations keep existing standalone listener routes authoritative until an approved migration succeeds. They are available through the migration workflow and are not adopted merely because a Site is created.

The migration preview is bound to a digest of the reviewed listeners, routes, targets, headers, existing Site ownership, certificate mappings, and affected policy scopes. Apply rejects a stale digest and requires a new preview after any relevant change. It also requires explicit acknowledgement of listed authority or HTTPS SNI hardening warnings.

When equivalence can be established, migration creates published Sites, preserves original route IDs where one route has one destination, and copies listener-wide fallback routes into named Sites so path misses keep their previous outcome. Copied targets, headers, secrets, access-policy references, response-template references, cache scopes, and retry scopes are carried in the same transaction. Durable source-to-destination mappings identify copied routes and targets. Every copied route becomes independently editable after migration; the preview lists each copy before Apply.

The preview blocks conversion when it cannot preserve behavior. Examples include legacy wildcard depth, an equal-priority fallback whose copied ID would change ordering, hostname canonicalization differences, an HTTPS IP-literal hostname, or overlap with existing published Site ownership. Resolve the listed routes or migrate a narrower set of listeners and preview again.

Site creation, route moves and copies, listener assignments, policy reference updates, provenance mappings, and removal of standalone eligibility are one transaction. A blocker, missing warning acknowledgement, stale preview, or database error leaves the standalone configuration authoritative.

New listeners require Site-owned routes immediately. Existing listeners retain standalone compatibility until their migration succeeds; afterward, requests that create or detach a route without a Site are rejected. Older clients must be updated to publish new draft Sites and edit shared listener assignments. A scalar listener update cannot silently remove the other bindings from a shared Site.

## Related links

- [Routing rules](./routing-rules)
- [Listeners](../concepts/listeners)
- [Public TLS and ACME](./public-tls-acme)

# Routing

Public routing follows this ownership chain:

**Listener → Site → Route → Targets**

The listener accepts the connection. Its hostname bindings select one Site. That Site's routes select an action, and a forward action selects one of the route's targets.

## Site selection

A Site owns exact or one-label wildcard hostnames and can be assigned to several listeners. The same route list is used through every assignment.

For each request, p2pstream checks:

1. an exact hostname Site;
2. a one-label wildcard Site;
3. the listener's Default Site for an otherwise unmatched hostname.

Once a Site is selected, its ownership is exclusive. A path miss or disabled published Site returns `404`; it does not fall through to another hostname Site or the Default Site. A disabled published Site keeps its ownership reserved.

Draft Sites do not participate in selection. Publishing activates all validated hostname and listener assignments together.

## Route selection

Routes match paths inside their owning Site. They are sorted by priority, then ID. Lower priority numbers run first.

| Priority | Path | Result |
| --- | --- | --- |
| `10` | `/api` | Checked first for the selected Site. |
| `20` | `/` | Broader path fallback in that Site. |
| default | empty | Used when no non-default route matches. |

The Site default route is a path fallback. The listener Default Site is a hostname fallback. A Site can have one default route, and a listener can have one published Default Site.

A route performs either a forward action or a redirect action. Redirect routes return locally without contacting an origin. Forward routes select from their enabled targets.

## Target selection

Targets are grouped by `priority_group`. p2pstream uses only the lowest group that has an available target, so higher groups provide failover. Within a group, the route's load-balancing policy considers target positions and weights.

Proxy targets use direct transport from the server or agent transport from a matching connected agent. Static targets return a local status, headers, and body. If a matched forward route has no available target, p2pstream returns `503 Service Unavailable`; it does not choose a different route.

## Listener context remains significant

A Site's route list is shared, but the actual incoming listener still controls protocol and listener-specific behavior. It remains part of authentication, policy context, cache isolation, observability, certificate selection, and Serve-versus-redirect handling.

An HTTP assignment can serve while an HTTPS assignment serves the same Site with certificate checks. An HTTP assignment can instead redirect to one of that Site's HTTPS Serve assignments.

## Request processing

p2pstream performs an early Site and route-only match to determine path security and access policy. WAF, rate limits, and traffic shapers then run with the resolved routing context. The later routing pass selects a target and advances load-balancer state. Cache rules run after route and target selection and may serve an eligible proxy response without contacting the origin.

Routes default to strict path security. Strict routes reject encoded `/` and `\` separators before policy and forwarding stages. `allow_encoded_separators` exists for upstreams that require encoded path identifiers; shared cache bypasses those requests.

## Migration from standalone routes

Older configurations can contain routes owned directly by a listener. Those routes remain authoritative until the Site migration is successfully applied.

Migration first generates a revision-bound preview. It shows proposed named Sites and Default Sites, routes that keep their IDs, fallback routes that require copies, warnings that need acknowledgement, and blockers that need configuration changes. Apply recomputes the preview inside one transaction and rejects it if the reviewed configuration changed.

The converter copies listener-wide path and default fallbacks into each new named Site when that is necessary to keep the old request winner. It records source-to-destination identities and expands cache and retry scopes for copied routes and targets. Each copy becomes independently editable. It does not approximate legacy wildcard depth, ambiguous priority ties, hostname ownership conflicts, or other behavior it cannot prove equivalent.

## Related links

- [Sites](../reference/sites)
- [Routing rules reference](../reference/routing-rules)
- [Publish a service](../guides/publish-a-service)
- [Redirects and static responses](../guides/redirects-and-static-responses)

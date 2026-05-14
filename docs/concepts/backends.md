# Backends

Backends describe the destination behavior after routing. WAF, rate limits, and traffic shapers are evaluated before a backend is selected. Cache rules are evaluated after selection and apply only to proxy-forward backends.

## Backend types

| Type | Use |
| --- | --- |
| Proxy forward | Forward the request to an upstream origin. |
| Static | Return a configured status code, headers, and body directly from p2pstream. |

Static backends are useful for maintenance windows, health-check responses, or deliberate sink routes.

## Forward modes

| Mode | Behavior |
| --- | --- |
| Direct | The p2pstream server connects to the upstream origin. |
| Agent pool | The server selects a connected agent and the agent connects to the upstream origin. |

Use direct mode when the upstream is reachable from the server. Use agent pool mode when the upstream is reachable only from another network.

## Upstream response timeout

Proxy-forward backends have an upstream response-header timeout. The default is `60000` milliseconds. This controls how long p2pstream waits for the upstream to send response headers; it does not limit the total duration of a response after headers have arrived, so long streaming downloads can continue.

Direct backends enforce this timeout from the p2pstream server. Agent-pool backends enforce it on the selected agent. Health-check timeouts are configured separately and are not affected by this value.

Older agents that do not understand the timeout metadata continue using their built-in `30000` millisecond upstream response-header timeout until they are upgraded.

## Load balancing

Agent pool backends support:

- round-robin,
- weighted round-robin,
- random,
- weighted random,
- least active requests,
- weighted least active requests.

Agent assignment weights must be between `1` and `1000`. At least one enabled assignment is required for an enabled agent-pool backend.

Routes can also load-balance across multiple backends. Route backend weights are configured on the route, so the same backend can receive different traffic shares on different routes.

## Health checks and availability

Proxy-forward backends can define an HTTP health check:

- method `GET` or `HEAD`,
- path starting with `/`,
- interval and timeout,
- healthy and unhealthy thresholds,
- expected status range.

Direct backend health checks run from the p2pstream server. Agent-pool backend health checks run through each enabled assigned connected agent, so a target such as `http://127.0.0.1:8888` is checked on the agent host, not on the server host.

For agent-pool backends, p2pstream tracks health per backend-agent assignment. When health checks are enabled, routing skips unhealthy or passively cooling-down agents while keeping the backend available if another assigned connected agent is still eligible. The backend-level health status shown in the API and UI is an aggregate: healthy when any assigned connected agent is healthy, unhealthy when all assigned connected agents are unhealthy, and unknown when no connected agent has produced a decisive check.

Passive unhealthy routing skips only apply while health checks are enabled. If health checks are off, transient connection errors or timeouts fail only the current request; later requests can still select the same backend or agent assignment.

p2pstream does not replay the same client request to another backend after an upstream failure. With health checks enabled, later requests avoid the temporarily unhealthy backend or agent assignment until the cooldown expires or an explicit check recovers it.

Automatic WAF waiting-room rules can also use backend active-request pressure and agent active-request or CPU pressure as activation signals. Those signals reduce load before new requests reach backend selection.

## Upstream headers and basic auth

Proxy-forward backends can inject upstream request headers. Mark secrets as sensitive so the management UI does not require the value on every edit.

Upstream basic auth is configured separately. When basic auth is enabled, p2pstream controls the `Authorization` header for that backend.

## Cache

Proxy-forward backends can be cached by global cache rules. Direct backend cache misses are fetched from the p2pstream server. Agent-pool cache misses are fetched through the selected agent and stored on the p2pstream server.

Static backends are not cached by the public asset cache. Requests with `Cookie` or `Authorization` are always bypassed.

## TLS verification

`tls_skip_verify` disables upstream certificate verification for that backend. Use it only for controlled internal services while you fix the upstream certificate chain. Do not use it for public internet upstreams.

# Backends

Backends describe the destination behavior after routing. WAF, rate limits, and traffic shapers are evaluated before a backend is selected.

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

For agent-pool backends, p2pstream tracks health per backend-agent assignment. Routing skips unhealthy or passively cooling-down agents while keeping the backend available if another assigned connected agent is still eligible. The backend-level health status shown in the API and UI is an aggregate: healthy when any assigned connected agent is healthy, unhealthy when all assigned connected agents are unhealthy, and unknown when no connected agent has produced a decisive check.

If no health check is configured, a backend or agent assignment is eligible until p2pstream observes a connection or timeout failure. Those passive failures mark the direct backend or selected backend-agent assignment temporarily unhealthy for a short cooldown, then it is tried again.

p2pstream does not replay the same client request to another backend after an upstream failure. Later requests avoid the temporarily unhealthy backend.

Automatic WAF waiting-room rules can also use backend active-request pressure and agent active-request or CPU pressure as activation signals. Those signals reduce load before new requests reach backend selection.

## Upstream headers and basic auth

Proxy-forward backends can inject upstream request headers. Mark secrets as sensitive so the management UI does not require the value on every edit.

Upstream basic auth is configured separately. When basic auth is enabled, p2pstream controls the `Authorization` header for that backend.

## TLS verification

`tls_skip_verify` disables upstream certificate verification for that backend. Use it only for controlled internal services while you fix the upstream certificate chain. Do not use it for public internet upstreams.

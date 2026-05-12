# Backends

Backends describe the destination behavior after routing.

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

When a backend health check marks a backend unhealthy, route backend pools skip it. If no health check is configured, the backend is eligible until p2pstream observes a connection or timeout failure. Those passive failures mark the backend temporarily unhealthy for a short cooldown, then it is tried again.

p2pstream does not replay the same client request to another backend after an upstream failure. Later requests avoid the temporarily unhealthy backend.

## Upstream headers and basic auth

Proxy-forward backends can inject upstream request headers. Mark secrets as sensitive so the management UI does not require the value on every edit.

Upstream basic auth is configured separately. When basic auth is enabled, p2pstream controls the `Authorization` header for that backend.

## TLS verification

`tls_skip_verify` disables upstream certificate verification for that backend. Use it only for controlled internal services while you fix the upstream certificate chain. Do not use it for public internet upstreams.

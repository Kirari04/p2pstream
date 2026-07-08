# Route Targets

Route targets describe what a forward route can send traffic to. They replace the old standalone backend model: destinations now live directly on the route that uses them.

## What It Is

A forward route has one or more enabled targets. A target can either proxy to an upstream origin or return a static response directly from p2pstream.

| Target type | Use |
| --- | --- |
| Proxy | Forward the request to an upstream origin. |
| Static | Return a configured status code, headers, and body. |

Proxy targets have a transport:

| Transport | Behavior |
| --- | --- |
| Direct | The p2pstream server connects to the upstream origin. |
| Agent | The server selects a connected agent by label selector, then uses that agent to dial the upstream TCP origin. |

<figure class="doc-screenshot">
  <img src="../assets/new/proxy_backends_and_routes.png" alt="p2pstream Proxy page showing routes with direct, agent, static, redirect, and fallback targets">
  <figcaption>The Proxy routes view shows route matches, priorities, target health, and whether traffic is forwarded, redirected, or answered with a static response.</figcaption>
</figure>

<figure class="doc-screenshot">
  <img src="../assets/new/proxy_edit_backend_modal.png" alt="p2pstream route target editor showing target type, transport, URL, health check, load balancing, and timeout controls">
  <figcaption>The target editor contains the destination details for a forward route, including transport, upstream URL, health checks, failover group, weight, and timeout behavior.</figcaption>
</figure>

## Runtime Behavior

Route matching picks the first enabled matching route by priority, then ID. If no explicit route matches, the listener's enabled default route is used.

Forward target selection works in two steps:

1. p2pstream filters enabled, valid, and healthy targets.
2. It chooses from the lowest available priority group using the route target load-balancing policy.

Targets in a higher priority group are failover targets. They are only considered when every lower priority group is unavailable.

Agent targets use label selectors. A selector matches enabled agents whose labels contain every requested key/value, and all selector labels must match the same agent. User labels are configured in the Agent editor. Labels under `p2pstream.io/` are system-owned and read-only. The system label `p2pstream.io/agent-id=<agent public_id>` provides exact-agent targeting. Empty label values are valid, but should be used intentionally because they only match labels with an empty value.

Proxy targets have an upstream response-header timeout. The default is `60000` milliseconds. It controls how long p2pstream waits for upstream response headers; it does not limit response streaming after headers arrive.

Enable `SECRETS_ENCRYPTION_KEY` to encrypt stored upstream basic-auth passwords and sensitive upstream request headers in SQLite.

Direct and agent targets enforce HTTP timeout and origin TLS policy from the server transport. For agent targets, the selected agent carries a raw TCP stream over Yamux, but the server still owns HTTP semantics and TLS verification. `tls_skip_verify` disables server-side origin verification and should only be used for controlled internal services.

Proxy targets can define HTTP health checks:

- method `GET` or `HEAD`,
- path starting with `/`,
- interval and timeout,
- healthy and unhealthy thresholds,
- expected status range.

Direct target health checks run from the p2pstream server. Agent target health checks run through matching connected agents. When health checks are enabled, unhealthy or passively cooling-down targets or target-agent paths are skipped for later requests.

Cache rules apply only to proxy targets. Static targets and redirect routes are not cached.

Static response bodies can be defined inline on the target or selected from a central generic response template. The template supplies only the body; the static target still controls status and response headers such as `Content-Type`, `Cache-Control`, and `Retry-After`.

## Common Mistakes

:::warning Be careful with `tls_skip_verify`
`tls_skip_verify` disables TLS certificate validation for the connection between p2pstream and the origin. It is only appropriate for internal services with self-signed certificates that you control.
:::

- Setting an agent target origin that only the server can resolve. Agent target origins are resolved from the selected agent host.
- Forgetting to label agents before creating a label-selected agent target.
- Putting fallback targets in the same priority group as primary targets.
- Expecting p2pstream to replay the same failed request to another target.
- Confusing health-check timeout with upstream response-header timeout.

## Related Links

- [Publish a service](../guides/publish-a-service)
- [Build a multi-agent target](../guides/agent-pool)
- [Response templates reference](../reference/response-templates)
- [Cache](./cache)
- [Routing rules reference](../reference/routing-rules)

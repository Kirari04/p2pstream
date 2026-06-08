# Routing Rules Reference

Routes belong to listeners and are evaluated only for traffic received by that listener.

## Route Fields And Defaults

A non-default route requires at least one of:

- host pattern,
- path prefix.

| Field | Rule |
| --- | --- |
| `listener_id` | Required. Route is scoped to this listener. |
| `priority` | Lower numbers evaluate first. |
| `host_pattern` | Exact host or wildcard subdomain. |
| `path_prefix` | Must start with `/` when set. |
| `action` | `forward` or `redirect`; defaults to forward when unspecified. |
| `target_load_balancing` | Defaults to round-robin for forward target pools. |
| `is_default` | Marks the listener default route. One default route is allowed per listener. |
| `redirect_status_code` | Defaults to `302` when unset. |
| `redirect_preserve_path_suffix` | Defaults enabled. |
| `redirect_preserve_query` | Defaults enabled. |

## Target Fields

Forward routes require at least one enabled target.

| Field | Rule |
| --- | --- |
| `target_type` | `proxy` or `static`. |
| `url` | Required for proxy targets. Must be an HTTP or HTTPS origin. |
| `transport` | `direct` or `agent` for proxy targets. |
| `agent_selector.match_labels` | Required for agent targets. All labels must match the same enabled agent. |
| `priority_group` | Lowest available group is selected; higher groups are failover. |
| `weight` | `1` to `1000000`; defaults to `100`. |
| `agent_load_balancing` | Agent selection policy for agent targets. |
| `upstream_response_header_timeout_millis` | Defaults to `60000`. |

Static targets use `static_status_code`, `static_response_headers`, and either inline body text or a generic response template.

Agent labels are configured in the Agent editor. Labels under `p2pstream.io/` are system-owned. Use `p2pstream.io/agent-id=<agent public ID>` for exact-agent targeting. Empty selector values are allowed and match only agents with the same empty label value.

## Validation Rules

| Pattern | Matches |
| --- | --- |
| `app.example.com` | exactly `app.example.com` |
| `*.example.com` | `app.example.com`, `media.example.com` |

Wildcard patterns do not match the apex `example.com`.

Redirect routes require target mode, target, and status code `301`, `302`, `307`, or `308`.

<figure class="doc-screenshot">
  <img src="../assets/new/proxy_edit_route_modal.png" alt="p2pstream route editor showing listener, host pattern, path prefix, action, targets, and priority">
  <figcaption>The route editor defines the listener-scoped match, action, priority, and forward target pool or redirect settings.</figcaption>
</figure>

<figure class="doc-screenshot">
  <img src="../assets/new/proxy_direct_route_modal.png" alt="p2pstream route editor showing a direct upstream target">
  <figcaption>Direct proxy targets are used when the p2pstream server itself can reach the upstream origin.</figcaption>
</figure>

<figure class="doc-screenshot">
  <img src="../assets/new/proxy_agent_route_target_modal.png" alt="p2pstream route editor showing an agent-selected proxy target">
  <figcaption>Agent proxy targets select a connected agent by labels and dial the origin from that agent's network.</figcaption>
</figure>

<figure class="doc-screenshot">
  <img src="../assets/new/proxy_redirect_route_modal.png" alt="p2pstream route editor showing redirect action settings">
  <figcaption>Redirect routes return a local redirect response without selecting a route target.</figcaption>
</figure>

<figure class="doc-screenshot">
  <img src="../assets/new/proxy_static_response_target_modal.png" alt="p2pstream route target editor showing a static response target">
  <figcaption>Static response targets return a local status, headers, and body from p2pstream without forwarding upstream.</figcaption>
</figure>

## Runtime Effects

Routes are sorted by priority ascending, then route ID ascending. If no enabled non-default route matches, the listener default route handles the request.

At request time, disabled targets, unhealthy targets, invalid target configs, and unavailable agent selector matches are skipped. p2pstream selects from the lowest available priority group. If no target is usable, the response is `503`.

When target health checks are enabled, connection and timeout failures mark the selected target or target-agent path temporarily unhealthy for later requests. The original request is not replayed to another target.

After a route and target are selected, cache rules may serve eligible proxy `GET` or `HEAD` requests. Redirect routes and static targets are not cached.

## Example

Specific route before broad fallback:

| Priority | Host | Path | Target |
| --- | --- | --- | --- |
| `10` | `app.example.com` | `/api` | `api-direct` |
| `20` | `app.example.com` | `/` | `app-agent` |
| default | empty | `/` | `welcome-static` |

## Related Tasks

- [Publish a service](../guides/publish-a-service)
- [Redirects and static responses](../guides/redirects-and-static-responses)
- [Troubleshooting route matching](../operations/troubleshooting#route-does-not-match)

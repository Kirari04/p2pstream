# Routing Rules Reference

Routes belong to Sites. A request first selects a Site from the incoming listener and hostname, then evaluates that Site's shared route list.

Configure routes from **Proxy → Sites**. Open or create a Site and use its route section to add, edit, clone, order, enable, disable, or delete routes. New routes are bound to the open Site automatically.

Existing standalone listener routes may remain active after an upgrade until they complete the revision-bound migration described in [Sites](./sites#migrating-standalone-routes). They cannot be created through the normal Site workflow.

## Route fields and defaults

| Field | Rule |
| --- | --- |
| `site_id` | Required for Site route creation. It identifies the owning Site. |
| `priority` | Lower numbers evaluate first. Route ID breaks a tie. |
| `path_prefix` | Must start with `/` when set. An empty path matches within the owning Site. |
| `path_security_mode` | Defaults to `strict`. Use `allow_encoded_separators` only for an upstream that requires encoded `/` or `\` identifiers. |
| `access_policy_id` | Optional reusable identity policy. Requests fail closed when its provider or policy is unavailable. |
| `action` | `forward` or `redirect`; defaults to forward when unspecified. |
| `target_load_balancing` | Defaults to round-robin for forward target pools. |
| `is_default` | Handles path misses inside the owning Site. A Site can have one default route. |
| `redirect_status_code` | `301`, `302`, `307`, or `308`; defaults to `302` when unset. |
| `redirect_preserve_path_suffix` | Defaults enabled. |
| `redirect_preserve_query` | Defaults enabled. |

Hostname matching belongs to the Site. A Site-owned route does not define a separate host pattern or listener scope. All Serve listener assignments use the same route paths and targets.

## Target fields

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
| `tls_skip_verify` | Disables upstream certificate verification for a deliberately trusted private origin. |
| `upstream_response_header_timeout_millis` | Defaults to `60000`. |
| `upstream_request_headers` | Ordered headers added to upstream requests. Sensitive values are write-only and returned as saved-state metadata. |
| `upstream_basic_auth` | Optional upstream username and write-only password. |
| `health_check` | Optional method, path, interval, timeout, thresholds, and expected status range. |
| `static_status_code` | Local status returned by a static target. |
| `static_response_headers` | Headers returned by a static target. |
| `static_response_body_mode` | Inline body or generic response template. |
| `static_response_template_id` | Generic template selected in template mode. |

Static targets use `static_status_code`, `static_response_headers`, and either inline body text or a generic response template.

Agent labels are configured in the **Edit Agent** drawer. Labels under `p2pstream.io/` are system-owned. Use `p2pstream.io/agent-id=<agent public ID>` for exact-agent targeting. Empty selector values match only agents with the same empty label value.

Sensitive header values and basic-auth passwords remain write-only. An ordinary saved edit preserves their saved state. Cloning cannot copy masked values: the clone retains enabled basic authentication but blocks saving until its password is re-entered, and each masked sensitive header needs a new value. A cloned route also starts with its default-route flag cleared so it cannot silently replace the Site's current default.

## Path matching and defaults

Routes are sorted by priority ascending, then route ID ascending. p2pstream selects the first enabled non-default route whose path prefix matches. If none matches, it selects the Site's enabled default route. With no match, the selected Site returns `404`; it does not try another Site.

| Priority | Path | Result |
| --- | --- | --- |
| `10` | `/api` | Checked first inside the selected Site. |
| `20` | `/` | Broad path fallback inside the same Site. |
| default | empty | Used only when no non-default path route matches. |

A listener's Default Site handles an unmatched hostname. A Site's default route handles an unmatched path. These are independent settings.

## Path security

Every route has a path security mode:

| Mode | Behavior |
| --- | --- |
| `strict` | Rejects request targets containing encoded path separators such as `%2F` or `%5C` before WAF, rate limits, traffic shaping, cache, or forwarding. |
| `allow_encoded_separators` | Allows encoded separators for compatibility with upstreams that use encoded path IDs. Shared cache bypasses these requests. |

Decoded `.` and `..` path segments and raw literal backslashes are always rejected on public listeners. Encoded dots inside ordinary segment names, such as `/files/v1%2e2/readme`, are allowed; encoded dots that decode to a whole `.` or `..` segment are rejected.

WAF, rate-limit, and traffic-shaper path matching use p2pstream's decoded request path model. Keep route-specific policy simple when enabling encoded-separator compatibility.

## Runtime effects

p2pstream performs a lightweight Site and route match before WAF, rate limits, access control, and traffic shapers to determine path security and access policy. This pass does not select a target or advance load-balancer state. Target selection still happens after those policy layers.

An assigned access policy applies to forward, static, and redirect routes. Protected routes bypass shared cache. See [Identity-Aware Access](./access-control).

At request time, disabled targets, unhealthy targets, invalid target configurations, and unavailable agent selector matches are skipped. p2pstream selects from the lowest available priority group. If no target is usable, the response is `503`.

When health checks are enabled, connection and timeout failures mark the selected target or target-agent path temporarily unhealthy for later requests. The original request is not replayed to another target.

After a route and target are selected, cache rules may serve eligible proxy `GET` or `HEAD` requests. Redirect routes and static targets are not cached.

## Related tasks

- [Sites](./sites)
- [Publish a service](../guides/publish-a-service)
- [Redirects and static responses](../guides/redirects-and-static-responses)
- [Troubleshooting route matching](../operations/troubleshooting#route-does-not-match)

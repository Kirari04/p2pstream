# Routing Rules Reference

Routes belong to sites. A request first selects a site from the incoming listener and hostname, then evaluates that site's shared route list.

Configure routes from **Proxy → Sites**. Open or create a site and use its route section to add, edit, clone, order, enable, disable, or delete routes. New routes are bound to the open site automatically.

Existing standalone listener routes may remain active after an upgrade until they complete the revision-bound migration described in [Sites](./sites#migrating-standalone-routes). They cannot be created through the normal site workflow.

## Route fields and defaults

| Field | Rule |
| --- | --- |
| `site_id` | Required for site route creation. It identifies the owning site. |
| `priority` | Lower numbers evaluate first. Route ID breaks a tie. |
| `path_prefix` | Must start with `/` when set. An empty path matches within the owning site. |
| `path_security_mode` | Defaults to `strict`. Use `allow_encoded_separators` only for an upstream that requires encoded `/` or `\` identifiers. |
| `access_policy_id` | Optional reusable identity policy. Requests fail closed when its provider or policy is unavailable. |
| `action` | `forward` or `redirect`; defaults to forward when unspecified. |
| `target_load_balancing` | Defaults to round-robin for forward target pools. |
| `is_default` | Handles path misses inside the owning site. A Site can have one default route. |
| `redirect_status_code` | `301`, `302`, `307`, or `308`; defaults to `302` when unset. |
| `redirect_preserve_path_suffix` | Defaults enabled. |
| `redirect_preserve_query` | Defaults enabled. |

Hostname matching belongs to the site. A Site-owned route does not define a separate host pattern or listener scope. All Serve listener assignments use the same route paths and targets.

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
| `tls_skip_verify` | Defaults off. Disables certificate-chain, hostname, and expiry checks, allowing origin impersonation even on a private network. Use only temporarily for diagnosis; restore verification with a trusted certificate. |
| `upstream_response_header_timeout_millis` | Defaults to `60000`. |
| `upstream_request_headers` | Ordered headers added to upstream requests. Sensitive values are write-only and returned as saved-state metadata. |
| `upstream_basic_auth` | Optional upstream username and write-only password. |
| `health_check` | Optional method, path, interval, timeout, thresholds, and expected status range. |
| `static_status_code` | Local status returned by a static target. |
| `static_response_headers` | Headers returned by a static target. |
| `static_response_body_mode` | Inline body or generic response template. |
| `static_response_template_id` | Generic template selected in template mode. |

Static targets use `static_status_code`, `static_response_headers`, and either inline body text or a generic response template.

Agent labels are configured in the **Edit agent** drawer. Labels under `p2pstream.io/` are system-owned. Use `p2pstream.io/agent-id=<agent public ID>` for exact-agent targeting. Empty selector values match only agents with the same empty label value.

Sensitive header values and basic-auth passwords remain write-only. An ordinary saved edit preserves their saved state. Cloning cannot copy masked values: the clone retains enabled basic authentication but blocks saving until its password is re-entered, and each masked sensitive header needs a new value. A cloned route also starts with its default-route flag cleared so it cannot silently replace the site's current default.

## Path matching and defaults

Routes are sorted by priority ascending, then route ID ascending. p2pstream selects the first enabled non-default route whose path prefix matches. If none matches, it selects the site's enabled default route. With no match, the selected site returns `404`; it does not try another site.

| Priority | Path | Result |
| --- | --- | --- |
| `10` | `/api` | Checked first inside the selected site. |
| `20` | `/` | Broad path fallback inside the same site. |
| default | empty | Used only when no non-default path route matches. |

A listener's default site handles an unmatched hostname. A Site's default route handles an unmatched path. These are independent settings.

## Path security

Every route has a path security mode:

| Mode | Behavior |
| --- | --- |
| `strict` | Rejects encoded path separators such as `%2F` or `%5C`, as well as the ambiguities listed below, before WAF, rate limits, access control, traffic shaping, cache, or forwarding. |
| `allow_encoded_separators` | Allows singly encoded separators for upstreams that use opaque path IDs. Shared cache bypasses these requests. All other path checks still apply. |

Public listeners reject duplicate slashes, semicolon path parameters (including encoded forms), raw literal backslashes, and `.` or `..` segments using either slash or backslash separators. These checks apply in both modes, including when decoding an encoded separator would produce duplicate slashes or traversal. For example, `//admin`, `/admin;session=x`, `/admin%3Bsession=x`, and `/files/a%5C..%5Cadmin` return `400`.

Nested percent encodings are also rejected in both modes: Go's once-decoded path must not contain another valid `%HH` escape. This includes nested separators (`%252F`), dots (`%252e`), and ordinary prefix characters (`/%2561dmin`). Literal percent filenames such as `/files/100%25.txt`, spaces such as `/files/a%20b.txt`, and dots inside ordinary names such as `/files/v1%2e2/readme` remain allowed. A literal filename containing a percent followed by two hexadecimal digits must use another naming scheme. Inspection bounds each path representation to eight decoding passes and rejects unresolved nesting, bounding work even for deeply wrapped escapes.

Accepted paths retain their original escaped bytes when forwarded; validation does not normalize one path for matching and send a different path upstream. WAF, rate-limit, and traffic-shaper path matching use Go's once-decoded request path: an encoded slash becomes a route boundary, while an encoded backslash remains a literal character. Compatibility mode explicitly accepts potential parser differences with an upstream that uses opaque encoded IDs. If the upstream treats backslashes as route boundaries, `/admin%5Cpanel` can select a fallback route even though the origin interprets it as `/admin/panel`. Enable this mode only where that weaker parser agreement is acceptable, and keep route-specific policy simple. It never permits literal duplicate slashes or path parameters.

## Runtime effects

p2pstream performs a lightweight site and route match before WAF, rate limits, access control, and traffic shapers to determine path security and access policy. This pass does not select a target or advance load-balancer state. Target selection still happens after those policy layers.

An assigned access policy applies to forward, static, and redirect routes. Protected routes bypass shared cache. See [Identity-Aware Access](./access-control).

At request time, disabled targets, unhealthy targets, invalid target configurations, and unavailable agent selector matches are skipped. p2pstream selects from the lowest available priority group. If no target is usable, the response is `503`.

When health checks are enabled, connection and timeout failures mark the selected target or target-agent path temporarily unhealthy for later requests. The original request is not replayed to another target.

After a route and target are selected, cache rules may serve eligible proxy `GET` or `HEAD` requests. Redirect routes and static targets are not cached.

## Related tasks

- [Sites](./sites)
- [Publish a service](../guides/publish-a-service)
- [Redirects and static responses](../guides/redirects-and-static-responses)
- [Troubleshooting route matching](../operations/troubleshooting#route-does-not-match)

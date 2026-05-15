# Routing Rules Reference

Routes belong to listeners and are evaluated only for traffic received by that listener.

## Exact Fields And Defaults

A route requires at least one of:

- host pattern,
- path prefix.

| Field | Rule |
| --- | --- |
| `listener_id` | Required. Route is scoped to this listener. |
| `priority` | Lower numbers evaluate first. |
| `host_pattern` | Exact host or wildcard subdomain. |
| `path_prefix` | Must start with `/` when set. |
| `action` | `forward` or `redirect`; defaults to forward when unspecified. |
| `load_balancing` | Defaults to round-robin for forward backend pools. |
| `redirect_status_code` | Defaults to `302` when unset. |
| `redirect_preserve_path_suffix` | Defaults enabled. |
| `redirect_preserve_query` | Defaults enabled. |

Forward route pool fields:

| Field | Rule |
| --- | --- |
| `backend_assignments` | At least one assignment for forward routes. Backend IDs must be unique per route. |
| `weight` | `1` to `1000`; defaults to `100`. |
| `fallback_backend_id` | Optional route fallback when no assigned backend is eligible. |

## Validation Rules

| Pattern | Matches |
| --- | --- |
| `app.example.com` | exactly `app.example.com` |
| `*.example.com` | `app.example.com`, `media.example.com` |

Wildcard patterns do not match the apex `example.com`.

Redirect routes require target mode, target, and status code `301`, `302`, `307`, or `308`.

| Mode | Valid target |
| --- | --- |
| Same host path | Root-relative path such as `/new`. |
| External origin keep path | HTTP or HTTPS origin such as `https://new.example.com`. |
| Absolute URL | Full HTTP or HTTPS URL. |

## Runtime Effects

Routes are sorted by priority ascending, then route ID ascending. If no enabled route matches, the listener default backend handles the request.

At request time, disabled assignments, disabled backends, unhealthy backends, and invalid backend configs are skipped. If nothing remains, the route fallback is tried. If no fallback is usable, the response is `503`.

When backend health checks are enabled, connection and timeout failures mark the selected backend or backend-agent assignment temporarily unhealthy for later requests. The original request is not replayed to another backend.

After a route and backend are selected, cache rules may serve eligible proxy-forward `GET` or `HEAD` requests. Redirect routes and static backends are not cached.

## Examples

Specific route before broad fallback:

| Priority | Host | Path | Backend |
| --- | --- | --- | --- |
| `10` | `app.example.com` | `/api` | `api` |
| `20` | `app.example.com` | `/` | `app` |
| `1000` | empty | `/` | `default` |

## Related Tasks

- [Publish a service](../guides/publish-a-service)
- [Redirects and static responses](../guides/redirects-and-static-responses)
- [Troubleshooting route matching](../operations/troubleshooting#route-does-not-match)

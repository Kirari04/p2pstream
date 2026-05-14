# Routing Rules Reference

Routes belong to listeners and are evaluated only for traffic received by that listener.

## Required match

A route requires at least one of:

- host pattern,
- path prefix.

Path prefixes must start with `/`.

## Host patterns

| Pattern | Matches |
| --- | --- |
| `app.example.com` | exactly `app.example.com` |
| `*.example.com` | `app.example.com`, `media.example.com` |

Wildcard patterns do not match the apex `example.com`.

## Priority

Routes are sorted by:

1. priority ascending,
2. route ID ascending.

Use low numbers for specific rules and high numbers for fallbacks.

## Actions

Forward routes require at least one backend assignment. The legacy single `backend_id` field is treated as a one-backend pool when no assignment list is provided.

Forward route pool fields:

| Field | Rule |
| --- | --- |
| `backend_assignments` | At least one assignment for forward routes. Backend IDs must be unique per route. |
| `load_balancing` | `round_robin`, `weighted_round_robin`, `random`, `weighted_random`, `least_active_requests`, or `weighted_least_active_requests`. |
| `weight` | `1` to `1000`; defaults to `100`. |
| `fallback_backend_id` | Optional route fallback when no assigned backend is eligible. |

At request time, disabled assignments, disabled backends, unhealthy backends, and invalid backend configs are skipped. If nothing remains, the route fallback is tried. If no fallback is usable, the response is `503`.

When backend health checks are enabled, connection and timeout failures mark the selected backend, or selected backend-agent assignment, temporarily unhealthy for later requests. When health checks are disabled, those failures affect only the current request and do not remove the backend from routing. The original request is not replayed to another backend.

Proxy-forward backends also have a configurable upstream response-header timeout. The default is `60000` milliseconds. Direct backends wait for response headers from the p2pstream server, while agent-pool backends wait from the selected agent. This timeout is separate from the backend health-check timeout and does not limit response streaming after headers arrive.

After a route and backend are selected, cache rules may serve eligible proxy-forward `GET` or `HEAD` requests from the public asset cache. Cache hits still happen after WAF, rate limits, and traffic shaping. A hit follows the selected backend to Cache and then returns a response. A miss or bypass follows the selected backend to Cache and then continues to the direct upstream or selected agent. Redirect routes and static backends are not cached.

`Authorization` requests always bypass cache, while cookie-bearing requests bypass by default unless the matching cache rule explicitly allows them.

Redirect routes require:

- redirect target mode,
- target,
- status code `301`, `302`, `307`, or `308`.

## Redirect target validation

| Mode | Valid target |
| --- | --- |
| Same host path | Root-relative path such as `/new`. |
| External origin keep path | HTTP or HTTPS origin such as `https://new.example.com`. |
| Absolute URL | Full HTTP or HTTPS URL. |

## Default backend

Every listener has a default backend. It handles requests when no enabled route matches.

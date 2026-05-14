# Routing

Routes decide what a listener does with a matching request. WAF rules, rate limits, and traffic shapers run before route resolution, so challenged, queued, blocked, or rate-limited requests do not reach route backend selection.

## Match fields

A route belongs to one listener and must include at least one of:

- a host pattern,
- a path prefix.

Host patterns support:

- exact hosts, for example `app.example.com`,
- wildcard subdomains, for example `*.example.com`.

Path prefixes must start with `/`.

## Priority

Routes are sorted by priority, then ID. Lower priority numbers run first.

Example:

| Priority | Host | Path | Result |
| --- | --- | --- | --- |
| `10` | `app.example.com` | `/api` | checked first |
| `20` | `app.example.com` | `/` | fallback for same host |
| `100` | empty | `/` | broad listener fallback |

If no route matches, p2pstream uses the listener default backend.

## Actions

| Action | Behavior |
| --- | --- |
| Forward | Send the request to a backend. |
| Redirect | Return a redirect response without forwarding upstream. |

Redirect status codes must be one of `301`, `302`, `307`, or `308`.

## Forward backend pools

A forward route can select one or more backends. Each route backend assignment has:

- a backend,
- an enabled flag,
- a route-specific weight from `1` to `1000`.

Routes can use the same load-balancing algorithms as agent pools:

- round-robin,
- weighted round-robin,
- random,
- weighted random,
- least active requests,
- weighted least active requests.

p2pstream records the backend actually selected for each request in proxy request events and traffic traces.

## Route fallback backend

A forward route can define one fallback backend. If all assigned backends are disabled or unavailable, p2pstream tries the route fallback backend. If the fallback is absent or unavailable too, p2pstream returns `503 Service Unavailable`.

The listener default backend is still only used when no enabled route matches the request.

## Redirect target modes

| Mode | Target example | Behavior |
| --- | --- | --- |
| Same host path | `/new` | Redirects to a path on the same request host. |
| External origin keep path | `https://new.example.com` | Keeps the incoming path and query on another origin. |
| Absolute URL | `https://new.example.com/docs` | Redirects to the exact URL, with optional path/query preservation. |

## Operational advice

Keep broad routes at higher numeric priorities, such as `100` or `1000`. Use low numbers for specific host/path rules that must win.

Captcha and waiting-room challenges are handled before routing and do not replay request bodies after admission. Design form and upload clients so they can resubmit after the browser receives the pass or admission cookie.

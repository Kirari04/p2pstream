# Routing

Routes decide what a listener does with a matching request.

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

## Redirect target modes

| Mode | Target example | Behavior |
| --- | --- | --- |
| Same host path | `/new` | Redirects to a path on the same request host. |
| External origin keep path | `https://new.example.com` | Keeps the incoming path and query on another origin. |
| Absolute URL | `https://new.example.com/docs` | Redirects to the exact URL, with optional path/query preservation. |

## Operational advice

Keep broad routes at higher numeric priorities, such as `100` or `1000`. Use low numbers for specific host/path rules that must win.

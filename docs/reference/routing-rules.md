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

Forward routes require a backend.

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

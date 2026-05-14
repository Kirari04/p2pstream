# Database Reference

p2pstream uses SQLite.

## Default path

When `DATABASE_URL` is unset:

```text
${CONFIG_DIR}/p2pstream.db
```

Docker sets:

```text
CONFIG_DIR=/data
```

So the default Docker database path is:

```text
/data/p2pstream.db
```

## SQLite settings

p2pstream configures:

- WAL journal mode,
- synchronous normal,
- busy timeout `10000` ms,
- foreign keys on,
- private cache.

Backups should include:

```text
p2pstream.db
p2pstream.db-wal
p2pstream.db-shm
```

## Conceptual tables

| Area | Tables |
| --- | --- |
| Auth | `users`, `sessions` |
| Agents | `agents`, `connections`, `agent_stats` |
| Public proxy | `public_backends`, `public_listeners`, `public_routes`, `public_route_backends`, `public_backend_agents` |
| Headers | `public_backend_headers`, `public_backend_upstream_headers` |
| TLS | `public_tls_certificates`, `public_tls_dns_credentials` |
| Controls | `public_rate_limit_rules`, `public_waf_captcha_providers`, `public_waf_rules`, `public_waf_settings`, `public_traffic_shaper_rules` |
| Observability | `proxy_request_events` |

`public_waf_settings` stores the cookie signing secret used for WAF pass, admission, and queue cookies. `proxy_request_events` includes WAF rule and action fields when a WAF decision handles a request. `agent_stats` includes reported agent CPU percentage for dashboard summaries and automatic WAF activation.

Do not edit the database by hand while the server is running.

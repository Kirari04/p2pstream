# Database Reference

p2pstream stores server state in SQLite unless `DATABASE_URL` points somewhere else.

## Exact Fields And Defaults

When `DATABASE_URL` is unset, the database path is:

```text
${CONFIG_DIR}/p2pstream.db
```

Docker sets:

```text
CONFIG_DIR=/data
```

So the default Compose database path is:

```text
/data/p2pstream.db
```

SQLite is opened with WAL journal mode, synchronous normal, busy timeout `10000` ms, foreign keys enabled, and private cache.

## Validation Rules

- Backups should include `p2pstream.db`, `p2pstream.db-wal`, and `p2pstream.db-shm`.
- Do not edit the database by hand while the server is running.
- If `DATABASE_URL` is empty, p2pstream creates `CONFIG_DIR` and the certs directory with `0700` permissions.

## Runtime Effects

Conceptual table groups:

| Area | Tables |
| --- | --- |
| Auth | `users`, `sessions` |
| Agents | `agents`, `connections`, `agent_stats` |
| Public proxy | `public_backends`, `public_listeners`, `public_routes`, `public_route_backends`, `public_backend_agents`, `public_response_templates` |
| Headers | `public_backend_headers`, `public_backend_upstream_headers` |
| TLS | `public_tls_certificates`, `public_tls_dns_credentials` |
| Controls | `public_rate_limit_rules`, `public_waf_captcha_providers`, `public_waf_rules`, `public_waf_settings`, `public_traffic_shaper_rules`, `public_cache_settings`, `public_cache_rules`, `public_cache_entries` |
| Observability | `proxy_request_events` |

`public_waf_settings` stores the cookie signing secret used for WAF pass, admission, and queue cookies. `proxy_request_events` includes WAF, cache, route, backend, agent, byte, status, and duration fields. `agent_stats` includes reported agent CPU percentage for dashboard summaries and automatic WAF activation.

## Examples

Backup files:

```text
p2pstream.db
p2pstream.db-wal
p2pstream.db-shm
certs/
```

Explicit SQLite URL for local recovery commands:

```bash
p2pstream users reset-password admin \
  --database-url 'file:/var/lib/p2pstream/p2pstream.db?mode=rwc'
```

## Related Tasks

- [Backup and restore](../operations/backup-restore)
- [First login](../getting-started/first-login)
- [Configuration reference](./configuration)

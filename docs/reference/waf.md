# WAF Reference

WAF rules are global public proxy rules evaluated before rate limits, traffic shapers, route resolution, and backend forwarding.

## Exact Fields And Defaults

Reserved WAF endpoints:

| Path | Use |
| --- | --- |
| `/.p2pstream/waf/captcha/verify` | Captcha form verification. |
| `/.p2pstream/waf/waiting-room` | Waiting-room page endpoint. |
| `/.p2pstream/waf/waiting-room/status` | Waiting-room status and admission check. |

ACME HTTP challenges bypass the WAF before these reserved endpoints are handled.

WAF rule defaults:

| Setting | Default or limit |
| --- | --- |
| Name | `waf-rule` when empty |
| Priority | `100` in database defaults |
| Action | Block |
| Activation mode | Always |
| Captcha pass TTL | `1800000` ms, 30 minutes |
| Captcha pass TTL range | 1 minute to 24 hours |
| Default key | remote IP |
| Block status | `403` |
| Block content type | `text/plain; charset=utf-8` |
| Block body | `Request blocked\n` |
| Block body limit | 64 KiB |

Waiting-room defaults:

| Setting | Default | Range |
| --- | --- | --- |
| Max admitted sessions | `50` | 1 to 1,000,000 |
| Admission rate | `10/sec` | 1 to 100,000/sec |
| Admission session TTL | `600000` ms | 1 minute to 24 hours |
| Queue poll interval | `5000` ms | 1 to 60 seconds |
| Queue timeout | `1800000` ms | 1 minute to 24 hours |
| Page title | `Waiting room` | non-empty custom text |
| Page body | `Traffic is high. You will be admitted automatically.` | non-empty custom text |

Automatic activation defaults:

| Signal | Default |
| --- | --- |
| Request window | `10000` ms |
| Minimum request rate | `50` rps |
| Traffic spike multiplier | `4` |
| Proxy active requests | `100` |
| Backend active requests | `100` |
| Agent active requests | `50` |
| Server CPU | `85%` |
| Agent CPU | `85%` |
| Minimum active duration | `30000` ms |
| Quiet period | `60000` ms |

## Validation Rules

Captcha providers are created under **Traffic Policy -> WAF** and support Cloudflare Turnstile, hCaptcha, and Google reCAPTCHA v2 checkbox. Provider secret keys are required, stored server-side, and not sent back to the UI after creation. Captcha rules require an enabled provider.

WAF match fields reuse the rate-limit matcher model: methods, protocols, host patterns, path prefixes, headers, cookies, and query parameters.

WAF key parts reuse rate-limit key sources: remote IP, host, method, path, protocol, header, cookie, and query parameter.

Automatic trigger thresholds accept `0` to disable individual signals. CPU percentages are 0 to 100.

## Runtime Effects

Rules are ordered by priority, then ID. The first enabled matching rule wins.

p2pstream verifies captcha tokens against the provider `siteverify` endpoint with a 3 second timeout. After success, it sets a signed `p2pstream_waf_<rule_id>` pass cookie and redirects with `303 See Other`.

Waiting-room state is in memory. Admission and queue identity are stored in signed cookies. Valid admission cookies continue to pass after restart until expiry; queue cookies are accepted after restart, but visitors are re-enqueued because FIFO state is not persisted.

Captcha and waiting-room passes only satisfy the matching WAF rule. The request still continues through rate limits, traffic shaping, route resolution, and backend forwarding.

The original request body is never replayed after a captcha challenge or waiting-room admission.

## Examples

Login captcha rule:

```text
Action: Captcha
Host pattern: app.example.com
Path prefix: /login
Methods: POST
Key: remote IP
Captcha pass TTL: 1800000 ms
```

Automatic waiting room:

```text
Action: Waiting room
Activation mode: Automatic
Host pattern: app.example.com
Minimum request rate: 50 rps
Backend active requests: 100
```

## Related Tasks

- [WAF](../concepts/waf)
- [Security hardening](../operations/security-hardening)
- [Troubleshooting WAF behavior](../operations/troubleshooting#waf-blocks-challenges-or-queues-unexpectedly)

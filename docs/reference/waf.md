# WAF Reference

WAF rules are global public proxy rules. They are evaluated before rate limits, traffic shapers, route resolution, and backend forwarding.

Reserved WAF endpoints:

| Path | Use |
| --- | --- |
| `/.p2pstream/waf/captcha/verify` | Captcha form verification. |
| `/.p2pstream/waf/waiting-room` | Waiting-room page endpoint. |
| `/.p2pstream/waf/waiting-room/status` | Waiting-room status and admission check. |

ACME HTTP challenges bypass the WAF before these reserved endpoints are handled.

## Captcha Providers

Create captcha providers under **Traffic Policy -> WAF**.

| Provider | Upstream setup | p2pstream fields |
| --- | --- | --- |
| Cloudflare Turnstile | Create a Turnstile widget for the protected hostnames in Cloudflare. | Provider `Turnstile`, site key, secret key, enabled flag. |
| hCaptcha | Create an hCaptcha site and add the protected hostnames. | Provider `hCaptcha`, site key, secret key, enabled flag. |
| Google reCAPTCHA v2 | Create a reCAPTCHA v2 checkbox site key for the protected hostnames. | Provider `reCAPTCHA v2`, site key, secret key, enabled flag. |

Provider secret keys are required, stored server-side, and not sent back to the UI after creation. Captcha rules require an enabled provider.

p2pstream verifies captcha tokens against the provider `siteverify` endpoint with a 3 second timeout. After a successful verification, p2pstream sets a signed `p2pstream_waf_<rule_id>` pass cookie and redirects with `303 See Other`.

The original request body is never replayed after a captcha challenge. A challenged `POST`, upload, or form submission must be submitted again by the client after the pass cookie is set.

## WAF Rules

Rules are ordered by priority, then ID. The first enabled matching rule wins.

| Setting | Default or limit |
| --- | --- |
| Name | `waf-rule` when empty |
| Action | Block |
| Activation mode | Always |
| Captcha pass TTL | `1800000` ms, 30 minutes |
| Captcha pass TTL range | 1 minute to 24 hours |
| Default key | remote IP |
| Block status | `403` |
| Block content type | `text/plain; charset=utf-8` |
| Block body | `Request blocked\n` |
| Block body limit | 64 KiB |

WAF match fields reuse the same matcher model as rate limits:

- methods,
- protocols,
- host patterns,
- path prefixes,
- headers,
- cookies,
- query parameters.

WAF key parts reuse the rate-limit key sources:

- remote IP,
- host,
- method,
- path,
- protocol,
- header,
- cookie,
- query parameter.

Key parts identify a visitor or policy bucket. If no key parts are configured, remote IP is used.

## Actions

| Action | Behavior |
| --- | --- |
| Block | Returns the configured response status, content type, body, and response headers. |
| Captcha | Shows a provider challenge page unless the visitor has a valid signed pass cookie. |
| Waiting room | Queues visitors and admits sessions gradually unless the visitor has a valid signed admission cookie. |

## Waiting Room

Waiting-room state is in memory. Admission and queue identity are stored in signed cookies so valid admitted sessions continue to pass after a restart. Queue cookies are accepted after restart, but visitors are re-enqueued because the FIFO queue is not persisted.

| Setting | Default | Range |
| --- | --- | --- |
| Max admitted sessions | `50` | 1 to 1,000,000 |
| Admission rate | `10/sec` | 1 to 100,000/sec |
| Admission session TTL | `600000` ms, 10 minutes | 1 minute to 24 hours |
| Queue poll interval | `5000` ms, 5 seconds | 1 to 60 seconds |
| Queue timeout | `1800000` ms, 30 minutes | 1 minute to 24 hours |
| Page title | `Waiting room` | non-empty custom text |
| Page body | `Traffic is high. You will be admitted automatically.` | non-empty custom text |

Visitors with an admission cookie pass through. New visitors receive a signed queue cookie and enter FIFO order. When capacity and admission-rate tokens allow, p2pstream sets an admission cookie and redirects with `303 See Other`; otherwise it returns a branded `503` waiting-room page with `Retry-After`.

The original request body is never replayed after waiting-room admission.

## Challenge And Queue Passes

Captcha pass cookies and waiting-room admission cookies only satisfy the matching WAF rule. After a visitor passes a challenge or is admitted from the waiting room, the request still continues through rate limits, traffic shaping, route resolution, and backend forwarding.

Captcha and waiting-room pages are p2pstream-branded interstitial pages with browser, p2pstream, and destination diagnostics plus a per-response reference ID. They do not include provider secrets or signed cookie values.

## Automatic Activation

Waiting-room rules can use `always` mode or `automatic` mode. Automatic mode activates only after one or more configured pressure signals trip for the minimum active duration, then deactivates after the quiet period.

| Signal | Default | Notes |
| --- | --- | --- |
| Request window | `10000` ms | Window used to calculate request rate. |
| Minimum request rate | `50` rps | `0` disables this signal. |
| Traffic spike multiplier | `4` | Compares current rate against the learned baseline. `0` disables this signal. |
| Proxy active requests | `100` | Current active public proxy requests. `0` disables this signal. |
| Backend active requests | `100` | Highest active request count across public backends. `0` disables this signal. |
| Agent active requests | `50` | Highest active agent request count. `0` disables this signal. |
| Server CPU | `85%` | Linux `/proc` process CPU sampling. `0` disables this signal. |
| Agent CPU | `85%` | Requires agents that report `cpu_percent`. `0` disables this signal. |
| Minimum active duration | `30000` ms | Pressure must last this long before activation. |
| Quiet period | `60000` ms | Pressure must stay quiet this long before deactivation. |

Validation ranges:

| Field | Range |
| --- | --- |
| Request window | 1 second to 5 minutes |
| Threshold counts | non-negative |
| Traffic spike multiplier | 0 to 100 |
| CPU percentages | 0 to 100 |
| Minimum active duration | 0 to 24 hours |
| Quiet period | 0 to 24 hours |

Server CPU triggers are best-effort and Linux-first. Unsupported platforms ignore server CPU pressure. Agent CPU pressure is available only when connected agents are new enough to send CPU stats.

## Operational Limits

The WAF protects at the HTTP application layer. It can block, challenge, or queue requests that reach p2pstream, but it does not absorb volumetric DDoS traffic. Use hosting-provider, CDN, firewall, or network DDoS protection for saturated links, packet floods, SYN floods, and other L3/L4 attacks.

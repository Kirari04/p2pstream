# WAF

The WAF is a global public proxy policy layer. It runs after ACME HTTP challenge handling and reserved WAF endpoints, then before rate limits, traffic shaping, route resolution, and forwarding.

Evaluation order:

1. ACME HTTP challenge bypass
2. WAF reserved endpoints
3. WAF rules
4. Rate limits
5. Traffic shaper
6. Route and backend selection
7. Forward, redirect, or static response

Rules are ordered by priority, then ID. The first enabled matching rule wins.

## Actions

| Action | Behavior |
| --- | --- |
| Block | Returns the configured status, content type, body, and headers. |
| Captcha | Shows a challenge page and sets a signed pass cookie after verification. |
| Waiting room | Queues visitors and admits sessions gradually with signed cookies. |

Passing a captcha or waiting-room admission does not bypass later policy layers. The request still goes through rate limits, traffic shaping, route resolution, and backend forwarding.

WAF match fields reuse the same policy matcher model as rate limits: method, protocol, host pattern, path prefix, headers, cookies, and query parameters. Key parts identify a visitor or policy bucket, defaulting to remote IP.

## Captcha providers

p2pstream supports:

- Cloudflare Turnstile,
- hCaptcha,
- Google reCAPTCHA v2 checkbox.

Create the provider in the upstream service, then add the site key and secret key under **Traffic Policy -> WAF**. Captcha verification is performed by p2pstream against the provider `siteverify` endpoint with a 3 second timeout.

On success, p2pstream sets a signed `p2pstream_waf_<rule_id>` pass cookie and redirects with `303 See Other`. Request bodies are not replayed, so a challenged `POST` must be submitted again by the client.

## Waiting room

Waiting room rules keep queue state in memory and use signed cookies for queue and admission state.

Defaults:

| Setting | Default |
| --- | --- |
| Max admitted sessions | `50` |
| Admission rate | `10/sec` |
| Admission session TTL | `10 minutes` |
| Queue poll interval | `5 seconds` |
| Queue timeout | `30 minutes` |

Visitors with a valid admission cookie pass through. A new visitor receives a signed queue cookie and enters FIFO order. When capacity and rate allow, p2pstream sets an admission cookie and redirects with `303 See Other`; otherwise it returns a branded `503` waiting-room page with `Retry-After`.

After restart, existing admission cookies remain valid until expiry. Queue cookies are accepted and re-enqueued because the queue itself is in memory.

## Automatic activation

Waiting-room rules can run in always-on mode or automatic mode. Automatic mode activates when configured pressure signals trip:

- request rate over a window,
- traffic spike versus baseline,
- proxy active requests,
- backend active requests,
- agent active requests,
- server CPU,
- agent CPU.

A threshold of `0` disables that signal. CPU triggers are best-effort: server CPU is sampled from Linux `/proc`, and unsupported platforms ignore server CPU triggers. Agent CPU requires agents new enough to report `cpu_percent`.

## Limits

The WAF is application-layer protection. It can reduce backend load and queue or challenge HTTP clients that reach p2pstream, but it is not a volumetric DDoS scrubber. Network saturation, SYN floods, and large L3/L4 attacks still require upstream protection from a hosting provider, CDN, firewall, or DDoS mitigation service.

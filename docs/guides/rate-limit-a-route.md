# Rate Limit a Route

Use rate limits to slow repeated requests before they reach the upstream.

Example: limit login attempts for one host and path.

## 1. Open rate limits

Open **Management -> Traffic Policy -> Rate Limits** and create a rule.

## 2. Configure the match

| Field | Value |
| --- | --- |
| Name | `login-limit` |
| Priority | `10` |
| Enabled | On |
| Methods | `POST` |
| Protocols | HTTPS |
| Host patterns | `app.example.com` |
| Path prefixes | `/login` |

## 3. Configure the algorithm

For login protection:

| Field | Value |
| --- | --- |
| Algorithm | Sliding window |
| Limit | `10` |
| Window | `60000` ms |
| Burst | `0` |

For APIs that should allow short bursts, use token bucket:

| Field | Value |
| --- | --- |
| Algorithm | Token bucket |
| Limit | `120` |
| Window | `60000` ms |
| Burst | `240` |

## 4. Configure the key

Default key is remote IP. Add key parts when you need a more specific budget:

- remote IP + host,
- remote IP + path,
- header `Authorization` for authenticated API clients,
- cookie or query parameter for known client identifiers.

::: warning Behind another reverse proxy
If all traffic arrives from one proxy IP, a remote-IP-only key can rate-limit every user together.
:::

## 5. Response

Use:

| Field | Value |
| --- | --- |
| Status | `429` |
| Content-Type | `text/plain; charset=utf-8` |
| Body | `Rate limit exceeded` |

Test with repeated requests and watch **Overview -> Problem Signals**.

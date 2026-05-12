# Redirects and Static Responses

Use redirects for host/path migrations. Use static responses for maintenance pages, probes, and deliberate blocks.

## Redirect a host

Open **Management -> Routes** and create:

| Field | Value |
| --- | --- |
| Listener | `public-https` |
| Priority | `10` |
| Host pattern | `old.example.com` |
| Path prefix | `/` |
| Action | Redirect |
| Redirect mode | External origin keep path |
| Redirect target | `https://new.example.com` |
| Status | `308` |
| Preserve query | On |

This sends:

```text
https://old.example.com/docs?a=1 -> https://new.example.com/docs?a=1
```

## Redirect a path on the same host

Use same-host path mode:

| Field | Value |
| --- | --- |
| Host pattern | `app.example.com` |
| Path prefix | `/old` |
| Redirect mode | Same host path |
| Redirect target | `/new` |
| Status | `302` |

## Serve a static maintenance response

Open **Management -> Backends** and create:

| Field | Value |
| --- | --- |
| Name | `maintenance` |
| Type | Static |
| Status code | `503` |
| Response body | `Maintenance in progress` |
| Header | `Retry-After: 300` |

Then create a route with a lower priority number than the normal application route:

| Field | Value |
| --- | --- |
| Priority | `1` |
| Host pattern | `app.example.com` |
| Path prefix | `/` |
| Backend | `maintenance` |

Disable or delete the route when maintenance is over.

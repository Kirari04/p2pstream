# Publish a Service with a Direct Backend

Use this when the upstream service is reachable from the p2pstream server.

Example target:

```text
https://app.internal:8443
```

Public URL:

```text
https://app.example.com
```

## 1. Create or update the backend

In the management UI, open **Proxy -> Backends**.

Create a backend:

| Field | Value |
| --- | --- |
| Name | `app` |
| Type | Proxy forward |
| Forward mode | Direct |
| Target origin | `https://app.internal:8443` |
| TLS skip verify | Off unless the upstream has a broken internal certificate |
| Enabled | On |

<figure class="doc-screenshot">
  <img src="../assets/management_1.png" alt="p2pstream Proxy page showing listener and backend entries">
  <figcaption>Listeners accept public traffic; backends define where matching requests are sent.</figcaption>
</figure>

If the upstream needs a header, add it under upstream request headers. If it needs HTTP basic auth, configure upstream basic auth instead of manually adding `Authorization`.

## 2. Create an HTTPS listener

Open **Proxy -> Listeners**.

Use the seeded `public-https` listener or create one. If you keep the seeded listener, change its default backend from the welcome page to your app:

| Field | Value |
| --- | --- |
| Name | `public-https` |
| Protocol | HTTPS |
| Bind address | empty |
| Port | `443` |
| Default backend | `app` |
| Enabled | On |

For Docker, make sure `443:443` is published.

## 3. Add a route

Open **Proxy -> Routes**.

Create a route:

| Field | Value |
| --- | --- |
| Listener | `public-https` |
| Priority | `10` |
| Host pattern | `app.example.com` |
| Path prefix | `/` |
| Action | Forward |
| Backend | `app` |
| Enabled | On |

<figure class="doc-screenshot">
  <img src="../assets/management_2.png" alt="p2pstream Traffic Policy and TLS pages showing WAF rules, rate limits, traffic shaper, routes, and TLS certificates">
  <figcaption>Routes are configured under Proxy, while TLS mappings, WAF rules, rate limits, cache rules, and traffic shapers live on focused TLS and Traffic Policy pages.</figcaption>
</figure>

## 4. Configure public TLS

Open **TLS** and add a certificate mapping for `app.example.com`.

For public deployments, use ACME:

- HTTP-01 if port `80` reaches p2pstream,
- TLS-ALPN-01 if port `443` reaches p2pstream,
- DNS-01 for wildcard certificates or when inbound validation ports are not available.

## 5. Test

```bash
curl -I https://app.example.com
```

Check **Overview** for request count and status class. If the request fails, open **Traffic**, enable tracing, and retry the request.

# Issue ACME Certificates with HTTP-01 or TLS-ALPN-01

Use HTTP-01 or TLS-ALPN-01 when the public DNS hostname points directly to p2pstream and inbound validation ports are available.

## Requirements

| Challenge | Required public reachability |
| --- | --- |
| HTTP-01 | `http://hostname/.well-known/acme-challenge/...` reaches a p2pstream HTTP listener. |
| TLS-ALPN-01 | `https://hostname:443` reaches a p2pstream HTTPS listener. |

The hostname must be public DNS, not `localhost`, an IP address, or an internal-only name.

## 1. Create the listener

Open **Management -> Listeners**.

For HTTP-01, ensure an HTTP listener is enabled on port `80`.

For TLS-ALPN-01, ensure an HTTPS listener is enabled on port `443`.

## 2. Add the certificate mapping

Open **Management -> TLS** and add:

| Field | Value |
| --- | --- |
| Listener | `public-https` |
| Hostname pattern | `app.example.com` |
| Method | `HTTP-01` or `TLS-ALPN` |
| CA | Let's Encrypt staging for testing, production when ready |
| Email | your ACME account email |
| Enabled | On |

## 3. Verify issuance

The certificate status should move from pending/renewing to ready.

Test:

```bash
curl -I https://app.example.com
```

If issuance fails, check:

- DNS points to the p2pstream host,
- ports `80` and/or `443` are open through firewall and NAT,
- the listener is enabled and running,
- the hostname pattern matches the requested name.

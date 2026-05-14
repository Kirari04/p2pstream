# Ports Reference

| Port | Default use |
| --- | --- |
| `80/tcp` | Public HTTP listener. Also used for ACME HTTP-01. |
| `443/tcp` | Public HTTPS listener. Also used for ACME TLS-ALPN-01. |
| `8081/tcp` | Management UI/API and agent HTTPS/WSS connections. The browser UI can be disabled with `MANAGEMENT_UI_DISABLED=true`. |

## Docker publishing

The runtime image exposes `80`, `443`, and `8081`, but Docker only publishes what you map.

```yaml
ports:
  - "80:80"
  - "443:443"
  - "8081:8081"
```

If you add a listener on `8088`, add:

```yaml
ports:
  - "8088:8088"
```

## Management URL

`MANAGEMENT_PUBLIC_URL` should include the externally reachable management scheme, host, and port:

```text
MANAGEMENT_PUBLIC_URL=https://proxy.example.com:8081
```

Agents use this URL to connect to management.

# Ports Reference

p2pstream uses separate ports for public listeners and management.

## Exact Fields And Defaults

| Port | Default use |
| --- | --- |
| `80/tcp` | Public HTTP listener and ACME HTTP-01. |
| `443/tcp` | Public HTTPS listener and ACME TLS-ALPN-01. |
| `8081/tcp` | Management UI/API and agent HTTPS/WSS connections. |

The default Docker Compose mapping is:

```yaml
ports:
  - "80:80"
  - "443:443"
  - "8081:8081"
```

## Validation Rules

- Listener ports must be between `1` and `65535`.
- Docker only publishes mapped ports.
- `MANAGEMENT_PUBLIC_URL` should include the externally reachable management scheme, host, and port.

## Runtime Effects

If you add a listener on `8088`, add a Docker port mapping before clients can reach it:

```yaml
ports:
  - "8088:8088"
```

Agents use management, not public listener ports:

```text
MANAGEMENT_PUBLIC_URL=https://proxy.example.com:8081
```

## Examples

Publish container port `443` on host port `8443`:

```dotenv
P2PSTREAM_HTTPS_PORT=8443
```

Then public users reach:

```text
https://proxy.example.com:8443
```

## Related Tasks

- [Docker Compose details](../getting-started/docker-compose)
- [Listeners](../concepts/listeners)
- [ACME HTTP/TLS-ALPN](../guides/acme-http-tls-alpn)

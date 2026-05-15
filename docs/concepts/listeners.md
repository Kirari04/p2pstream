# Listeners

A listener is the public TCP bind and protocol where user traffic enters p2pstream.

## What It Is

Listeners belong to the public proxy runtime, not the management server. Each listener has a protocol, bind address, port, enabled flag, and default backend.

| Protocol | Behavior |
| --- | --- |
| HTTP | Plain HTTP listener. Also serves ACME HTTP-01 challenges. |
| HTTPS | TLS listener with SNI certificate selection. Also supports ACME TLS-ALPN-01 challenges. |

## When It Matters

Listeners matter when publishing apps, issuing ACME certificates, changing ports, binding only to loopback, or running behind Docker/NAT/firewall rules.

## Runtime Behavior

An empty bind address means all interfaces. Ports must be between `1` and `65535`.

| Bind address | Port | Meaning |
| --- | --- | --- |
| empty | `80` | Listen on all interfaces inside the container or host. |
| `127.0.0.1` | `8080` | Listen only on loopback. |
| `192.0.2.10` | `443` | Listen only on that host address. |

Enabled means the listener is part of desired configuration. Running means a server socket is currently active. Disabling a listener stops its runtime. Deleting a listener requires it to be stopped or disabled first.

On an empty database, p2pstream creates:

| Listener | Protocol | Port |
| --- | --- | --- |
| `public-http` | HTTP | `80` |
| `public-https` | HTTPS | `443` |

Both use catch-all routes to the seeded `default` static welcome backend until you replace the backend or add more specific routes.

## Common Mistakes

- Creating a listener in the UI but not publishing the container port in Compose.
- Binding to an address that does not exist on the host/container.
- Using HTTP-01 without a reachable HTTP listener on port `80`.
- Using TLS-ALPN-01 without a reachable HTTPS listener on port `443`.

## Related Links

- [Publish a service](../guides/publish-a-service)
- [Ports reference](../reference/ports)
- [ACME HTTP/TLS-ALPN](../guides/acme-http-tls-alpn)

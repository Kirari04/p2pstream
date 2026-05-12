# Listeners

A listener is a TCP bind plus a protocol. Public requests enter p2pstream through listeners.

## Protocols

| Protocol | Behavior |
| --- | --- |
| HTTP | Plain HTTP listener. Also serves ACME HTTP-01 challenges when an ACME certificate is being issued. |
| HTTPS | TLS listener with SNI certificate selection. Also supports ACME TLS-ALPN-01 challenges. |

## Bind address and port

An empty bind address means all interfaces. For Docker, the container can listen on a port that is not exposed to the host until you publish it.

Examples:

| Bind address | Port | Meaning |
| --- | --- | --- |
| empty | `80` | Listen on all interfaces inside the container or host. |
| `127.0.0.1` | `8080` | Listen only on loopback. |
| `192.0.2.10` | `443` | Listen only on that host address. |

Ports must be between `1` and `65535`.

## Enabled vs running

Enabled means the listener is part of the desired configuration. Running means a server socket is currently active.

Disabling a listener stops its runtime. Deleting a listener requires it to be stopped or disabled first.

## Defaults

On an empty database, p2pstream creates:

| Listener | Protocol | Port |
| --- | --- | --- |
| `public-http` | HTTP | `80` |
| `public-https` | HTTPS | `443` |

Both use the seeded `default` backend until you replace it.

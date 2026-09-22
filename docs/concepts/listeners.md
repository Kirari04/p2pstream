# Listeners

A listener is the public TCP bind and protocol where user traffic enters p2pstream.

## What It Is

Listeners belong to the public proxy runtime, not the management server. Each listener has a protocol, bind address, port, and enabled flag. Assign Sites to a listener to serve their hostnames and shared routes. An optional Default Site handles unmatched hostnames.

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

Manage listeners under **Proxy → Listeners**. The searchable table keeps protocol, route count, bind address, and runtime state on one row. The common **Stop** or **Start** and **Edit** actions remain visible; less frequent lifecycle actions are grouped under **More**.

On an empty database, p2pstream creates:

| Listener | Protocol | Port |
| --- | --- | --- |
| `public-http` | HTTP | `80` |
| `public-https` | HTTPS | `443` |

Both serve one shared, published Default Site with a default route and static welcome target. Edit its routes under **Proxy → Sites**, or create a named Site for your application.

Deleting a stopped listener removes its Site assignments while preserving the Sites and their routes. A Site with no remaining assignments serves no traffic until it is assigned again. Change any Site redirects that use this listener as their HTTPS destination before deleting it.

<figure class="doc-screenshot">
  <img src="../assets/new/proxy_listeners.png" alt="p2pstream Proxy Listeners table showing searchable HTTP and HTTPS listeners with bind addresses, route counts, runtime state, and row actions">
  <figcaption>The compact Listeners table separates desired configuration from runtime state while keeping common runtime and edit actions on each row.</figcaption>
</figure>

<figure class="doc-screenshot">
  <img src="../assets/new/proxy_edit_interface_listener_modal.png" alt="p2pstream Edit Listener drawer showing protocol, bind address, port, and enabled state">
  <figcaption>The listener drawer controls the public bind. Assign a Default Site for unmatched hostnames; container port publishing and host firewall rules still need to expose the same port outside p2pstream.</figcaption>
</figure>

## Common Mistakes

- Creating a listener in the UI but not publishing the container port in Compose.
- Binding to an address that does not exist on the host/container.
- Using HTTP-01 without a reachable HTTP listener on port `80`.
- Using TLS-ALPN-01 without a reachable HTTPS listener on port `443`.

## Related Links

- [Sites](../reference/sites)
- [Publish a service](../guides/publish-a-service)
- [Ports reference](../reference/ports)
- [ACME HTTP/TLS-ALPN](../guides/acme-http-tls-alpn)

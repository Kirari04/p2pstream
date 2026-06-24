# Management TLS Reference

Management TLS protects the management UI/API, agent stats calls, and the authenticated Yamux agent tunnel.

## Exact Fields And Defaults

| Mode | Required variables | Behavior |
| --- | --- | --- |
| `auto` | none | Generate local CA and server cert if no cert/key are provided. |
| `provided` | `MANAGEMENT_TLS_CERT_FILE`, `MANAGEMENT_TLS_KEY_FILE` | Use provided certificate material. |
| `off` | `MANAGEMENT_ALLOW_INSECURE_HTTP=true` | Serve management over HTTP. |

`MANAGEMENT_TLS_MODE` defaults to `auto`.

Auto mode writes:

```text
${CONFIG_DIR}/certs/management/ca.crt.pem
${CONFIG_DIR}/certs/management/ca.key.pem
${CONFIG_DIR}/certs/management/server.crt.pem
${CONFIG_DIR}/certs/management/server.key.pem
```

The generated CA and server certificate are valid for 10 years. Certificate files remain plaintext PEM. Auto-generated private-key files are encrypted at rest when stored-secret encryption is configured; provided-mode key files remain operator-owned and are not rewritten. The server certificate is regenerated if the hostname set no longer matches.

## Validation Rules

- Cert and key files must be set together.
- Provided mode requires cert and key files.
- Off mode requires `MANAGEMENT_ALLOW_INSECURE_HTTP=true`.
- `MANAGEMENT_TLS_CLIENT_CA_FILE` requires TLS.
- `MANAGEMENT_PUBLIC_URL` must use `https` unless management TLS is off and insecure HTTP is explicitly allowed.

## Runtime Effects

Auto mode certificate names include the detected advertise host, `localhost`, `p2pstream.local`, `server`, `127.0.0.1`, `::1`, and entries from `MANAGEMENT_TLS_EXTRA_HOSTS`.

Agents verify management TLS. Pass either:

```text
MANAGEMENT_CA_FILE=/etc/p2pstream/management-ca.pem
```

or:

```text
MANAGEMENT_CA_PEM_BASE64=...
```

For agent mTLS, set `MANAGEMENT_TLS_CLIENT_CA_FILE` on the server and configure agents with:

```text
AGENT_TLS_CERT_FILE=/etc/p2pstream/agent.crt.pem
AGENT_TLS_KEY_FILE=/etc/p2pstream/agent.key.pem
```

## Examples

Auto TLS with extra names:

```dotenv
MANAGEMENT_PUBLIC_URL=https://proxy.example.com:8081
MANAGEMENT_TLS_EXTRA_HOSTS=proxy.example.com,192.0.2.10
```

Provided TLS:

```dotenv
MANAGEMENT_TLS_MODE=provided
MANAGEMENT_TLS_CERT_FILE=/etc/p2pstream/management.crt.pem
MANAGEMENT_TLS_KEY_FILE=/etc/p2pstream/management.key.pem
```

## Related Tasks

- [TLS](../concepts/tls)
- [Security hardening](../operations/security-hardening)
- [Expose a home lab app](../guides/expose-a-home-lab-app)

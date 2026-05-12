# Management TLS Reference

Management TLS protects the management UI/API and agent connections.

## Modes

| Mode | Required variables | Behavior |
| --- | --- | --- |
| `auto` | none | Generate local CA and server cert if no cert/key are provided. |
| `provided` | `MANAGEMENT_TLS_CERT_FILE`, `MANAGEMENT_TLS_KEY_FILE` | Use provided certificate material. |
| `off` | `MANAGEMENT_ALLOW_INSECURE_HTTP=true` | Serve management over HTTP. |

`MANAGEMENT_TLS_MODE` defaults to `auto`.

## Auto-generated files

Auto mode writes:

```text
${CONFIG_DIR}/certs/management/ca.crt.pem
${CONFIG_DIR}/certs/management/ca.key.pem
${CONFIG_DIR}/certs/management/server.crt.pem
${CONFIG_DIR}/certs/management/server.key.pem
```

The CA and server certificate are valid for 10 years. The server certificate is regenerated if the hostnames no longer match.

## Certificate names

Auto mode includes:

- detected advertise host,
- `localhost`,
- `p2pstream.local`,
- `server`,
- `127.0.0.1`,
- `::1`,
- entries from `MANAGEMENT_TLS_EXTRA_HOSTS`.

Set `MANAGEMENT_PUBLIC_URL` to override the generated default management URL used by setup snippets.

## Agent trust

Agents verify management TLS. Pass either:

```text
MANAGEMENT_CA_FILE=/etc/p2pstream/management-ca.pem
```

or:

```text
MANAGEMENT_CA_PEM_BASE64=...
```

## Agent mTLS

Set `MANAGEMENT_TLS_CLIENT_CA_FILE` on the server to verify optional agent client certificates. Agents then need:

```text
AGENT_TLS_CERT_FILE=/etc/p2pstream/agent.crt.pem
AGENT_TLS_KEY_FILE=/etc/p2pstream/agent.key.pem
```

The certificate must identify the correct agent ID.

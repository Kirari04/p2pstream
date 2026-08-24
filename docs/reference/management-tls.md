# Management TLS Reference

Management TLS protects the management UI/API, agent stats calls, and the authenticated Yamux agent tunnel.

## Exact Fields And Defaults

| Mode | Required variables | Behavior |
| --- | --- | --- |
| `auto` | none | Generate local CA and server cert if no cert/key are provided. |
| `provided` | `MANAGEMENT_TLS_CERT_FILE`, `MANAGEMENT_TLS_KEY_FILE` | Use provided certificate material. |
| `off` | `MANAGEMENT_ALLOW_INSECURE_HTTP=true` | Serve management over HTTP. |

`MANAGEMENT_TLS_MODE` defaults to `auto`.

Neutral generated certificate subjects reduce needless passive product fingerprinting, but they are defense in depth rather than an authentication boundary. Keep management access authenticated and restrict network exposure where practical.

Auto mode writes:

```text
${CONFIG_DIR}/certs/management/ca.crt.pem
${CONFIG_DIR}/certs/management/ca.key.pem
${CONFIG_DIR}/certs/management/server.crt.pem
${CONFIG_DIR}/certs/management/server.key.pem
```

The generated CA and server certificate are valid for 10 years. The server certificate is regenerated if the hostname set no longer matches.

## Validation Rules

- Cert and key files must be set together.
- Provided mode requires cert and key files.
- Off mode requires `MANAGEMENT_ALLOW_INSECURE_HTTP=true`.
- `MANAGEMENT_TLS_CLIENT_CA_FILE` requires TLS.
- `MANAGEMENT_PUBLIC_URL` must use `https` unless management TLS is off and insecure HTTP is explicitly allowed.

## Runtime Effects

Auto mode certificate names include the detected advertise host, `localhost`, `server`, `127.0.0.1`, `::1`, and entries from `MANAGEMENT_TLS_EXTRA_HOSTS`. Generated certificate subjects use neutral management labels and do not include the product name. Set `MANAGEMENT_ADVERTISE_HOST` or `MANAGEMENT_TLS_EXTRA_HOSTS` for any additional name agents actually use.

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

## Certificate Rotation

Open **Settings → Management TLS** to regenerate the auto certificate or stage a custom server certificate, private key, and CA bundle. The management UI, API, agent stats endpoint, and agent tunnel continue to share this one certificate.

A CA change uses a two-step trust rollout:

1. The server distributes a dual old/new CA bundle and the public staged certificate. Agents validate the hostname and chain, atomically persist the bundle, reload it, and report the generation and digest after readback.
2. Activation is allowed when every enabled agent has acknowledged. Offline, incompatible, timed-out, and failed agents block normal activation; disabled agents are listed but excluded. Forced activation is available with an explicit warning and marks affected agents as stranded.

After activation, rollback remains available while agents trust both CAs. **Retire old trust** starts a second acknowledged rollout containing only the active CA. Finalizing that rollout removes the rollback window.

Cancelling a staged CA change or rolling an activated certificate back does not silently leave the abandoned CA behind. Both actions enter **Trust cleanup**, distribute the currently active CA as the sole managed trust anchor, and wait for every compatible participating agent to acknowledge durable cleanup. Older agents that cannot install managed trust could not have installed the abandoned bundle and therefore do not block cleanup. Cleanup can be forced when necessary, but unresolved agents remain visible as attention items and are reconciled whenever they report again.

The server retains the desired active trust digest after a rotation returns to idle. This keeps offline and disabled agents auditable instead of treating the whole fleet as implicitly ready. A disabled stale agent remains excluded from rollout gates, but must be repaired or reinstalled before it is enabled if it cannot reconnect with the active certificate.

Generated Linux, Docker Compose, and direct CLI setup forms configure a writable `MANAGEMENT_TRUST_FILE` used for durable updates; Docker uses a named state volume. If an agent cannot reconnect after a forced change, use the repair command shown on the rotation page. An old or damaged agent should instead run its full install command again. The agent identity and token remain the recovery authority; private server keys are never sent to agents.

Replacing only the server leaf certificate under the same CA does not require an agent trust-anchor change and can activate without waiting for fleet trust acknowledgements.

Certificate and key files created by the rotation manager are written atomically. Cancelled staged keys and retired rotation-managed keys are removed after the new state is durably committed; cleanup is retried and surfaced in the UI if the filesystem refuses deletion. Original operator-provided certificate paths are never deleted automatically.

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

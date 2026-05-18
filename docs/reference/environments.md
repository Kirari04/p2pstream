# Environments Reference

Environments let one p2pstream management console operate other p2pstream instances. Each remote environment is an HTTPS management URL plus an admin access token and a pinned certificate trust decision.

The local instance is always available as the virtual **Local** environment. Remote environments are stored only on the control-plane instance where they are created.

## Management Access Tokens

Create management access tokens from **Environments -> Access Tokens** on the target instance. Tokens are shown once when created, start with `p2pat_`, and grant admin management API access.

| Field | Behavior |
| --- | --- |
| Name | Required and unique. |
| Expiry | Optional. Empty means the token never expires. |
| Enabled | Disabled tokens cannot authenticate. |
| Last used | Updated after successful bearer authentication. |

Expired, disabled, deleted, and malformed tokens are rejected. Deleting a token revokes it immediately.

## Register A Direct Environment

Use direct transport when the control-plane server can reach the target management URL itself.

1. On the target p2pstream instance, create an admin access token.
2. On the control-plane instance, open **Environments** and add an environment.
3. Enter a unique name and an absolute `https://` management URL with no fragment.
4. Select **Direct** transport.
5. Paste the target access token.
6. Save the environment.
7. Run certificate discovery, review the certificate, and trust it.
8. Test the environment, then select it from the header switcher.

The URL is normalized without a trailing slash. HTTP URLs are not accepted for environments.

## Register An Agent-Routed Environment

Use agent transport when the control-plane server cannot reach the target directly, but a connected local agent can.

1. Create or connect an agent on the control-plane instance.
2. On the target instance, create an admin access token.
3. Add an environment and select **Agent** transport.
4. Pick the connected local agent that can reach the target management URL.
5. Paste the target access token.
6. Save, discover the certificate through the agent, review it, and trust it.

Agent-routed management requests include per-request certificate trust metadata. They do not use TLS skip verification.

## Certificate Trust

Remote environments use explicit trust-on-first-use.

1. Discovery opens a TLS handshake to the target and collects the peer certificate.
2. Discovery does not send the access token and does not make a management RPC.
3. The UI shows the fingerprint, subject, issuer, SANs, and validity dates.
4. An admin explicitly trusts the certificate.
5. Future remote management requests verify the target against the saved certificate and hostname.

Trust states:

| State | Meaning |
| --- | --- |
| `UNTRUSTED` | No certificate has been saved. Remote management is blocked. |
| `TRUSTED` | The observed certificate matches the saved certificate and is valid for the hostname and time. |
| `CHANGED` | The observed certificate fingerprint differs from the saved fingerprint. Remote management is blocked until re-trusted. |
| `EXPIRED` | The saved or observed certificate is past `NotAfter`. Remote management is blocked. |

For certificate rotation, rediscover the certificate, confirm the new fingerprint and identity details, then trust the replacement certificate. Normal remote operations remain blocked while the environment is changed or expired.

## Operational Behavior

The header environment switcher controls operational views such as overview, proxy configuration, traffic tracing, agents, TLS, templates, WAF, rate limits, shaping, and cache. Setup, login, logout, current user, and environment registry operations always stay local.

When switching environments, traffic tracing reconnects to the selected environment and clears retained trace state.

## Security Notes

- Environment access tokens grant admin access to the target p2pstream instance.
- Environment access tokens are stored by the control-plane instance because it must replay them to proxy unattended management requests.
- HTTPS is required for every remote environment.
- Environment certificate verification is pinned and hostname-checked, not skipped.
- Certificate discovery never sends access tokens.
- Agent authentication is separate from management access token authentication.

## Related Tasks

- [Management TLS](./management-tls)
- [Agents](../concepts/agents)
- [Security hardening](../operations/security-hardening)

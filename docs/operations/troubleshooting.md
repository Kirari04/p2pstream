# Troubleshooting

Start with logs:

```bash
docker compose logs -f p2pstream
```

For advanced systemd installs:

```bash
sudo journalctl -u p2pstream -f
sudo journalctl -u p2pstream-agent -f
```

## Management UI will not open

| Check | Fix |
| --- | --- |
| Container running | `docker ps` or `systemctl status p2pstream`. |
| Port published | Publish `8081:8081` or use the actual host port. |
| Scheme | Use `https://host:8081` unless management TLS is explicitly off. |
| Firewall | Allow the management port from your admin network. |
| Browser UI disabled | If `MANAGEMENT_UI_DISABLED=true`, the browser UI intentionally returns `404`; ConnectRPC APIs and the agent WebSocket remain available. |

## Browser certificate warning

| Cause | Fix |
| --- | --- |
| Auto-generated management TLS | Trust the generated CA or provide your own certificate. |
| Wrong hostname | Set `MANAGEMENT_PUBLIC_URL` and `MANAGEMENT_TLS_EXTRA_HOSTS`, then restart if needed. |
| Management behind another proxy | Terminate trusted TLS at that proxy or pass the correct public URL to agents. |

## Cannot log in

| Cause | Fix |
| --- | --- |
| Wrong or forgotten password | Reset it with `p2pstream users reset-password USERNAME` on a host or container with access to the same database. |
| Setup window expired and no users exist | Restart the server to reopen the setup window. |
| Reset command used the wrong database | Run it with the same `CONFIG_DIR` as the server or pass `--database-url` for the server's SQLite database. |

## Agent will not connect

| Check | Fix |
| --- | --- |
| `MANAGEMENT_URL` | It must point to management, usually `https://host:8081`. |
| CA trust | Use `MANAGEMENT_CA_FILE` or `MANAGEMENT_CA_PEM_BASE64` for auto TLS. |
| Token | Rotate the token and update the agent env file. |
| Agent ID | Use the generated `agent-...` public ID. |
| Firewall/NAT | Agent host must reach management HTTPS/WSS. |
| Insecure URL | HTTP requires `AGENT_ALLOW_INSECURE_MANAGEMENT=true`, intended for development only. |

## Public listener fails to bind

| Cause | Fix |
| --- | --- |
| Port already used | Stop the other service or choose another listener port. |
| Missing Docker publish | Add `host:container` port mapping and restart the container. |
| Privileged port with non-root user | Run with enough privileges or bind a high port. |
| Bind address not present | Use an empty bind address or a real local address. |

## HTTPS serves fallback/self-signed certificate

| Cause | Fix |
| --- | --- |
| No matching certificate mapping | Add a mapping for the exact host or wildcard. |
| ACME certificate not ready | Check certificate status and last error. |
| Request SNI mismatch | Test with the real hostname, not the IP address. |
| Listener not restarted | Stop/start the listener or wait for automatic restart after certificate issuance. |

## ACME fails

| Check | Fix |
| --- | --- |
| Public DNS | Hostname must resolve to the p2pstream public address. |
| HTTP-01 | Port `80` must reach the HTTP listener. |
| TLS-ALPN-01 | Port `443` must reach the HTTPS listener. |
| DNS-01 | Cloudflare zone ID and API token must be valid and enabled. |
| Wildcard | Use DNS-01; HTTP-01 and TLS-ALPN-01 do not support wildcard issuance. |
| CA | Test with staging before production. |

## Route does not match

| Check | Fix |
| --- | --- |
| Listener | Route must belong to the listener receiving the request. |
| Host pattern | Use exact host or `*.example.com`. |
| Path prefix | Prefix must start with `/`. |
| Priority | Lower numbers win. Put specific routes first. |
| Default backend | If no route matches, the listener default backend handles the request. |

## Backend returns bad gateway

| Cause | Fix |
| --- | --- |
| Direct upstream unreachable | Test connectivity from the p2pstream server/container. |
| Agent upstream unreachable | Test connectivity from the agent host. |
| Agent offline | Reconnect or enable an assigned agent. |
| Upstream TLS error | Fix the upstream certificate; use skip verify only as a temporary internal workaround. |
| Wrong target origin | Include scheme and host, for example `http://app:8080`. |
| Passive health cooldown | If health checks are enabled, recent connect or timeout failures can temporarily remove the backend or selected agent assignment from routing. Wait for recovery, fix the upstream, or adjust health-check settings. |

When health checks are disabled, transient upstream failures fail only the current request and should not cause `no_route_backend_available`.

## Backend returns gateway timeout

| Cause | Fix |
| --- | --- |
| Upstream is slow to send response headers | Increase the backend response-header timeout. The default is `60000` ms. |
| Agent-pool backend waits on a private app | Test the target origin from the selected agent host and raise the backend timeout if the app legitimately takes longer before headers. |
| Health check timeout confusion | Health-check timeout is separate. Raising the backend response-header timeout does not change health-check timing. |
| Old agent binary | Upgrade agents so they honor the per-backend timeout metadata; older agents keep their built-in `30000` ms timeout. |

The backend response-header timeout limits only the wait for first upstream headers. It does not cap the duration of streaming a response after headers are received.

## Rate limits affect every user

| Cause | Fix |
| --- | --- |
| p2pstream sees one proxy IP | Add better key parts or place p2pstream at the edge. |
| Rule too broad | Add host/path/method matchers. |
| Priority conflict | Move specific rules to lower priority numbers. |

## WAF blocks, challenges, or queues unexpectedly

| Cause | Fix |
| --- | --- |
| Rule too broad | Narrow the WAF match by host, path, method, header, cookie, or query parameter. |
| Priority conflict | Lower priority numbers win. Move specific allow-through behavior outside the broad rule's match or adjust priorities. |
| Captcha provider unavailable | Confirm the provider is enabled and the site key/secret key match the upstream provider configuration. |
| Waiting room stays active | Check automatic trigger thresholds, active request counts, server CPU, and agent CPU in the dashboard. Use `0` to disable an automatic signal. |
| All clients share one queue identity | Add key parts that identify visitors better than remote IP when p2pstream is behind another proxy. |
| Large form or upload must be retried | Captcha and waiting-room admission use `303` redirects and do not replay request bodies. Resubmit after admission. |

## Trace stream reconnects

| Cause | Fix |
| --- | --- |
| Management connection interrupted | Check browser network and management logs. |
| Server restarted | Reopen Traffic after restart. |
| Too much trace volume | Use Basic or Detailed level and clear old traces. |
| Auth session expired | Log in again. |

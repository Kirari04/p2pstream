# Architecture

<script setup>
import { withBase } from "vitepress";
</script>

p2pstream is one server process with two network surfaces: public listeners for user traffic, and management for operators plus agents.

<div class="architecture-frame">
  <img :src="withBase('/architecture.svg')" alt="p2pstream architecture diagram">
</div>

## What It Is

The server runs the management UI/API, public proxy runtime, SQLite storage, public policy layers, TLS automation, and optional agent forwarding.

| Component | Role |
| --- | --- |
| Management UI/API | Serves the Vue UI, ConnectRPC API, and authenticated agent Yamux tunnel on `MANAGEMENT_PORT`, default `8081`. |
| Public listeners | Bind configured HTTP/HTTPS ports and receive public user traffic. |
| WAF | Applies ordered block, captcha, and waiting-room rules before rate limits and routing. |
| Router | Selects a route by listener, host, path prefix, and priority. |
| Target executor | Forwards directly, returns static responses, redirects, or sends requests to a label-selected agent. |
| SQLite | Stores users, sessions, agents, public proxy config, TLS metadata, and observability events. |

## When It Matters

Understand the architecture when choosing between direct targets and agent targets, deciding which ports to expose, planning backups, or troubleshooting where a request stopped.

## Runtime Behavior

Direct target flow:

1. A client connects to a public listener.
2. Globally invalid paths, ACME HTTP challenges, and reserved WAF endpoints are handled before normal policy evaluation.
3. p2pstream performs a route-only match to enforce the matched route's path security mode.
4. WAF rules may block, require captcha, or place the visitor in a waiting room.
5. Rate limit rules run before route resolution.
6. A traffic shaper may wrap upload/download body streams.
7. The router selects a route target, or a listener default route target if no explicit route matches.
8. Cache rules can serve eligible proxy assets after route/target selection.
9. The server forwards directly to the upstream origin or returns a redirect/static response.
10. Observability records status, duration, policy IDs, listener/route/target IDs, agent ID, and byte counts.

The early route-only match is used for path security mode, including strict rejection of encoded path separators by default. Target selection and load-balancer state changes still happen later, after WAF, rate limits, and traffic shapers.

Agent target flow:

1. An agent connects outbound to management over management TLS and upgrades `GET /agent/tunnel` with `Upgrade: p2pstream-yamux`.
2. The agent authenticates with its generated agent ID and token.
3. A route target is configured with agent transport and a label selector.
4. For matching requests, the server selects a label-matched connected agent using the target's agent load-balancing policy.
5. The server opens one Yamux stream for the upstream TCP connection.
6. The server-owned HTTP transport runs over that stream; the agent only dials the origin and relays bytes.

## Common Mistakes

- Exposing management `8081` as if it were a public app listener.
- Expecting Docker to publish a new listener port that was only created in the UI.
- Forgetting that agent target origins are resolved from the selected agent host.
- Running old WebSocket agents against a Yamux-tunnel server; agent and server versions must match after this breaking transport change.
- Deleting `/data` and losing SQLite, TLS material, ACME state, and the management CA.

## Related Links

- [Docker Compose quickstart](../getting-started/quickstart)
- [Publish a service](../guides/publish-a-service)
- [Expose a home lab app](../guides/expose-a-home-lab-app)
- [Backup and restore](../operations/backup-restore)

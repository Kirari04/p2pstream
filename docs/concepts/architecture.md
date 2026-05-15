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
| Management UI/API | Serves the Vue UI, ConnectRPC API, and agent WebSocket on `MANAGEMENT_PORT`, default `8081`. |
| Public listeners | Bind configured HTTP/HTTPS ports and receive public user traffic. |
| WAF | Applies ordered block, captcha, and waiting-room rules before rate limits and routing. |
| Router | Selects a route by listener, host, path prefix, and priority. |
| Backend executor | Forwards directly, returns static responses, redirects, or sends requests to an agent. |
| SQLite | Stores users, sessions, agents, public proxy config, TLS metadata, and observability events. |

## When It Matters

Understand the architecture when choosing between direct backends and agents, deciding which ports to expose, planning backups, or troubleshooting where a request stopped.

## Runtime Behavior

Direct backend flow:

1. A client connects to a public listener.
2. ACME HTTP challenges and reserved WAF endpoints are handled before normal policy evaluation.
3. WAF rules may block, require captcha, or place the visitor in a waiting room.
4. Rate limit rules run before route resolution.
5. A traffic shaper may wrap upload/download body streams.
6. The router selects a route or the listener default backend.
7. Cache rules can serve eligible proxy-forward assets after route/backend selection.
8. The server forwards directly to the upstream origin or returns a redirect/static response.
9. Observability records status, duration, policy IDs, listener/backend/route IDs, agent ID, and byte counts.

Agent backend flow:

1. An agent connects outbound to management over HTTPS/WSS.
2. The agent authenticates with its generated agent ID and token.
3. A backend is configured in agent-pool mode with enabled agent assignments.
4. For matching requests, the server selects an agent using the backend load-balancing policy.
5. The request is encoded and sent over the agent WebSocket.
6. The agent connects to the upstream from its own network and streams the response back.

## Common Mistakes

- Exposing management `8081` as if it were a public app listener.
- Expecting Docker to publish a new listener port that was only created in the UI.
- Forgetting that agent-pool target origins are resolved from the agent host.
- Deleting `/data` and losing SQLite, TLS material, ACME state, and the management CA.

## Related Links

- [Docker Compose quickstart](../getting-started/quickstart)
- [Publish a service](../guides/publish-a-service)
- [Expose a home lab app](../guides/expose-a-home-lab-app)
- [Backup and restore](../operations/backup-restore)

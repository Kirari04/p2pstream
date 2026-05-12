# Architecture

<script setup>
import { withBase } from "vitepress";
</script>

p2pstream has two network surfaces:

- public listeners that receive user traffic,
- the management UI/API that operators and agents use.

<div class="architecture-frame">
  <img :src="withBase('/architecture.svg')" alt="p2pstream architecture diagram">
</div>

## Server

The server process runs the management API/UI and the public proxy runtime.

| Component | Role |
| --- | --- |
| Management UI/API | Serves the Vue UI and ConnectRPC API on `MANAGEMENT_PORT`, default `8081`. |
| Public listeners | Bind configured HTTP/HTTPS listener ports and receive public traffic. |
| Router | Selects a route by listener, host, path prefix, and priority. |
| Backend executor | Forwards directly, returns static responses, redirects, or sends requests to an agent. |
| SQLite | Stores users, sessions, agents, public proxy config, TLS metadata, and observability events. |

## Direct backend flow

1. A client connects to a public listener.
2. Rate limit rules run before route resolution.
3. A traffic shaper rule may wrap the upload/download body.
4. The router selects a route or the listener default backend.
5. The server forwards the request from the server host to the upstream origin.
6. Observability records status, duration, listener/backend/route IDs, and byte counts.

Direct mode is simplest when the upstream is reachable from the p2pstream server.

## Agent backend flow

1. An agent connects outbound to management over HTTPS/WSS.
2. The agent authenticates with its generated agent ID and token.
3. A public backend is configured in `agent_pool` mode with one or more enabled agent assignments.
4. For matching requests, the server selects an agent using the backend load balancing policy.
5. The request is encoded and sent over the agent WebSocket.
6. The agent forwards the request from its own network and streams the response back.

Agent mode is useful when the upstream is inside a home lab or private network and cannot accept inbound connections.

## Management TLS

Management HTTPS is enabled by default. In auto mode, p2pstream creates a persisted local CA and server certificate under:

```text
/data/certs/management
```

Agents verify this certificate with `MANAGEMENT_CA_FILE` or `MANAGEMENT_CA_PEM_BASE64`. They do not skip TLS verification by default.

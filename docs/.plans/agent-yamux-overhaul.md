# Agent Yamux Transport Overhaul

## Current State

- Branch: `rewrite/agent-yamux-transport`
- Base commit: `ddb6c09f5eb5aa2e7e7335dd11ff8fccaa0eb0d2`
- Last updated: `2026-06-06T14:41:10+02:00`
- Current phase: Phase 2 hardening complete
- Current blocker: none.

## Decisions

- Use Yamux over the existing management TLS connection.
- Use hard cutover; no WebSocket compatibility layer.
- Use raw TCP streams, not HTTP-object messages.
- Server owns HTTP semantics through `httputil.ReverseProxy` and `http.Transport`.
- Agent only dials TCP origins and relays bytes.
- Agent and server versions must match during this rewrite.
- Management reverse proxies must allow HTTP/1.1 upgrade streaming for `/agent/tunnel` with `Upgrade: p2pstream-yamux`.
- Origin TLS verification for agent-pool backends is performed by the server-side transport.

## Phase Checklist

- [x] Phase 1: Tunnel package
- [x] Phase 2: Server tunnel bootstrap
- [x] Phase 3: Agent tunnel client
- [x] Phase 4: Unified proxy transport
- [x] Phase 5: Health checks, certificate discovery, environment proxy
- [x] Phase 6: Remove legacy WebSocket/msg/httpmsg runtime
- [x] Phase 7: Docs and setup snippets
- [x] Phase 8: Final verification
- [x] Phase 9: Documentation updates
- [x] Phase 10: Smoke and regression testing

## Phase 2 Hardening Checklist

- [x] Baseline validation
- [x] Docker smoke upstream replacement
- [x] Agent-pool smoke scenario expansion
- [x] Tunnel lifecycle unit/integration tests
- [x] Proxy parity tests
- [x] TLS and certificate discovery hardening
- [x] Observability and logging improvements
- [x] Dev ergonomics cleanup
- [x] Final validation

## Files Changed

| File | Purpose |
| --- | --- |
| `docs/.plans/agent-yamux-overhaul.md` | Persistent handoff ledger for this rewrite. |
| `go.mod`, `go.sum` | Added `github.com/hashicorp/yamux v0.1.2`; removed `github.com/coder/websocket`. |
| `internal/tunnel/*` | New tunnel protocol package: constants, OpenRequest/OpenResponse validation, length-prefixed JSON frames, Yamux config, tests. |
| `internal/server/server.go` | Replaced `/ws` with `/agent/tunnel`; added authenticated HTTP/1.1 upgrade, hijack, Yamux server session lifecycle, DB connected/disconnected state, duplicate rejection. |
| `internal/server/agent_hub.go` | Simplified agent hub to connected sessions only; removed pending request maps and late response tracker. |
| `internal/server/public_routing.go` | Agent-pool proxying now uses `httputil.ReverseProxy` and `http.Transport.DialContext` through Yamux streams. |
| `internal/server/public_backend_health.go` | Agent health checks now use the same server-side HTTP transport over `dialViaAgent`. |
| `internal/server/environment_proxy.go` | Environment proxy and certificate discovery now use server-side TLS over agent tunnel streams. |
| `internal/agent/agent.go` | Replaced WebSocket/message loop with tunnel bootstrap, Yamux client, stream accept loop, TCP dial, and bidirectional byte relay. |
| `internal/agent/management_tls_test.go` | Added regression coverage that tunnel bootstrap forces HTTP/1.1 ALPN even when the management TLS server supports HTTP/2. |
| `msg/*`, `httpmsg/*` | Deleted legacy custom HTTP-message protocol packages. |
| `internal/server/*test.go`, `internal/agent/*test.go`, root `*_test.go` | Migrated tests from WebSocket/message helpers to Yamux tunnel helpers and production-like stream relay. |
| `agent_tunnel_test.go` | Shared root test helper for HTTP/1.1 tunnel upgrade and Yamux agent relay. |
| `cmd/root.go`, `cmd/server.go` | Updated CLI description and management startup log from WebSocket to agent tunnel. |
| `Makefile` | Hardened `dev`, `run`, and `kill` cleanup so Air wrappers, repo-local server/agent child processes, Vite, and stale dev-port listeners are terminated on interrupt/restart. |
| `.air.toml` | Configured Air to send interrupts, wait briefly before killing the dev server child, and ignore noisy dependency/runtime directories. |
| `Dockerfile`, `docker-compose.test.yml` | Added a repo-owned Docker smoke upstream image and replaced the Python static upstream. |
| `internal/smoketest/upstream/main.go` | New smoke upstream with GET, POST echo, headers, streaming, slow-header, close-early, health, and minimal WebSocket endpoints. |
| `internal/smoketest/docker_smoke_test.go` | Expanded Docker smoke coverage for direct and agent-pool GET, POST body, streaming, forwarded headers, timeout, close-early, and WebSocket upgrade scenarios. |
| `internal/agent/agent.go` | Added unsupported-version stream error mapping, tunnel stream debug logs, relay byte/duration logging, and accept-loop exit logs. |
| `internal/agent/agent_test.go` | Added malformed/unsupported OpenRequest recovery and session-close tests. |
| `internal/server/agent_registry_test.go` | Strengthened token rotation tunnel test with DB disconnect, old-token rejection, and new-token reconnect assertions. |
| `internal/server/public_routing.go` | Added agent dial/open failure logs and closed late-opened Yamux streams when request context cancels. |
| `internal/server/server.go` | Added tunnel version, duration, and active request fields to connect/disconnect logs. |
| `internal/server/public_routing_timeout_test.go` | Added upload cancellation, mid-response agent disconnect, and agent-backed HTTPS verification tests. |
| `internal/server/environments_test.go` | Added agent environment certificate discovery, trust, proxy, changed-certificate rejection, and disconnected-agent discovery tests. |
| `internal/server/public_cache_test.go` | Added agent-pool cache miss/store/hit parity coverage with selected agent event recording. |
| `README.md`, `docs/**` | Updated transport, reverse proxy, TLS ownership, and compatibility documentation. |
| `docs/public/architecture.svg` | Updated architecture diagram text from WSS to Yamux tunnel. |

## Validation Log

| Date | Command | Result | Notes |
| --- | --- | --- | --- |
| `2026-06-05T22:38:56Z` | `git pull --ff-only` | passed | `dev` was already up to date before branch creation. |
| `2026-06-05T22:40:00Z` | `go test ./internal/tunnel` | passed | Phase 1 package tests. |
| `2026-06-05T22:45:00Z` | `go test ./internal/server` | passed | Phase 2 server bootstrap checkpoint. |
| `2026-06-05T22:50:00Z` | `go test ./internal/agent` | passed | Phase 3 agent tunnel client checkpoint. |
| `2026-06-06T01:04:00+02:00` | `go test ./...` | passed | Full Go suite after deleting legacy protocol packages. |
| `2026-06-06T01:10:00+02:00` | `make test` | passed | Includes `go test ./...` and `vue-tsc --noEmit`. |
| `2026-06-06T01:11:00+02:00` | `make build` | passed | Frontend/backend build passed; Vite emitted existing large-chunk warning. |
| `2026-06-06T01:16:00+02:00` | `make docker-smoke` | failed | Docker build failed before app smoke checks: temporary DNS failure resolving `deb.debian.org` during `apt-get update`. |
| `2026-06-06T01:16:00+02:00` | `make docker-smoke-clean` | passed | Cleanup completed after failed Docker build. |
| `2026-06-06T01:17:00+02:00` | `rg -n "httpmsg\|p2pstream/msg\|github.com/coder/websocket\|PendingRequests\|LateAgentResponses\|pendingAgentRequest\|WriteCh\|/ws\\b" -S --glob '!docs/.plans/**'` | passed | No legacy runtime refs; matches only unrelated `PendingRequestsPerFlush` naming in web trace store. |
| `2026-06-06T13:55:00+02:00` | `go test ./internal/agent` | passed | Covers the HTTP/1.1 ALPN tunnel bootstrap regression. |
| `2026-06-06T13:56:00+02:00` | `go test ./...` | passed | Full Go suite after ALPN fix. |
| `2026-06-06T13:57:00+02:00` | `timeout 35s make dev; status=$?; make kill; if [ "$status" = 124 ]; then exit 0; else exit "$status"; fi` | passed | Bounded dev smoke reached `Connected tunnel successfully`; local ports `8081`, `8088`, and `8089` were already occupied by another dev instance before cleanup. |
| `2026-06-06T14:02:00+02:00` | `make kill && ss -ltnp '( sport = :8081 or sport = :8088 or sport = :8089 or sport = :5173 )'` | passed | Cleanup removed stale dev listeners; `ss` showed no bound dev ports. |
| `2026-06-06T14:03:00+02:00` | `timeout -s INT 30s make dev; status=$?; sleep 1; ss -ltnp '( sport = :8081 or sport = :8088 or sport = :8089 or sport = :5173 )'; ...` | passed | Restarted from clean state, connected the agent tunnel, handled interrupt, logged graceful shutdown, and released all dev ports. |
| `2026-06-06T14:04:00+02:00` | `git diff --check` | passed | No whitespace errors in the pending diff. |
| `2026-06-06T14:10:00+02:00` | `git status --short --branch` | passed | Clean phase 2 starting point on `rewrite/agent-yamux-transport`. |
| `2026-06-06T14:10:00+02:00` | `git diff --check` | passed | Baseline diff check before phase 2 edits. |
| `2026-06-06T14:11:00+02:00` | `go test ./...` | passed | Baseline full Go suite before phase 2 edits. |
| `2026-06-06T14:12:00+02:00` | `make test` | passed | Baseline `go test ./...` plus management UI typecheck. |
| `2026-06-06T14:13:00+02:00` | `make build` | passed | Baseline frontend/backend build; Vite emitted existing large-chunk warning. |
| `2026-06-06T14:13:00+02:00` | `make kill && ss -ltnp '( sport = :8081 or sport = :8088 or sport = :8089 or sport = :5173 )'` | passed | Baseline cleanup left no dev listeners. |
| `2026-06-06T14:17:00+02:00` | `make docker-smoke` | passed | Existing Docker smoke reached app-level checks and passed after Docker DNS was available. |
| `2026-06-06T14:31:00+02:00` | `go test ./internal/agent ./internal/server ./internal/smoketest ./internal/smoketest/upstream` | failed | First targeted run exposed a test fixture issue: the new agent cache test needed a matching DB agent row for the event FK. |
| `2026-06-06T14:32:00+02:00` | `go test ./internal/server` | passed | Passed after seeding the cache test agent row. |
| `2026-06-06T14:32:00+02:00` | `go test ./internal/agent ./internal/server ./internal/smoketest ./internal/smoketest/upstream` | passed | Targeted phase 2 package validation passed. |
| `2026-06-06T14:33:00+02:00` | `git diff --check` | passed | No whitespace errors after the phase 2 implementation. |
| `2026-06-06T14:34:00+02:00` | `go test ./...` | passed | Full Go suite after phase 2 implementation. |
| `2026-06-06T14:35:00+02:00` | `make test` | passed | Includes full Go suite and management UI typecheck. |
| `2026-06-06T14:36:00+02:00` | `make build` | passed | Frontend/backend build passed; Vite emitted the existing large-chunk warning. |
| `2026-06-06T14:37:00+02:00` | `timeout -s INT 30s make dev` | failed | Exposed stale listeners on `8081`, `8088`, and `8089` from a previous dev run; fixed by extending `make kill` to terminate listener PIDs found via `ss`. |
| `2026-06-06T14:38:00+02:00` | `make kill && ss -ltnp '( sport = :8081 or sport = :8088 or sport = :8089 or sport = :5173 )'` | passed | Cleanup left no dev listeners after the listener-PID fallback. |
| `2026-06-06T14:39:00+02:00` | `timeout -s INT 30s make dev; make kill; ss -ltnp '( sport = :8081 or sport = :8088 or sport = :8089 or sport = :5173 )'` | passed | Dev started, Air ignored noisy dirs, agent tunnel connected, interrupt shut services down, and no dev ports remained bound. |
| `2026-06-06T14:40:00+02:00` | `make docker-smoke-clean && make docker-smoke` | passed | Expanded Docker smoke passed direct GET/POST/stream and agent-pool GET/POST/stream/headers/timeout/close-early/WebSocket scenarios. |
| `2026-06-06T14:41:00+02:00` | `make docker-smoke-clean` | passed | Removed smoke containers, network, and data volume after the successful run. |
| `2026-06-06T14:41:00+02:00` | `go test -race ./internal/tunnel ./internal/agent ./internal/server` | passed | Race run passed for the tunnel protocol, agent relay, and server proxy/lifecycle packages. |

## Deferred: Per-Agent Transport Pooling

Current state:
- Agent transports use `DisableKeepAlives=true` to guarantee each request uses the selected agent.

Future design:
- Cache `http.Transport` per `(agent_id, backend_id, tls_config_fingerprint, timeout_config)`.
- Close the pool on agent disconnect, token rotation, or backend config change.
- Never reuse a transport across agents.
- Add idle connection limits and explicit close-idle behavior.

## Handoff Notes

- Next task: review the phase 2 hardening commit or start the next rewrite phase.
- Known failing tests: none.
- Known risks: performance may regress because agent-pool transports intentionally use `DisableKeepAlives = true` until per-agent pooling is designed.
- Important implementation details:
  - `/agent/tunnel` requires HTTP/1.1 hijack; tunnel clients disable HTTP/2 for bootstrap and force TLS ALPN to `http/1.1`.
  - `make dev` now traps `INT`, `TERM`, and `EXIT` and cleans up both wrapper PIDs and repo-local child binaries so restarts do not leave `8081`, `8088`, or `8089` bound.
  - Each upstream TCP connection maps to one Yamux stream.
  - `OpenRequest` and `OpenResponse` are length-prefixed JSON control frames capped at 16 KiB.
  - Agent decode/open failures close only the stream, not the session.
  - Server-side proxy, health check, environment proxy, and certificate discovery all use `dialViaAgent`.
  - Old WebSocket agents are intentionally incompatible.

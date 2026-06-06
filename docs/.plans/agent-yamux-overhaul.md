# Agent Yamux Transport Overhaul

## Current State

- Branch: `rewrite/agent-yamux-transport`
- Base commit: `ddb6c09f5eb5aa2e7e7335dd11ff8fccaa0eb0d2`
- Last updated: `2026-06-06T14:04:30+02:00`
- Current phase: Implementation complete; final validation mostly complete; `make dev` startup/shutdown verified
- Current blocker: `make docker-smoke` could not complete because Docker build DNS failed resolving `deb.debian.org`; local tests and build pass.

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
- [x] Phase 10: Smoke and regression testing, except Docker smoke blocked by external DNS

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
| `Makefile` | Hardened `dev`, `run`, and `kill` cleanup so Air wrappers, repo-local server/agent child processes, and Vite are terminated on interrupt/restart. |
| `.air.toml` | Configured Air to send interrupts and wait briefly before killing the dev server child. |
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

## Handoff Notes

- Next task: rerun `make docker-smoke` when Docker build DNS can resolve `deb.debian.org`.
- Known failing tests: none in `go test ./...`, `make test`, or `make build`.
- Known risks: Docker smoke has not reached app-level smoke scenarios in this environment; performance may regress because agent-pool transports intentionally use `DisableKeepAlives = true` until per-agent pooling is designed.
- Important implementation details:
  - `/agent/tunnel` requires HTTP/1.1 hijack; tunnel clients disable HTTP/2 for bootstrap and force TLS ALPN to `http/1.1`.
  - `make dev` now traps `INT`, `TERM`, and `EXIT` and cleans up both wrapper PIDs and repo-local child binaries so restarts do not leave `8081`, `8088`, or `8089` bound.
  - Each upstream TCP connection maps to one Yamux stream.
  - `OpenRequest` and `OpenResponse` are length-prefixed JSON control frames capped at 16 KiB.
  - Agent decode/open failures close only the stream, not the session.
  - Server-side proxy, health check, environment proxy, and certificate discovery all use `dialViaAgent`.
  - Old WebSocket agents are intentionally incompatible.

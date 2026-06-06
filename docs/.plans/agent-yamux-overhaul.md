# Agent Yamux Transport Overhaul

## Current State

- Branch: `rewrite/agent-yamux-transport`
- Base commit: `ddb6c09f5eb5aa2e7e7335dd11ff8fccaa0eb0d2`
- Last updated: `2026-06-06T19:07:24+02:00`
- Current phase: Ready to merge to dev
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

## Phase 3 Pooling And Browser E2E Checklist

- [x] Baseline validation
- [x] Agent transport pool implementation
- [x] Public proxy pooling integration
- [x] Health check pooling integration
- [x] Environment proxy pooling integration
- [x] Pool invalidation hooks
- [x] Pool lifecycle/concurrency tests
- [x] Playwright setup
- [x] Environment switch browser tests
- [x] Smoke and regression validation
- [x] Final handoff

## Phase 4 Pre-Merge Checklist

- [x] Branch hygiene and remote sync
- [x] High-risk implementation review
- [x] Generated file check
- [x] Full local validation
- [x] Docker smoke validation
- [x] Browser E2E validation
- [x] Legacy runtime reference scan
- [x] Merge decision recorded
- [ ] Merge to dev
- [ ] Post-merge validation

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
| `web/management/vite.config.ts` | Proxies environment-scoped Connect calls under `/environments/` during Vite dev sessions. |
| `internal/server/agent_transport_pool.go` | Added keyed per-agent `http.Transport` pooling with request-ID context propagation, close-by-agent/backend/environment invalidation, and shutdown cleanup. |
| `internal/server/agent_transport_pool_test.go` | Added pool reuse, separation, invalidation, reconnect, concurrency, and request-ID fallback coverage. |
| `internal/server/server.go` | Added `App.AgentTransports`, disconnect-hook pool cleanup, and app-level transport cleanup helper. |
| `internal/server/agent_hub.go` | Added disconnect hook execution outside the hub mutex for pool invalidation. |
| `internal/server/public_routing.go` | Switched agent-backed public proxying from per-request transports to pooled per-agent/backend transports. |
| `internal/server/public_backend_health.go` | Switched agent-backed health checks to the public backend transport pool and request-ID context propagation. |
| `internal/server/environment_proxy.go` | Switched agent-backed environment proxy calls to pooled per-agent/environment transports while keeping auth wrapping outside the pool. |
| `internal/server/agent_registry.go`, `internal/server/public_config.go`, `internal/server/environments.go` | Added defensive pool invalidation after agent, backend, environment, token, and trust changes. |
| `internal/server/agent_registry_test.go`, `internal/server/environments_test.go`, `internal/server/public_routing_timeout_test.go` | Extended existing lifecycle/environment fixtures to assert pooling and invalidation behavior. |
| `web/management/package.json`, `web/management/bun.lock` | Added Playwright as an explicit frontend dev dependency and added E2E scripts. |
| `web/management/playwright.config.ts` | Added deterministic browser E2E server orchestration with isolated SQLite/cache directories and management/Vite projects. |
| `web/management/e2e/environment-switch.spec.ts`, `web/management/e2e/helpers/connect.ts` | Added browser coverage for switching to a trusted loopback environment through management proxy and direct Vite. |
| `Makefile` | Added `frontend-e2e` target for explicit Playwright execution. |
| `.air.toml`, `.gitignore` | Excluded Playwright artifact directories from Air and Git. |
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
| `2026-06-06T14:48:00+02:00` | `go test ./internal/server -run 'TestEnvironmentRequiresHTTPSAndTrustedCertificateBeforeProxy\|TestAgentEnvironmentProxyDiscoversAndPinsCertificate'` | passed | Added `GetPublicProxyConfig` coverage through direct and agent environment proxies. |
| `2026-06-06T14:49:00+02:00` | `bun run --cwd web/management typecheck` | passed | Verified management UI types after adding the Vite `/environments/` dev proxy. |
| `2026-06-06T14:50:00+02:00` | `bun run --cwd web/management build` | passed | Vite build passed; emitted the existing large-chunk warning. |
| `2026-06-06T15:09:00+02:00` | `git status --short --branch` | passed | Clean phase 3 starting point on `rewrite/agent-yamux-transport`. |
| `2026-06-06T15:09:00+02:00` | `git diff --check` | passed | Baseline diff check before phase 3 edits. |
| `2026-06-06T15:11:00+02:00` | `go test ./...` | passed | Baseline full Go suite before pooling changes. |
| `2026-06-06T15:12:00+02:00` | `make test` | passed | Baseline full Go suite plus management UI typecheck. |
| `2026-06-06T15:13:00+02:00` | `make build` | passed | Baseline frontend/backend build; Vite emitted the existing large-chunk warning. |
| `2026-06-06T15:16:00+02:00` | `make docker-smoke-clean && make docker-smoke` | passed | Baseline Docker smoke passed before pooling changes. |
| `2026-06-06T15:16:00+02:00` | `make docker-smoke-clean` | passed | Removed smoke containers, network, and data volume after the baseline run. |
| `2026-06-06T15:24:00+02:00` | `go test ./internal/server -run 'TestAgentPoolHealthCheckRunsThroughAssignedAgent\|TestAgentEnvironmentProxyDiscoversAndPinsCertificate\|TestAgentProxy'` | passed | Initial pooling integration checkpoint. |
| `2026-06-06T15:26:00+02:00` | `go test ./internal/server -run 'TestAgentTransportPool\|TestRotateAgentTokenDisconnectsActiveAgent\|TestAgentEnvironmentProxyDiscoversAndPinsCertificate\|TestAgentProxy\|TestPublicBackendHealth'` | passed | Pool unit/integration tests, token rotation invalidation, and environment reuse checkpoint. |
| `2026-06-06T15:27:00+02:00` | `bun run --cwd web/management typecheck` | passed | Frontend typecheck after adding Playwright files. |
| `2026-06-06T15:28:00+02:00` | `bun run --cwd web/management e2e` | failed | Playwright browser binary was not installed in the local environment. |
| `2026-06-06T15:29:00+02:00` | `bun run --cwd web/management e2e:install` | passed | Installed Chromium for local Playwright execution. |
| `2026-06-06T15:31:00+02:00` | `bun run --cwd web/management e2e` | failed | Exposed Playwright setup isolation issue: implicit config DB migrated the repo-root legacy `p2pstream.db`. |
| `2026-06-06T15:42:00+02:00` | `bun run --cwd web/management e2e` | passed | Browser environment switch passed through both `management-proxy` and `vite-direct` projects after using an explicit isolated SQLite URL and deterministic E2E server startup. |
| `2026-06-06T15:43:00+02:00` | `git diff --check` | passed | No whitespace errors after Phase 3 implementation. |
| `2026-06-06T15:43:00+02:00` | `go test ./internal/server -run 'TestAgentTransportPool\|TestEnvironment\|TestAgentProxy\|TestPublicBackendHealth'` | passed | Focused server regression set passed. |
| `2026-06-06T15:43:00+02:00` | `bun run --cwd web/management typecheck` | passed | Frontend typecheck passed after Playwright config fixes. |
| `2026-06-06T15:44:00+02:00` | `go test ./internal/server` | passed | Full server package passed with pooling enabled. |
| `2026-06-06T15:44:00+02:00` | `bun run --cwd web/management build` | passed | Frontend production build passed; Vite emitted the existing large-chunk warning. |
| `2026-06-06T15:45:00+02:00` | `go test ./...` | passed | Full Go suite passed after pooling and E2E changes. |
| `2026-06-06T15:45:00+02:00` | `make test` | passed | Full Makefile test target passed, including management UI typecheck. |
| `2026-06-06T15:46:00+02:00` | `make build` | passed | Frontend/backend build passed; Vite emitted the existing large-chunk warning. |
| `2026-06-06T15:47:00+02:00` | `bun run --cwd web/management e2e` | passed | Final browser E2E rerun passed both management proxy and direct Vite projects. |
| `2026-06-06T15:48:00+02:00` | `make docker-smoke-clean && make docker-smoke && make docker-smoke-clean` | passed | Docker smoke passed direct and agent-pool GET/POST/stream/header/timeout/close-early/WebSocket scenarios and cleaned up containers/volume/network. |
| `2026-06-06T15:49:00+02:00` | `go test -race ./internal/server` | passed | Race coverage passed for server pooling, proxying, environment, and lifecycle tests. |
| `2026-06-06T15:49:00+02:00` | `make kill && ss -ltnp '( sport = :8081 or sport = :8088 or sport = :8089 or sport = :5173 or sport = :19081 )'` | passed | Final cleanup left no dev/E2E listeners bound. |
| `2026-06-06T18:58:00+02:00` | `git switch rewrite/agent-yamux-transport && git status --short --branch && git fetch origin && git rev-list --left-right --count origin/dev...rewrite/agent-yamux-transport && git log --oneline origin/dev..rewrite/agent-yamux-transport` | passed | Branch clean; `origin/dev` had not moved; branch was `0` behind and `4` commits ahead. |
| `2026-06-06T18:59:00+02:00` | Manual high-risk review | passed | Reviewed pool keys, invalidation hooks, request-ID context propagation, Playwright isolated `DATABASE_URL`, and legacy transport removal points. No code changes needed. |
| `2026-06-06T19:00:00+02:00` | `make generate && git diff --exit-code -- . ':(exclude)docs/.plans/agent-yamux-overhaul.md'` | passed | No generated-file changes; ledger was intentionally dirty for Phase 4 bookkeeping. |
| `2026-06-06T19:00:00+02:00` | `bash -n scripts/install-agent.sh` | passed | Installer shell syntax check passed. |
| `2026-06-06T19:01:00+02:00` | `go test ./...` | passed | Full Go suite passed. |
| `2026-06-06T19:01:00+02:00` | `go vet ./...` | passed | Go vet passed. |
| `2026-06-06T19:01:00+02:00` | `bun test --cwd web/management src/lib/*.test.ts` | failed | This invocation treats the glob as a Bun filter; reran using the CI workflow form below. |
| `2026-06-06T19:01:00+02:00` | `cd web/management && bun test src/lib/*.test.ts` | passed | Frontend unit tests passed: 92 tests across 9 files. |
| `2026-06-06T19:01:00+02:00` | `bun run --cwd web/management typecheck` | passed | Management UI typecheck passed. |
| `2026-06-06T19:01:00+02:00` | `bun run --cwd web/management build` | passed | Frontend build passed; Vite emitted the existing large-chunk warning. |
| `2026-06-06T19:02:00+02:00` | `docker build --target runtime -t p2pstream:premerge .` | passed | Runtime image build passed; frontend build emitted the existing large-chunk warning. |
| `2026-06-06T19:02:00+02:00` | `go test -race ./internal/tunnel ./internal/agent ./internal/server` | passed | Race checks passed for tunnel, agent, and server packages. |
| `2026-06-06T19:02:00+02:00` | `bun run --cwd web/management e2e` | failed | A stale previous dev instance was bound to `5173`, `8081`, `8088`, and `8089`; cleaned with `make kill`. |
| `2026-06-06T19:04:00+02:00` | `bun run --cwd web/management e2e` | failed | Vite process stayed alive without binding after stale-state cleanup; terminated and removed `web/management/test-results`, `web/management/playwright-report`, and Playwright tmp dirs. |
| `2026-06-06T19:05:00+02:00` | `bun run --cwd web/management e2e` | passed | Browser environment switch passed in both `management-proxy` and `vite-direct` projects after clean artifact state. |
| `2026-06-06T19:06:00+02:00` | `make docker-smoke-clean && make docker-smoke && make docker-smoke-clean` | passed | Docker smoke passed direct and agent-pool GET/POST/stream/header/timeout/close-early/WebSocket scenarios and cleaned up containers/volume/network. |
| `2026-06-06T19:07:00+02:00` | `timeout -s INT 30s make dev; make kill; ss -ltnp '( sport = :8081 or sport = :8088 or sport = :8089 or sport = :5173 or sport = :19081 )'` | passed | Dev server started, Vite bound, agent tunnel connected, interrupt shut services down, and no checked ports remained bound. |
| `2026-06-06T19:07:00+02:00` | `rg -n "github.com/coder/websocket\|p2pstream/msg\|httpmsg\|PendingRequests\|LateAgentResponses\|pendingAgentRequest\|WriteCh\|/ws\\b" -S --glob '!docs/.plans/**' --glob '!web/management/bun.lock'` | passed | Only allowed matches: smoke `/ws` endpoint/test and unrelated frontend `PendingRequestsPerFlush` naming. |

## Deferred: Per-Agent Transport Pool Refinements

Current state:
- Agent-backed public proxy, health check, and environment proxy paths use pooled `http.Transport` instances.
- Pools are keyed by agent plus backend/environment/TLS/timeout/origin inputs.
- Idle connections are closed on agent disconnect, token rotation, backend update/delete, environment update/delete/trust, and app shutdown.

Future design:
- Add per-agent/backend pool observability if operators need runtime visibility into idle connection reuse.
- Tune idle connection limits from production data.
- Consider closing environment pools on management access-token changes if token lifecycle grows beyond current auth wrapping.

## Phase 4 Merge Decision

Final branch commits:
- `131ba39 Rewrite agent transport with yamux tunnel`
- `4881478 Harden yamux transport smoke and lifecycle coverage`
- `0bda8c0 Fix environment-scoped management proxy in dev`
- `7cf9677 Add agent transport pooling and browser e2e coverage`

Merge decision:
- Merge `rewrite/agent-yamux-transport` into `dev` with `git merge --no-ff`.
- Preserve branch commits; do not squash.
- Add no new protocol, API, proto, or production metrics changes in Phase 4.
- Create only this ledger validation commit before the merge.

## Handoff Notes

- Next task: merge `rewrite/agent-yamux-transport` into `dev` and run post-merge validation.
- Known failing tests: none.
- Known risks: pooled transports intentionally reuse Yamux streams for sequential same-key HTTP requests; invalidation coverage is broad, but future config surfaces must close the relevant pool entries when they affect dialing, TLS, origin, or timeout behavior.
- Important implementation details:
  - `/agent/tunnel` requires HTTP/1.1 hijack; tunnel clients disable HTTP/2 for bootstrap and force TLS ALPN to `http/1.1`.
  - `make dev` now traps `INT`, `TERM`, and `EXIT` and cleans up both wrapper PIDs and repo-local child binaries so restarts do not leave `8081`, `8088`, or `8089` bound.
  - Playwright E2E uses an explicit isolated `DATABASE_URL`; relying only on `CONFIG_DIR` would migrate the repo-root legacy `p2pstream.db`.
  - Playwright starts the backend, waits for `GetSetupState`, starts the agent, then starts Vite so both `management-proxy` and `vite-direct` projects are ready deterministically.
  - `web/management/test-results` and `web/management/playwright-report` are ignored by Git and Air.
  - Each upstream TCP connection maps to one Yamux stream.
  - `OpenRequest` and `OpenResponse` are length-prefixed JSON control frames capped at 16 KiB.
  - Agent decode/open failures close only the stream, not the session.
  - Server-side proxy, health check, environment proxy, and certificate discovery all use `dialViaAgent`.
  - Pooled agent transports pull the request ID from context; if absent, `dialViaAgent` receives a generated UUID fallback.
  - Old WebSocket agents are intentionally incompatible.

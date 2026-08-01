# p2pstream Security Context

## 1. Audit target

Repository: `https://github.com/Kirari04/p2pstream`

Target revision:

- The context below was prepared from the repository's `dev` branch and public documentation.
- Before starting the scan, record the exact local commit with:

  ```bash
  git rev-parse HEAD
  ```

- Replace this section with that commit hash.
- If production runs a tagged release or the `main` branch, scan that exact deployed revision as a separate target. Do not assume findings on `dev` exactly represent production.

Authorization:

- The repository owner has authorized a defensive security review of this codebase.
- Validation must be limited to the local checkout, local containers, and synthetic test services.
- Do not probe public hosts, deployed customer instances, real upstream services, or third-party infrastructure.
- Do not use real credentials or disclose secrets found in local files.

## 2. System purpose

p2pstream is a self-hosted reverse proxy and traffic-management system with:

- Public HTTP and HTTPS reverse-proxy listeners.
- A web management UI and management API.
- Static responses and redirects.
- Direct HTTP(S) upstream targets.
- Optional remote agents that reach origins on another network.
- Automatic and manually configured TLS certificates.
- Global WAF rules.
- Rate limits and traffic shaping.
- Shared public-asset caching.
- Request tracing and observability.
- Remote-environment administration.

Security-sensitive assets include:

- Management administrator accounts and sessions.
- Management API access tokens.
- Agent enrollment/authentication tokens.
- Remote-environment access tokens.
- Upstream credentials and custom upstream headers.
- Management TLS private keys and local CA material.
- Public-listener TLS private keys and ACME account material.
- Cloudflare DNS credentials when DNS-01 is configured.
- SQLite configuration and operational state.
- Route, target, listener, WAF, cache, rate-limit, and traffic-shaping policy.
- Request traces and operational logs.
- Access to private networks reachable by the main server or connected agents.

## 3. High-level architecture

The normal deployment is a single Go server process with two main network surfaces.

### Public data plane

Public listeners accept client HTTP or HTTPS traffic. Listeners, routes, and targets are configured through the management plane and stored in SQLite.

The documented public request pipeline is approximately:

1. Accept the request on a configured public listener.
2. Reject invalid or dangerous request paths.
3. Handle ACME challenge traffic and reserved internal paths.
4. Perform an early route match needed for path-security decisions.
5. Apply the global WAF.
6. Apply rate limiting.
7. Apply traffic shaping.
8. Select the final route and target, or the listener default.
9. Apply shared-cache policy.
10. Serve a static response, redirect, direct upstream, or agent upstream.
11. Record traces, logs, metrics, and health information.

### Management/control plane

The management listener serves:

- The Vue-based management UI.
- The ConnectRPC management API.
- Authentication and session endpoints.
- Management access-token operations.
- Agent status and enrollment functions.
- Remote-environment administration.
- An authenticated agent tunnel endpoint at `/agent/tunnel`.
- Management TLS, which is enabled by default.

The documented default management port is `8081`.

### Agent plane

A remote agent makes an outbound TLS connection to the management endpoint and requests an HTTP/1.1 protocol upgrade on `/agent/tunnel` to `p2pstream-yamux`.

After authentication:

- The server maintains a Yamux session with the agent.
- A route may select an agent using labels.
- The server opens one Yamux stream for an upstream TCP connection.
- The agent dials the configured origin from the agent's network and relays bytes.
- The server owns the HTTP transport that runs over the stream.

This creates a significant trust boundary: a compromise or authorization error can turn the proxy or an agent into a pivot into networks that are not reachable from the public internet.

## 4. Runtime and deployment model

Documented/default deployment characteristics:

- Backend language/runtime: Go.
- Frontend: Vue application built with Bun.
- State store: SQLite using WAL mode.
- Typical container runtime: Debian Bookworm slim.
- Container process: non-root `p2pstream` user.
- The binary is granted `cap_net_bind_service` so it can bind ports below 1024.
- Persistent state is normally mounted at `/data`.
- Default SQLite path is `/data/p2pstream.db`.
- Default management bind address is `0.0.0.0`.
- Default exposed ports are `80`, `443`, and `8081`.
- The default Docker Compose example publishes all three ports.
- The management UI can be disabled for API-only operation.
- The server is one process; concurrency is managed by the Go runtime. Goroutine, connection, cancellation, and shared-state lifecycle must be reviewed.

Deployment assumption to verify:

- The application appears to use one administrative trust domain rather than an explicitly untrusted multi-tenant control plane.
- Multiple hostnames, listeners, routes, agents, and environments can still coexist, so cross-route, cross-host, and cross-agent isolation remains security-sensitive.

## 5. Network interfaces and protocols

### Documented interfaces

- Public HTTP listener, commonly TCP `80`.
- Public HTTPS listener, commonly TCP `443`.
- Management HTTPS listener, commonly TCP `8081`.
- ConnectRPC API over the management listener.
- HTTP/1.1 Upgrade to the agent Yamux protocol at `/agent/tunnel`.
- Direct HTTP or HTTPS connections to configured origins.
- Raw TCP streams through connected agents, carrying the server-controlled upstream HTTP transport.
- ACME HTTP-01, TLS-ALPN-01, and DNS-01 operations where configured.
- Outbound HTTPS connections to configured remote p2pstream environments.

### Not confirmed by the public documentation

The audit must determine whether these are implemented, partially supported, or rejected:

- Public HTTP/2 behavior.
- Upstream HTTP/2 behavior.
- HTTP/3.
- Public WebSocket forwarding.
- Public `CONNECT` forwarding.
- PROXY protocol.
- h2c.
- Arbitrary TCP proxying outside the agent transport.
- Upgrade handling other than the management agent tunnel.

Do not assume an undocumented protocol is unavailable merely because it is absent from the documentation.

## 6. Public and administrative entry points

### Public entry points

- Every configured public listener.
- Every hostname and wildcard-host route.
- Every route path prefix.
- Listener fallback/default behavior.
- Static-response routes.
- Redirect routes.
- Direct proxy targets.
- Agent proxy targets.
- ACME challenge endpoints.
- Reserved WAF endpoints:
  - `/.p2pstream/waf/captcha/verify`
  - `/.p2pstream/waf/waiting-room`
  - `/.p2pstream/waf/waiting-room/status`

### Administrative entry points

- Management UI.
- ConnectRPC management API.
- Initial administrator setup flow.
- Login, logout, password reset, and session management.
- Management API access tokens.
- Agent creation, token issuance, authentication, labels, and status.
- Agent tunnel upgrade at `/agent/tunnel`.
- Remote-environment creation and access-token configuration.
- Listener, route, target, certificate, WAF, cache, rate-limit, traffic-shaper, and template configuration.
- Trace, log, health, and metrics functions.
- Configuration import/export or backup/restore functions, if present.
- Management-TLS configuration and certificate rotation.

The WAF applies to the public reverse-proxy pipeline. Do not assume it protects the management listener.

## 7. Routing and upstream selection

A route belongs to a listener and can match using:

- Exact hostnames.
- Wildcard hostnames.
- Path prefixes.
- Priority.
- Stable ID ordering as a tie-breaker.

A forward route selects among configured targets. Targets can be:

- Direct HTTP(S) origins.
- Agent-backed origins.
- Static responses.
- Redirects.

Targets can have priorities, health state, and load-balancing behavior.

Security model:

- At request time, an unauthenticated public client must not be able to replace or extend the configured target authority, scheme, host, IP, port, Unix socket, agent, or remote environment.
- The incoming `Host`, `:authority`, path, query, headers, body, and protocol framing are attacker-controlled.
- Operator-configured targets may legitimately reach private networks, but only the configured destination should be reachable.
- Redirects, URL parsing, path joining, environment proxy variables, DNS resolution, and protocol upgrades must not allow a request to escape the configured destination.
- Agent-backed targets must not let public requests choose arbitrary endpoints reachable from the agent's network.
- Health checks and validation requests must follow the same destination restrictions as production forwarding.

Path security is documented as strict by default. It rejects encoded separators, decoded dot or dot-dot segments, and literal backslashes. A compatibility mode may exist and shared-cache behavior changes under that mode. Review all canonicalization decisions for consistency.

## 8. Client identity and trusted proxies

The server contains a trusted-client-identity subsystem with provider modes such as:

- Custom trusted proxy ranges.
- Cloudflare.
- Bunny.
- CloudFront.

Documented behavior includes:

- Client-IP headers are ignored when the direct peer is not trusted.
- Missing, malformed, excessive, or conflicting forwarding data from a trusted peer should fail closed.
- Trusted-chain and single-IP header modes.
- Limits on trusted header size and hop count.
- Removal of configured client-IP headers before forwarding upstream.
- Sanitization and regeneration of `Forwarded`, `X-Forwarded-*`, `X-Real-IP`, and related headers.

Required audit outcomes:

- An untrusted public client cannot spoof the identity used by WAF, rate limiting, routing, logging, or upstream applications.
- Provider CIDR refreshes cannot be redirected to attacker-controlled URLs.
- Provider lists have strict response-size, entry-count, parsing, and timeout limits.
- Stale provider data has a safe failure mode.
- IPv4, IPv6, IPv4-mapped IPv6, zone identifiers, alternative forms, and chained proxies cannot bypass trust checks.
- The identity used for policy is the same identity recorded in traces and forwarded upstream.
- Public requests cannot inject a second authoritative forwarded-header representation.

## 9. Authentication and authorization

Documented management authentication includes:

- A first-administrator setup window.
- A setup token.
- Username validation.
- A minimum password length of 12 characters.
- SQLite-backed sessions.
- Session expiry around seven days.
- HTTP-only session cookies.
- SameSite `Lax`.
- Secure cookies under TLS/production/forced-secure configuration.
- Session revocation after password reset.
- General management API access tokens.
- Agent IDs and tokens; agent tokens are displayed once and stored hashed.
- Remote-environment access tokens.

Review at minimum:

- Initial setup race conditions and exposure of the setup token.
- Brute-force resistance and login throttling.
- Session fixation, rotation, revocation, expiry, and concurrent-session behavior.
- CSRF for cookie-authenticated state-changing operations.
- CORS and origin validation.
- Cookie security when management TLS is explicitly disabled.
- Authorization on every ConnectRPC method, not only UI routes.
- Privilege checks for token, agent, environment, TLS, route, listener, and policy changes.
- Token hashing and constant-time comparison.
- Secret values returned by read/list APIs after initial creation.
- Logging, tracing, error-message, and telemetry leakage.
- Authorization of the HTTP/1.1 Upgrade path before a Yamux session is accepted.
- Cross-agent impersonation and label manipulation.
- Remote-environment token use and credential forwarding.
- Fail-open behavior during database, TLS, or policy errors.

## 10. TLS and certificate handling

### Management TLS

Management TLS modes include:

- `auto`, the default, using a generated local CA and server certificate.
- `provided`, using operator-supplied material.
- `off`, only when explicitly allowed as an insecure mode.

Agents can trust management TLS through CA-file or base64 CA configuration. Optional agent mutual TLS can be configured with a client CA.

Required properties:

- Management TLS fails closed when configured material is missing, mismatched, invalid, expired, or unreadable.
- Private keys are created with restrictive permissions and are never sent through normal read APIs.
- Hostname/SAN verification cannot be silently disabled.
- Certificate reloads are atomic.
- Disabling TLS cannot accidentally leave Secure-cookie or authentication assumptions inconsistent.
- mTLS identity is bound correctly to the authenticated agent identity.

### Public TLS

Public HTTPS listeners support certificate mappings by exact or wildcard hostname, with sources including:

- Manual upload.
- File paths.
- Self-signed certificates.
- ACME HTTP-01.
- ACME TLS-ALPN-01.
- ACME DNS-01, including Cloudflare integration.

Review:

- SNI normalization and exact/wildcard precedence.
- Default-certificate behavior.
- Cross-host certificate leakage.
- ACME challenge routing and authorization.
- DNS credential handling.
- Certificate renewal races and rollback behavior.
- File permissions and symlink handling.
- TLS-ALPN challenge isolation.
- Certificate/key parsing limits.
- Safe cleanup of replaced key material.

### Upstream TLS

Direct proxy targets contain an option capable of skipping TLS verification.

Treat this as an intentional high-risk operator option, not automatically as a vulnerability. Verify that:

- Verification is enabled by default.
- Disabling it is explicit and clearly surfaced.
- It cannot be enabled by an unprivileged or public user.
- It is scoped to the intended target.
- Credentials are not sent to an unintended server after redirects, DNS changes, or target failover.
- SNI and hostname verification use the intended configured hostname.

## 11. Cache model

The shared public-asset cache runs after route and target selection. Documented features include:

- Configurable cache-key scope.
- Query-string handling.
- Vary-header handling, with `Accept-Encoding` relevant by default.
- Configurable cacheable status codes.
- A maximum object size, documented up to 100 MiB.
- Cookie-bearing requests bypassing shared cache.
- WAF, rate limiting, and traffic shaping still applying to cache hits.

Audit for:

- Missing `Authorization` bypass.
- `Set-Cookie` responses entering shared cache.
- Cross-host, cross-route, cross-target, or cross-agent cache collisions.
- Encoded-path and normalization mismatches.
- Query sorting, duplicate keys, empty values, semicolon handling, and percent encoding.
- `Vary` mismatches.
- HEAD/GET confusion.
- Range response poisoning.
- Compression variant confusion.
- Redirect and error caching.
- Host header or scheme confusion.
- Cache deception.
- Purge/invalidation authorization.
- Object-size enforcement before and during buffering.
- Compatibility path mode bypassing cache consistently.
- Sensitive data appearing in cache diagnostics or traces.

## 12. WAF, rate limiting, and traffic shaping

The documented order is:

1. WAF.
2. Rate limit.
3. Traffic shaper.
4. Route and target selection.
5. Cache.
6. Origin.

Policy expressions can use request properties such as method, protocol, host, path, remote IP/CIDR, headers, cookies, and query values.

Review:

- Expression parser/evaluator denial of service.
- Cost and size limits.
- Regex backtracking or expensive match behavior.
- Canonicalization consistency between policy and routing.
- Header, query, and cookie ambiguity.
- Trusted-client-IP consistency.
- Rule ordering and fail-open behavior.
- Reserved-endpoint bypasses.
- ACME bypass scope.
- Captcha and waiting-room token integrity, replay protection, expiry, and client binding.
- Memory growth in waiting-room or rate-limit state.
- Distributed or restart behavior.
- Cache hits receiving the same protections as misses.
- Policy update atomicity.

## 13. Persistent state, filesystem, and secrets

The persistent data directory contains SQLite state and generated certificate material. Documentation warns that database backups can contain operational tokens and upstream credentials.

Required controls:

- The data directory is not world-readable or writable.
- Default directory permissions remain restrictive.
- SQLite, WAL, and shared-memory files receive safe permissions.
- Database write access is equivalent to administrative control and must be treated accordingly.
- Uploaded or generated certificates cannot escape the configured directory through path traversal or symlink attacks.
- Backup, export, and diagnostic functions redact or protect secrets.
- No secret is logged in plaintext.
- Tokens shown once are not recoverable through ordinary APIs.
- Error responses do not expose filesystem paths, SQL details, keys, or credentials.
- Database migrations are transactional or fail safely.
- Corrupt or partially migrated state cannot disable authentication or authorization.
- Configuration updates and runtime snapshots are atomic.

## 14. Server-side outbound requests and SSRF-sensitive features

Review every outbound network path, including:

- Direct upstream connections.
- Agent-origin connections.
- Health checks.
- ACME directory and challenge operations.
- DNS provider APIs.
- Trusted-proxy provider range downloads.
- GeoIP database downloads, if enabled.
- Remote-environment management requests.
- Certificate retrieval or validation.
- Redirect following.
- Any webhook, import, URL preview, or diagnostic feature found during the audit.

Remote-environment management is especially sensitive because an administrator supplies an HTTPS management URL and access token. Verify:

- Scheme restrictions.
- Redirect handling.
- DNS rebinding resistance.
- Loopback, link-local, private, multicast, unspecified, and metadata-address handling according to the intended policy.
- IPv4/IPv6 normalization.
- Credential stripping on host, scheme, or port changes.
- Certificate pinning and hostname verification.
- Response-size and timeout limits.
- No proxy-environment-variable bypass.
- No cross-environment credential reuse.

## 15. Availability and resource limits

The public proxy and agent tunnel are internet-facing or semi-public and must have strict bounds.

Audit:

- Request-line, URI, header-count, header-size, trailer, and body limits.
- Chunked uploads and bodies without known length.
- Slowloris and slow-body behavior.
- Read, write, idle, handshake, response-header, and full-request timeouts.
- Upstream retry count and amplification.
- Redirect loops.
- Connection-pool limits and stale connections.
- Agent Yamux stream count and per-agent request limits.
- Stream cancellation and half-close behavior.
- Goroutine, timer, body, buffer, socket, and file-descriptor leaks.
- Large static responses.
- Cache fill concurrency and duplicate work.
- Compression/decompression bombs, if compression is implemented.
- WAF expression and regex cost.
- Trace/log volume amplification.
- Unbounded label, route, target, certificate, and policy counts.
- SQLite lock contention and write amplification.
- Graceful shutdown and configuration reload.
- Panic/exception paths reachable from untrusted input.

Distinguish ordinary high traffic from attacks that create asymmetric CPU, memory, disk, connection, or outbound-request cost.

## 16. Security invariants

The audit must verify these invariants:

1. Public clients cannot select an arbitrary direct-origin destination.
2. Public clients cannot select an arbitrary endpoint reachable from an agent.
3. Public clients cannot reach the management listener through public routing.
4. Untrusted forwarding headers cannot establish client identity or privilege.
5. Host, authority, scheme, path, and client identity have one canonical representation before security decisions.
6. Frontend and upstream request framing cannot diverge.
7. Hop-by-hop headers are removed correctly across direct and agent transports.
8. Authentication and authorization remain valid after rewrites, retries, redirects, fallback, and failover.
9. Credentials are not forwarded to a destination with a lower trust level.
10. Hostnames, routes, targets, agents, caches, connections, certificates, and traces do not leak data across security boundaries.
11. Management operations require authenticated, authorized requests and appropriate CSRF protection.
12. Agent registration and tunnels require cryptographically strong, correctly scoped credentials.
13. Agent labels cannot be used to impersonate or hijack another agent.
14. Upstream TLS verification fails closed unless an authorized operator explicitly configures a narrow exception.
15. Management TLS fails closed unless an authorized operator explicitly enables insecure mode.
16. Public request processing has enforceable limits on time, memory, body size, headers, connections, streams, retries, and logs.
17. Runtime configuration replacement is atomic or fails safely.
18. Secret material is not exposed through logs, traces, APIs, caches, backups, or errors.
19. Shared-cache entries cannot disclose one request's private response to another requester.
20. ACME and reserved internal paths cannot bypass unrelated security controls.
21. Database corruption, lock errors, or migration failures cannot cause authentication or authorization to fail open.
22. Security behavior is consistent between direct origins and agent-backed origins.

## 17. Attacker-controlled inputs

Treat these as hostile unless a specific authenticated role and validation rule says otherwise:

- TCP connection timing and lifecycle.
- TLS ClientHello and SNI.
- HTTP version and upgrade requests.
- Request method.
- Request target in origin-form, absolute-form, authority-form, or malformed forms.
- Host and `:authority`.
- Path, raw path, percent encoding, dot segments, slashes, backslashes, and Unicode.
- Query keys, values, duplicates, separators, and encoding.
- Every request header, duplicate header, and unusual whitespace form.
- Cookies.
- Request body, trailers, and chunk boundaries.
- `Content-Length` and `Transfer-Encoding`.
- `Forwarded`, `X-Forwarded-*`, `X-Real-IP`, CDN headers, and client-certificate headers.
- Origin, Referer, and browser CSRF-related headers.
- WebSocket or generic upgrade headers.
- Client disconnects, resets, retries, and concurrent requests.
- Every field accepted by the management API.
- Uploaded certificates and keys.
- Route, listener, target, WAF, cache, rate-limit, traffic-shaper, and response-template configuration.
- Agent labels and metadata.
- Remote-environment URLs and credentials.
- Imported configuration or backup content.
- Filesystem paths accepted through configuration.
- DNS answers and redirect responses from destinations contacted by the server.

## 18. High-priority source areas

Prioritize these code areas and expand the list during reconnaissance:

### Public proxy and routing

- `internal/server/public_proxy_pipeline.go`
- `internal/server/public_routing.go`
- `internal/server/http_defaults.go`
- Public listener lifecycle and snapshot code.
- Request-target and path-validation code.
- Route matching, host normalization, and redirect construction.
- Direct and agent transport pools.
- Reverse-proxy rewrite and forwarded-header logic.

### Client identity and trust

- `internal/server/client_identity*`
- `internal/server/trusted_proxy*`
- Provider CIDR download/update code.
- Forwarded-header sanitization and upstream header generation.

### Agent and tunnel

- `internal/server/agent_*`
- `internal/server/*agent*transport*`
- `internal/tunnel/`
- `internal/agent/`
- Yamux setup, stream limits, authentication, labels, connection replacement, and cancellation.

### Management authentication and authorization

- `internal/server/auth*`
- `internal/server/management_token*`
- Login throttling and setup code.
- ConnectRPC interceptors and method-level authorization.
- Session-cookie creation and validation.
- Management API handlers.

### TLS and ACME

- `internal/server/management_tls*`
- `internal/server/public_tls*`
- `internal/server/public_acme*`
- `internal/server/cloudflare_dns*`
- Certificate storage and reload code.

### Policy, caching, and observability

- `internal/server/public_waf*`
- `internal/server/public_cache*`
- `internal/server/public_rate*`
- `internal/server/public_traffic*`
- Trace, log, metrics, redaction, response-template, and CEL evaluation code.

### Configuration and persistence

- `internal/config/`
- `internal/db/`
- SQL migrations and generated queries.
- Runtime snapshot and configuration-reload code.
- Remote-environment proxy and certificate-pinning code.

### Packaging and frontend

- `Dockerfile`
- Docker Compose files.
- Installer and systemd scripts.
- GitHub Actions and release workflows.
- `web/management/`, especially authentication, token display, API clients, and HTML rendering.

## 19. Build and test commands

Expected local toolchain includes Go and Bun.

Common commands:

```bash
make dev
make test
make build
make verify
make docker-build
make docker-smoke
make docker-race-test
```

Important:

- `make verify` expects a clean Git tree and can fail because of modified or untracked files.
- Commit `SECURITY_CONTEXT.md`, keep it in a dedicated audit commit, or temporarily move it outside the checkout before running `make verify`.
- Run generated proof-of-concept tests in a separate audit branch or worktree.
- Do not weaken existing tests to make a finding reproduce.

Suggested baseline:

```bash
go test ./...
go test -race ./...
go vet ./...
```

Also run the repository's frontend type checks/tests and installer/lifecycle checks through the documented Make targets.

## 20. Safe local validation environment

Use only synthetic components:

- Main server bound to loopback or an isolated Docker network.
- A local HTTP origin that records received method, target, headers, body, and connection boundaries.
- A local HTTPS origin using test certificates.
- A deliberately strict HTTP/1.1 origin parser for desynchronization tests.
- A deliberately different frontend/backend parsing pair only inside the local lab.
- A local DNS server capable of controlled answer changes for rebinding tests.
- A local agent connected to a private Docker network containing synthetic services.
- A fake metadata endpoint containing harmless marker data.
- Local test CA and disposable certificates.
- Disposable SQLite databases and data directories.
- Synthetic administrator, API, environment, and agent credentials.

Never direct validation traffic to:

- Cloud metadata endpoints.
- RFC1918 networks outside the isolated lab.
- Real Cloudflare APIs.
- Public ACME production directories.
- Public trusted-proxy-list endpoints during fuzzing.
- Real remote environments.
- Production agents or origins.

For ACME testing, use a local test server or an appropriate staging environment only when explicitly configured and authorized.

## 21. Finding standards

Classify findings as:

- **Confirmed**: reproduced safely in the local test environment.
- **High confidence**: complete, reachable source-to-sink path with strong code evidence.
- **Medium confidence**: plausible path with one unresolved deployment or runtime assumption.
- **Low confidence / hardening**: speculative, defense-in-depth, or dependent on an unrealistic configuration.

Every primary finding must include:

- Affected file, symbol, and line range.
- Exact attacker-controlled source.
- Trust boundary crossed or invariant broken.
- Required authentication and privileges.
- Required configuration and deployment assumptions.
- Complete execution or data-flow path.
- Safe reproduction or rigorous code-path evidence.
- Observed and expected behavior.
- Existing protections and why they do not prevent exploitation.
- Realistic impact.
- Minimal root-cause remediation.
- Regression-test design.
- Residual risk.

Do not report:

- Generic dependency-version concerns without a reachable vulnerable path.
- Operator-controlled insecure settings as vulnerabilities unless the default, authorization, scope, warning, or isolation is defective.
- Hypothetical request smuggling without identifying two components that interpret the same bytes differently.
- Generic SSRF solely because administrators can configure an origin; demonstrate unauthorized destination influence or unsafe handling of a lower-trust configuration source.
- Missing headers or generic best practices without project-specific impact.

## 22. Questions the repository owner should confirm

The scan may proceed without answers, but the report must mark these as assumptions:

1. Which exact branch, tag, and commit are deployed in production?
2. Is management port `8081` publicly reachable, VPN-only, or private-network-only?
3. Is management TLS ever intentionally disabled?
4. Are public users expected to use WebSockets or any generic HTTP Upgrade?
5. Are HTTP/2, h2c, HTTP/3, CONNECT, or PROXY protocol intentionally supported?
6. Can any user below full administrator privilege edit routes, targets, agents, environments, certificates, or security policy?
7. Are untrusted customers given management access to one shared instance?
8. Which trusted-proxy provider modes are used in production?
9. Are direct origins or agent origins allowed to be arbitrary private IP addresses?
10. Is upstream TLS verification ever disabled in production?
11. Are remote environments allowed to point at private or loopback addresses?
12. Are Cloudflare DNS credentials stored in the database or an external secret store?
13. What request-body, header, timeout, retry, connection, and Yamux-stream limits are intended?
14. Which trace fields and request/response bodies may be persisted?
15. What is the expected backup, restore, and key-rotation procedure?

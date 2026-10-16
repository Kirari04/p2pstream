# Native Go attribution: effective settings before implementation

Base: 248bad099682878fe398c0ebaad235787ef47858, exact Go1.26.6.
This is source review, not executed validation. Existing roles remain unchanged
unless the new attribution-only environment switch is explicitly selected.

| Setting | Production direct route fixture | Proposed native attribution role |
| --- | --- | --- |
| Upstream transport | DefaultTransport.Clone + direct pool settings | DefaultTransport.Clone + the same settings |
| Proxy selection | ProxyFromEnvironment; loopback origin bypass | same, do not print proxy environment values |
| Dial timeout / keepalive | 30s / 30s from pinned Go default | same cloned dialer |
| TLS handshake / expect continue | 10s / 1s from pinned Go default | same |
| Keepalives | enabled | enabled |
| Idle pool total / per host | 1024 / 128 | 1024 / 128, one origin |
| Max active per host | 0, admission handles resources | 0, native omits admission |
| Idle connection timeout | at least90s, default90s | 90s |
| Response header timeout | target fixture explicitly10s | 10s |
| Upstream HTTP/2 / compression | cloned ForceAttemptHTTP2=true / compression enabled | same; actual origin is plaintext HTTP/1 |
| Read/write buffer settings | zero fields, pinned Go effective4KiB defaults | same |
| Max response header setting | zero, pinned Go default | same |
| ReverseProxy response pool | production32KiB | same existing pooled32KiB helper, one shard |
| TCP buffers | tcpsocket.Configure(tcp,0), kernel autotuning retained | same configuration helper, no capacity owner |
| TCP NOTSENT LOWAT | requested32KiB on Linux public/origin sockets | same helper on native accepted/upstream TCP sockets |
| Public header read / idle timeout | 5s / 120s | existing configurePublicHTTPServer gives same |
| Public whole read/write timeout | zero | zero |
| Public MaxHeaderBytes | 64KiB | same existing configurePublicHTTPServer |
| Public TLS minimum | TLS1.2 | TLS1.2 |
| Cipher suites / curves | pinned Go defaults | same Go defaults |
| Certificate/key | same fixture files, production selector | same files, native static certificate |
| Public ALPN advertised | acme-tls/1,h2,http/1.1 | same list; capture actual H1 negotiation |
| Attribution H1 client SNI | explicit `public.test` only under the attribution opt-in | same; default and plaintext clients unchanged |
| HTTP route | supported ProtocolHTTP listener, hostpublic.test, /direct target | native ReverseProxy to same target/path |
| HTTPS route | existing ProtocolHTTPS fixture and direct route | native ServeTLS to same target/path |
| CPU/memory | proxy0–3/GOMAXPROCS4/4GiB/no swap | same |

Intentional differences: native has no production admission/lease accounting,
route selection, retries/cache/WAF/access middleware or persisted request events.
Its existing runtime sampler remains enabled. Production uses the existing
simple direct-route fixture, not every possible Sites/policy configuration.
Both roles retain normal TCP autotuning. Native applies common socket settings
without copying admission behavior. Production still uses its certificate
selector, while the attribution-only H1 client sends SNI `public.test` in both
arms so both serve the generated shared fixture certificate. Record actual
negotiated TLS version, cipher, certificate fingerprint and ALPN in the separate
four-condition smoke, and bind both peer fingerprints to the independently
computed fixture certificate fingerprint. The client disables HTTP/2
with an empty TLSNextProto map, so ALPN can be absent; require the same observed
value in both roles and HTTP/1 on the wire instead of inventing a new ALPN
requirement. The default load client and the earlier no-SNI HAProxy/Caddy
reference remain unchanged; attribution results are not a direct causal
comparison to those reference cells.

Sources: direct_transport_pool.go:113–139; direct_transport_resources.go:15–68;
http_defaults.go:14–58; public_tls.go:48–60; proxy_lifecycle.go:621 onward;
tcpsocket/buffers.go and buffers_linux.go; pinned Go src/net/http/transport.go:46–57.
The existing stdlib role uses a sparse literal transport and therefore differs
from this table. The new switch must use the matched clone above without
changing existing default-role behavior or the active calibration archive.

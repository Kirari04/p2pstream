# Native Go attribution diagnostic source checkpoint

This directory is a temporary, test-only diagnostic over immutable base
`248bad099682878fe398c0ebaad235787ef47858`. It does not change production Go
files or the active calibration archive.

## Fixed behavior

`P2PSTREAM_QUALIFICATION_NATIVE_GO_ATTRIBUTION=1` enables the isolated
subprocess listener switch. Without it, the existing `proxy` and `stdlib`
subprocess roles continue to serve HTTPS exactly as before. With it, the
explicit public mode is `http` or `https` for both roles.

The native role uses one `httputil.ReverseProxy`, a single shared 32 KiB
`BufferPool`, and a clone of `http.DefaultTransport` configured to match the
production direct transport: total idle 1024, per-host idle 128, unlimited
active connections, 90 second idle timeout, 10 second response-header timeout,
and the cloned default dial/TLS/expect-continue behavior. Both accepted public
TCP sockets and origin TCP sockets pass through `tcpsocket.Configure(tcp, 0)`.
The public HTTP server uses `configurePublicHTTPServer`; HTTPS uses TLS 1.2
minimum and the production `acme-tls/1,h2,http/1.1` advertisement.

The smoke does not require a nonempty ALPN value. It drives H1 through the
existing load role with the attribution-only client `ServerName` fixed to
`public.test`, so the production selector and native listener both serve the
generated shared fixture certificate. The default load client, plaintext HTTP,
and ALPN advertisement remain unchanged. The smoke requires both HTTPS arms to
observe identical TLS version, cipher suite, first peer-certificate SHA-256,
and negotiated ALPN (empty is allowed), requires each peer fingerprint to
equal the independently computed generated-certificate fingerprint, and
requires HTTP/1 on the wire. Plain HTTP must report no TLS
state. All four smoke conditions require the exact 256-byte response, status
200, HTTP/1 origin, and natural process cleanup.

The accepted matrix is fixed at 24 rows:

- production/native × public HTTP/HTTPS × H1 concurrency 16/128 × three pairs;
- arm order: production→native, native→production, production→native;
- scheme order within each arm: HTTP→HTTPS, HTTPS→HTTP, HTTP→HTTPS;
- one fresh proxy process per pair/arm/scheme, with c16 then c128 in that process;
- 2 second warmup and 10 second timed interval;
- server CPUs 0–3 (first four available), load/origin on the remaining CPUs;
- GOMAXPROCS 4, 4 GiB/no-swap proxy cgroup, idle-per-host 128, 32 KiB pool;
- one ordinary Go1.26.6 test binary for origin, load, production, and native roles.

Every attempted raw row is retained before validation. Exact status, payload,
HTTP versions, attempted/useful counts, and production event conservation are
prerequisites. A cell triggers profiling only when each of its three paired
production/native useful-RPS ratios is below 0.85. The ratio of medians remains
descriptive. Neither value is a release gate or retention decision.

## Intentional differences

The native arm omits admission and ownership leases, route/target selection,
retry/cache middleware, WAF/access checks, and request-event persistence. It
retains the same Go HTTP server defaults, client transport deadlines and pools,
socket configuration, fixture certificate, H1 client, origin, CPU assignment,
and memory envelope. The attribution-only HTTPS client sends SNI `public.test`;
the earlier HAProxy/Caddy reference used the default IP-targeted client without
that explicit SNI, so these rows cannot be used as a direct causal comparison
to the earlier reference. The diagnostic measures this application delta; it does
not replace the HAProxy gate or prove one omitted subsystem causal.

The load-role TLS fields are collected only when
`P2PSTREAM_QUALIFICATION_NATIVE_GO_SMOKE=1`; the 24 timed rows do not take that
extra lock or hash a certificate.

## Bounded lifecycle

`build-native-go-attribution.py` first copies and verifies the checkpoint,
patch, runner, contracts, plan, settings, and builder. It rejects a tmpfs/ramfs
build parent, exports the immutable base, retains the archive and tree hashes,
applies the copied portable patch, and verifies both applied Go file hashes.
Every Git, archive, patch, Go, and metadata command runs in an owned bounded
process group. Compilation uses the absolute Go1.26.6 executable, all four
temporary variables point at the owned disk-backed root, and its timeout is 600
seconds. Build and metadata outcomes are persisted while TERM/INT are ignored;
handlers are restored only after the manifest is durable.

`qualify-proxy-native-go-attribution.py <bundle>` validates the archive,
checkpoint, patch, scripts, binary BuildID, ordinary Go1.26.6 build info, image
ID, and CPU topology. It sanitizes inherited qualification variables and sets
the timed smoke switch explicitly to zero. It runs the four-condition smoke,
then the exact chronological matrix. Every proxy receives a stop file and must
exit naturally before ordinary `docker rm`; container absence and every owned
process group are verified even after a leader exits. Container absence requires
a successful, fully parsed `docker ps --all --no-trunc` inventory and matches
both the owned name and full ID; a daemon or permission error fails closed.
Forced cleanup invalidates
the run. Individual load processes are bounded at 125 seconds, Docker commands
at 30 seconds, and the complete runner at 1200 seconds. Attempted results and a
final outcome are persisted before interrupt handlers are restored.

`native-go-attribution-contracts.py` is a finite twelve-case source contract for
the lifecycle helpers. It covers inherited smoke sanitization, exact child-PATH
executable resolution and missing-tool failure, the all-three
pair trigger, missing/wrong chronology, wrong raw load configuration, a leader-exited descendant, TERM-resistant
timeout cleanup, repeated TERM during cleanup, and fail-closed container absence.
Each case keeps its own result directory.

Planned commands after explicit CPU handoff and source approval:

```text
/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin/go test ./internal/server -run '^TestQualificationNativeAttribution' -count=10
/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin/go test -race ./internal/server -run '^TestQualificationNativeAttribution' -count=5
python3 native-go-attribution-contracts.py
python3 build-native-go-attribution.py
python3 <bundle>/qualify-proxy-native-go-attribution.py <bundle> --smoke-only
python3 <bundle>/qualify-proxy-native-go-attribution.py <bundle>
```

The focused commands run in a clean archive with the overlay and the same pinned
toolchain/temp environment. The smoke-only command is a source contract example;
the full runner already performs the same smoke before any matrix row, so it is
not run twice in the accepted cohort.

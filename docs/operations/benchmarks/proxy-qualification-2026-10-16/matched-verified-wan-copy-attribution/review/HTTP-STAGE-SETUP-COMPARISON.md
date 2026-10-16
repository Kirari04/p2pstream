# Accepted native HTTP stage versus mature external-agent setup

Date: 2026-10-16

This is a source-only comparison. No build, workload, runtime edit, or source
experiment was run. The accepted HTTP-stage source is a historical diagnostic,
not current qualification. Its inspected production transport/relay
integration files are byte-identical to the current files inspected here:
`agent_transport_pool.go`, `public_routing.go`, `agent_request_limiter.go`,
`internal/agent/{agent.go,adaptive_capacity.go}`, and
`internal/tunnel/{growing_conn.go,yamux.go}`. `internal/tunnel/yamux.go` is
integration/configuration code, not the underlying Yamux implementation. The
separate module comparison below finds one current runtime difference, so this
report does not claim that all Yamux runtime code is byte-identical.

## Established facts

### Yamux module provenance and terminal-drain difference

Both source trees replace `github.com/hashicorp/yamux` with the repository's
local `./third_party/yamux` module (`go.mod:5-6` and
`tmp/http-stage-build.jISvFyke/source/go.mod:5-6`). The accepted native build
tree's 38-file module is byte-identical to its preserved source copy and to the
module at pinned diagnostic commit
`364e7744603841f5d306f4c242f3acd2b04183bf`. It is also byte-identical to the
module tree at `4477c0b^` (`01af4d63625f9e7324aeae84065edd8afcc7566e`):
all three have Git tree identity `6d9622d1cb3383f78019ff164452bfd3002e0262`
where represented in Git, and the two preserved directories have the same
normalized 38-file SHA-256 index digest
`5c163e1d911c621fd589b3ffbf191ab707b87b6cf68123bff1a7acd97a798ea4`.

Current production source `8ac4c8df8237e37777ba2fac0da6f949da17bdea`
has module tree `b87b39f4ff13513cfe18b8c4ef9425aea235a07c`.
Compared with the accepted native module, exactly four paths differ: runtime
`stream.go`, test `session_test.go`, new test
`half_close_buffered_drain_test.go`, and `P2PSTREAM.md`. These are exactly the
changes from commit `4477c0b607f197ff3e558858903fdfebe4a0aa3e`
(`Preserve buffered Yamux reads after terminal receive closure`). No other
module path differs. In particular, native and current `session.go`, `mux.go`,
and `const.go` are byte-identical. The accepted/native `stream.go` SHA-256 is
`4d5503619981cf983b99d5b40c9ce46a304ed13d09ad0fbc01f16d80016f9012`;
current is
`a5fc23ea8f1d1c033126b629db47af5ee49c3e9b3c6ddb714ea1ce2416923cd7`.

The current runtime change suppresses a terminal error after a read has
already returned bytes when peer FIN or terminal stream state becomes visible
while the synchronous credit update is blocked
(`third_party/yamux/stream.go:176-193`). It also skips an obsolete per-stream
window update once the stream is remote-closed or closed
(`third_party/yamux/stream.go:440-458`). The accepted native stage therefore
does not exercise this terminal-drain behavior. This is an established common
H1/H2 source difference, but its guarded states are terminal receive states;
source comparison alone does not show it running during a retained transfer or
causing the throughput slowdown.

### Registration and application construction

The mature external-agent fixture creates a real SQLite database, inserts an
enabled agent with a hashed bearer token, constructs the full application,
starts adaptive server capacity, publishes a real public route/listener, and
serves the management API over TLS
(`internal/server/proxy_qualification_test.go:843-880,899-928`). The tunnel
handler authenticates the attacker-controlled ID/token both before and under
the per-agent auth lock, negotiates the group and lane contract, persists the
connection, and publishes the agent through `AgentHub`
(`internal/server/server.go:745-879,1041-1069`). The real agent creates an
adaptive capacity controller, starts its stats reporter, and establishes the
control/bulk lane generation through the management HTTP client
(`internal/agent/agent.go:113-203`).

The native stage calls `NewApp` with no database, replaces the server pool and
capacity manager with fixed fixture-owned instances, constructs a synthetic
`AgentConn`, accepts five physical sockets itself, registers one fixed session
key, and installs the lanes directly
(`tmp/http-stage-build.jISvFyke/source/internal/server/http_stage_qualification_linux_test.go:91-127,194-280`).
It therefore omits DB identity lookup, the management HTTP upgrade, `AgentHub`,
stats reporting, public listener setup, target selection, and health state.

DB registration and connection persistence occur at startup, outside payload
relay. Request-event/SQL timing has already failed its causal gate and is not a
new experiment candidate. The production agent's five-second stats RPC and
capacity snapshots are a separate background difference: the reporter reads
runtime/capacity state and sends an authenticated management request
(`internal/agent/agent.go:1760-1800,1818-1903`).

### Adaptive owners and session resources

This is the clearest ownership difference.

The mature server sets the structural guard to 65,536, enables the adaptive
controller, and starts its sampling loop
(`internal/server/proxy_qualification_test.go:851-859`). During authenticated
session setup it reserves initial aggregate pool/metadata bytes against the
adaptive receive-window owner; later aggregate growth uses the same owner
(`internal/server/server.go:884-945,1013-1025`). The adaptive loop samples at
the configured interval and can reclaim idle transports only on pressure
transitions (`internal/server/agent_stream_capacity_adaptive.go:46-104`).

The mature agent also builds an adaptive controller from the production stream
resource model and forces an initial resource sample
(`internal/agent/agent.go:142-171`). It reserves control, initial bulk-pool,
accept/rejection metadata, and later aggregate growth through
`agentTunnelCapacityRuntime`
(`internal/agent/agent.go:951-999,1103-1141`). Receive-resource reservation
holds the agent capacity mutex while obtaining the controller snapshot and
placing the named lifetime charge
(`internal/agent/adaptive_capacity.go:280-355`). Its pressure watcher snapshots
the adaptive runtime every 250 ms, while the controller's default sample
interval is 100 ms
(`internal/agent/agent.go:1769-1799` and
`internal/sysmetrics/memory_pressure.go:118-130`).

The native server uses a fixed capacity of 16, does not call
`StartAdaptiveTunnelCapacity`, and accounts initial/growth pool bytes only in a
fixture `TransportCeilingLedger`
(`tmp/http-stage-build.jISvFyke/source/internal/server/http_stage_qualification_linux_test.go:103-118,247-280`).
The native agent uses `newAgentTunnelCapacityRuntime(16, false, nil)` plus a
separate 2 GiB fixture pool ledger
(`tmp/http-stage-build.jISvFyke/source/internal/agent/http_stage_qualification_linux_test.go:106-123,373-415` and
`tmp/http-stage-build.jISvFyke/source/internal/tunnel/transport_ceiling_qualification_linux.go:35-46`).
With a nil controller, `tryReserveOwnedReceiveResource` returns a no-op lease,
so the fixture capacity runtime does not execute the production adaptive
sample/spend path (`internal/agent/adaptive_capacity.go:280-286`).

Thus the accepted stage verifies the production HTTP transport, stream wrapper,
Yamux growth machinery, and agent relay under a hard external pool budget, but
does not verify the current adaptive owner/sampler path at either endpoint.

### Flow-control and per-stream configuration

The byte and allocation windows are matched. Both sides use
`DefaultAggregateFlowControlConfig`, a 512 KiB initial byte window, the 1 GiB
maximum, the aggregate session pool, and `GrowingConn`
(`internal/tunnel/protocol.go:88-106` and
`internal/tunnel/yamux.go:27-34,264-295`). Zero-valued mature fixture window
options normalize to that same 1 GiB default
(`internal/tunnel/yamux.go:411-422`). Both server sessions reject peer-created
streams with `MaxIncomingStreams=-1`.

The hard incoming limit differs on the agent: native uses 16, while the mature
adaptive negotiation can advertise up to 65,536
(`tmp/http-stage-build.jISvFyke/source/internal/agent/http_stage_qualification_linux_test.go:373-415` versus
`internal/agent/agent.go:1103-1141`). At concurrency one this is an admission
ceiling difference; the source does not preallocate one data buffer per allowed
stream, so it is not by itself a demonstrated throughput mechanism.

### Authentication and physical control setup

The native stage establishes direct TLS sockets and authenticates each lane
with a fixture key before constructing Yamux
(`tmp/http-stage-build.jISvFyke/source/internal/server/http_stage_qualification_linux_test.go:194-244` and
`tmp/http-stage-build.jISvFyke/source/internal/agent/http_stage_qualification_linux_test.go:329-369`).

The mature path uses verified management TLS, forces HTTP/1.1 for the tunnel,
sends an authenticated HTTP Upgrade with the complete capacity/flow/lane
contract, and constructs Yamux over the upgraded body
(`internal/agent/agent.go:793-869,917-1028,1103-1134`). On the server it hijacks
the management connection and places a read gate in front of the TLS connection
until `GoAway` has fenced unsolicited SYNs
(`internal/server/server.go:955-1012`). Both paths then use the same negotiated
control Yamux config, aggregate bulk config, `GoAway` direction fence, and
production agent stream-serving function.

The mature HTTP-upgrade carrier wrappers and background management RPCs are
therefore production-only, protocol-independent activity. Source inspection
does not show a pacing loop or idle timer in those wrappers. They remain a
possible trace boundary if both public protocols are slow, not an established
cause.

### Origin and lane schedule

Both origins are plaintext HTTP/1.1 over a real TCP socket. The mature runner
starts the origin as a separate process in the agent namespace without setting
the optional origin TLS flags
(`scripts/qualify-proxy-agent-growth.sh:430-448`); its handler reports the
origin protocol/connection and serves exact-length bulk bodies
(`internal/server/proxy_qualification_test.go:674-733,783-803`). The native
stage embeds its HTTP server in the agent fixture process, uses an exact target
allowlist, and validates phase/order and content
(`tmp/http-stage-build.jISvFyke/source/internal/agent/http_stage_qualification_linux_test.go:233-327`).
The remaining concrete difference is process ownership: separate Go runtime,
heap, GC, and scheduler in the mature fixture versus a shared agent runtime in
the native stage. Buffer and flush semantics were compared previously and are
outside this report.

Lane scheduling is effectively matched. The native schedule closes each of
eight conditioning pools, then uses lanes 1/2/3/4 once and revisits the same
four keys in the same direction-transition pattern
(`tmp/http-stage-build.jISvFyke/source/internal/tunnel/transport_ceiling_qualification_linux.go:89-105`).
The mature runner also closes after each of eight warmups, then issues retained
download/upload followed by three alternating download/upload repetitions
(`scripts/qualify-proxy-agent-growth.sh:1066-1085`). Production
`transportLane()` rotates through live bulk lanes and the lane is part of the
transport key (`internal/server/agent_tunnels.go:90-119` and
`internal/server/agent_transport_pool.go:26-37,109-124`). Native uses an
explicit lane key; mature selection reaches the same eight-phase lane order.
The different numeric route-target IDs do not change the one-pool-per-lane
shape.

## Inferences if both public H1 and H2 retain the slowdown

Both protocols being slow would reject a public-H2-specific mechanism. This
setup comparison leaves three concrete common boundaries that the native stage
does not exercise:

1. **Adaptive ownership/sampling at both endpoints.** A production growth
   reservation can contend with resource sampling under the same capacity
   mutex, whereas the native agent's fixed nil-controller reservation is a
   no-op and the native server uses only its external ledger. This is a
   plausible short blocking mechanism, but healthy pressure, zero rejection,
   and successful growth do not measure reservation wait time. It is not a
   causal finding and does not justify repeating rejected allocation-unit,
   growth-policy, refund, or pressure variants.
2. **The real management-upgrade carrier and background management work.** The
   mature Yamux sessions run over the HTTP-upgraded management connection and
   coexist with adaptive snapshots/stats RPCs. The native stage runs Yamux
   directly over its authenticated TLS sockets. This could matter to both
   public protocols, but source inspection shows no deliberate throttle; only
   timing at the physical connection and adaptive reservation boundaries would
   distinguish it.
3. **Current terminal-drain handling in the local Yamux fork.** The accepted
   native source predates `4477c0b`, while current production contains it. Its
   two runtime guards act only after remote-close/closed state is visible, so a
   trace would first have to establish such a transition during an affected
   transfer before this becomes a plausible payload-time mechanism. The source
   difference alone is not causal evidence.

The real DB identity setup, identical aggregate windows, plaintext HTTP/1
origin protocol, and lane schedule do not supply a new payload-time mechanism.
The separate-origin-process boundary is concrete but points away from simple
shared-runtime contention because the healthy native stage shares its origin
with the agent process. No causal claim follows without matched timing
evidence.

The already rejected DATA/control emission, refund queue/epoch, TCP low-water,
allocation/growth-policy, and SQL/event variants remain rejected and are not
reopened by this comparison.

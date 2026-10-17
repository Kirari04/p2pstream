# Tiny smoke 2 pooled transport lifetime investigation

Date: 2026-10-16

Scope: read-only inspection of the production source at
`8ac4c8df8237e37777ba2fac0da6f949da17bdea`, the frozen protocol diagnostic
runner, and the two `tiny-smoke-2` result files. The branch currently points at
the evidence-only commit `15b79d00e717fe9ab29d9c5f90a600875e5f5cef`; its
diff from `8ac4c8d` contains only documentation/evidence paths and no runtime
source changes. No workload, build, runtime edit, or source experiment was run.

## Conclusion

`tiny-smoke-2` does not contain a post-conditioning same-key reuse attempt.
Every `GotConn.Reused=false` observation is expected from the fixture cadence:

1. Transfers 1 through 8 are conditioning transfers. Immediately after each
   one, the runner calls `close_idle_agent_transports` (`generated/qualify-proxy-agent-growth-wan-protocol.sh:1216-1220`).
2. The admin query is deliberately destructive: it calls
   `app.AgentTransports.closeAll()` (`internal/server/proxy_qualification_test.go:568-570`).
   `closeAll` retires every matching pool entry and invokes
   `CloseIdleConnections` (`internal/server/agent_transport_pool.go:565-567,584-599`).
3. The production key includes `TunnelLane` and the selected lane comes from a
   round-robin `transportLane()` call
   (`internal/server/agent_transport_pool.go:26-37,109-124` and
   `internal/server/agent_tunnels.go:90-119`).
4. After the last explicit close, transfers 9 through 12 visit lanes 1, 2, 3,
   and 4 exactly once. Transfer 13, which would be the first revisit of the
   lane-1 key created by transfer 9, is absent.

The repeated remote address every four rows identifies the stable physical
Yamux lane, not reuse of the logical HTTP connection. The two result files show
the same four-lane cycle. Rows 1/5/9 map to lane 1, 2/6/10 to lane 2, 3/7/11 to
lane 3, and 4/8/12 to lane 4. The first two visits in each group are separated
by explicit whole-pool closes. The third visit creates that lane's first
post-close pooled stream.

The mapping is exact for this fixed route and agent. `publicRouteTargetTransport`
selects the lane once, stores it in `agentTransportKey.TunnelLane`, and passes
that same lane into the pooled transport
(`internal/server/agent_transport_pool.go:109-124,191-196`). Its dialer passes
the preferred lane to `dialViaAgentWithCapacity`
(`internal/server/agent_transport_pool.go:495-503`), which uses that lane's
session without another round-robin selection
(`internal/server/public_routing.go:874-900`). The server connection logs map
the trace remote endpoints to lanes as follows:

| protocol | lane 1 | lane 2 | lane 3 | lane 4 |
| --- | --- | --- | --- | --- | --- |
| H1 | `10.212.0.2:48658` | `10.212.0.2:48642` | `10.212.0.2:48628` | `10.212.0.2:48614` |
| H2 | `10.212.0.2:33388` | `10.212.0.2:33424` | `10.212.0.2:33412` | `10.212.0.2:33402` |

Because the agent, route target, origin, TLS policy, and timeout are fixed in
each run, that lane mapping identifies the complete pool key. The tuple does
not identify a logical HTTP connection: successive logical Yamux streams on
one physical lane intentionally expose the same underlying local/remote tuple.

The strongest internal corroboration is the post-close capacity sequence in
both protocols: `agent_capacity.in_use` is 1, 2, 3, 4 on rows 9 through 12.
Those successful streams accumulate across distinct lane keys; they do not
disappear after handler return. Every row reports pressure `healthy`,
`rejected_pressure=0`, and `rejected_fixed_limit=0`.

`wait_aggregate_lanes_grown_and_quiescent` is not the closer. It only polls the
server and agent snapshots and returns when the lane accounting is quiescent
(`generated/qualify-proxy-agent-growth-wan-protocol.sh:524-562`). The separate
`close_idle_agent_transports` helper constructs the destructive admin query
(`...sh:514-522`).

## Runtime closure mechanisms checked

The production `http.Transport` enables keep-alives and sets a 30 second idle
timeout (`internal/server/agent_transport_pool.go:457-504`). A stable key and
agent generation returns the existing pooled entry
(`internal/server/agent_transport_pool.go:159-169`). Successful EOF marks the
entry reusable and idle; it does not retire it
(`internal/server/agent_transport_pool.go:348-425`).

The concrete early-close paths do not match these rows:

- Explicit pool retirement closes idle transports. This is exactly what the
  conditioning helper invokes. Normal successful completion does not invoke
  it (`internal/server/agent_transport_pool.go:326-345,391-413`).
- Adaptive periodic reclaim runs only on a transition into soft or critical
  pressure (`internal/server/agent_stream_capacity_adaptive.go:46-104`). Both
  runs stayed healthy. Demand-driven reclaim is reached only after pooled
  capacity admission errors (`internal/server/public_routing.go:935-951` and
  `internal/server/public_retry_transport.go:909-937`); all 24 requests
  completed successfully without capacity rejection.
- Pool-size eviction occurs only when retained entries reach the pooled entry
  limit (`internal/server/agent_transport_pool.go:174-203`). The observed
  admission limit is over 23,000 while this fixture creates at most four
  post-close keys.
- Tunnel disconnect retires the affected lane
  (`internal/server/agent_tunnels.go:200-217`). The server logs show all four
  bulk lanes remain connected until final fixture teardown.
- The dial-time request-context watcher only protects opening and the open
  handshake. Its `handshakeDone` channel is closed by a defer before a
  successful `DialContext` return, so later request cancellation is no longer
  watched (`internal/server/public_routing.go:1022-1110`).
- On the agent, each stream handler receives the tunnel-session context, not
  the public HTTP request context (`internal/agent/agent.go:1359-1375`). The
  relay closes the stream and origin only when one copy finishes or that
  session context ends (`internal/agent/agent.go:1641-1707`). There is no
  per-request idle cleanup timer in this path.

The accepted native HTTP stage uses the same production pool constructor and
`RoundTrip` path with an explicit lane key
(`tmp/http-stage-build.jISvFyke/source/internal/server/http_stage_qualification_linux_test.go:284-350`).
Its schedule deliberately closes conditioning phases, creates one persistent
connection on each lane in phases 8-11, then revisits those same keys in phases
12-15
(`tmp/http-stage-build.jISvFyke/source/internal/tunnel/transport_ceiling_qualification_linux.go:89-105`).
The preserved phase evidence reports `Reused=true`, the same connection and
origin identities, and roughly 24 second idle times for phases 12-15. That is
consistent with the 30 second idle timeout and shows that new per-request
contexts do not inherently destroy successful pooled streams.

## Small falsifiable next probe

If another runtime probe is authorized, the minimum discriminating extension
is to retain the existing verified H1/H2 setup and explicit conditioning
closes, then issue one additional post-close transfer after row 12. Round-robin
selection makes row 13 revisit lane 1 and the exact key first created by row 9.
Record the existing `GotConn`, `PutIdleConn`, origin connection ID, pool-entry
identity/count, capacity state, and reclaim counters before and after it.

Expected if pooling is healthy: row 13 has `GotConn.Reused=true`, `WasIdle=true`,
the same logical connection and origin connection IDs as row 9, no intervening
reclaim/retirement, and the post-close in-use count remains four. A new logical
or origin connection with unchanged key, no close request, no reclaim, no
pressure transition, and a live lane would falsify that expectation and would
justify tracing the transport close caller. Running rows 13-16 would cover all
four lane keys and match the accepted native-stage reuse schedule, but row 13
alone answers whether the current observation was caused by the 12-row
schedule.

## Evidence identities

- H1 results: `eb8786c3b239bb5284e37b2c3c2406284778c17c1a41f32aaf5fb8ce0edec688`
- H2 results: `98e8b27d3f8bef950296a861d7dccb1bfbdcbc8053474f8537185eee6574e691`
- Generated runner: `dd16eb3f484fdfc3196c1845ecac8f85a83d4b7d25bd90463dd5321a63e726b3`

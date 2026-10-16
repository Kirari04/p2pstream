# Copy-timing decision map for current production source

Date: 2026-10-16

Scope: read-only mapping of runtime source
`8ac4c8df8237e37777ba2fac0da6f949da17bdea`. No source, test, traffic, or
instrumentation change was made. The pending attribution record measures an
existing response-body `Read`, downstream recorder `Write`, and the gap from
one `Read` return to the next `Read` invocation. It does not observe inside
Yamux or the HTTP/2 scheduler.

The accepted native HTTP-stage fixture is not a same-source control: its local
`third_party/yamux` module predates `4477c0b`. Its healthy result can bound the
layers it omitted, but is not used below as byte-identical runtime evidence.

## Source facts

### The public copy loop is serial

The public route installs the agent round tripper in `httputil.ReverseProxy`
and supplies a 32 KiB buffer (`internal/server/public_routing.go:581-595,728-731`;
`internal/server/reverse_proxy_buffer_pool.go:8-42`). Pinned Go 1.26.6 executes
one `src.Read(buf)`, then one `dst.Write(buf[:nr])`, and does not start the next
read until that write returns
(`/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/src/net/http/httputil/reverseproxy.go:723-753`).

The production body wrappers forward the caller's exact slice
(`internal/server/agent_transport_pool.go:415-454`). The public response
recorder directly calls its underlying `ResponseWriter.Write` and then updates
its byte counter (`internal/server/traffic_trace_request.go:34-41`). Thus the
pending probe's upstream-read duration is the full response-body `Read` call,
and its downstream-write duration contains the underlying public writer call.

For H2, the handler writer ultimately waits for the scheduled DATA write result
before returning (`h2_bundle.go:5147-5186,6728-6737` in the pinned toolchain).
Its 4 KiB handler buffer can be bypassed by larger writes, so handler write size
does not establish DATA-frame size (`h2_bundle.go:4038-4061,6642-6648`). A long
measured H2 `Write` is nevertheless time spent at the public downstream
boundary, including scheduler or flow-control wait below that call.

### A measured upstream `Read` contains receive-credit work

On a download, the server's HTTP transport reads a capacity-managed
`GrowingConn` over the selected Yamux bulk lane
(`internal/server/public_routing.go:1001-1020` and
`internal/server/agent_request_limiter.go:40-65,89-97`). The following work is
therefore inside the measured upstream `Read`:

1. Yamux waits when no receive bytes are buffered
   (`third_party/yamux/stream.go:131-160,195-241`).
2. It copies buffered aggregate chunks to the 32 KiB caller buffer. Fully
   drained 4 KiB units return to the session pool, add matching stream/session
   refund debt, and wake the session refund publisher
   (`third_party/yamux/stream.go:162-174,266-291`;
   `third_party/yamux/aggregate_window.go:395-427`). Adding that debt is
   synchronous; its physical publication is performed by the session worker
   (`aggregate_window.go:538-559,576-639,666-746`).
3. Yamux then runs the ordinary per-stream byte-window update before returning.
   Small updates are batched below one quarter of the receive window; a selected
   update waits for the physical send result
   (`third_party/yamux/stream.go:426-511`).
4. `GrowingConn.Read` observes completed bytes only after the underlying Yamux
   read returns. When two demand samples justify growth, it reserves resources
   and synchronously publishes session-allocation, stream-allocation, then byte
   growth before returning to `ReverseProxy`
   (`internal/tunnel/growing_conn.go:69-121,148-212`).

On the agent sender, a Yamux write waits if its byte credit is zero or if the
aggregate session/stream allocation has no credit
(`third_party/yamux/stream.go:331-399` and
`third_party/yamux/aggregate_window.go:211-247`). Receiver read cadence is thus
upstream credit cadence: no next public read means no next consumption-driven
refund or growth opportunity.

### Downstream time also affects growth decisions

The growth estimator deliberately measures consumption over wall time,
including time outside `Read`; it has no independent timer. It needs two
supporting samples, resets after a long pause, and never shrinks credit
(`internal/tunnel/receive_window_growth.go:13-82`). Because the copy loop is
serial, an H2 downstream `Write` stall lengthens the next consumption epoch and
can lower or delay the next growth proposal. A smaller H2 receive limit can
therefore be an effect of downstream pacing rather than evidence that upstream
credit was the initiating stall.

For aggregate bulk lanes, a growth proposal calls the server's adaptive
receive-window owner (`internal/server/server.go:1013-1017`). Reservation may
refresh the resource controller and waits for any concurrent sample before
spending the named lifetime charge
(`internal/server/agent_stream_capacity.go:366-465,1374-1407`). This cost is
inside the same measured upstream `Read`. Ordinary chunk refunds do not call
the adaptive sampler. The periodic server sample runs independently and only
reclaims idle transports on pressure transitions
(`internal/server/agent_stream_capacity_adaptive.go:46-115`).

### Limits of existing row pool observations

`AggregateReceiveWindowStats` takes one session-ledger lock and reports receive
limit, current available credit, total/free units, derived buffered units, and
pending plus publishing refund bytes
(`third_party/yamux/aggregate_window.go:99-133`). The qualification endpoint
copies those values by lane (`internal/server/proxy_qualification_test.go:639-655`).

The exported initial row is not a reliable immutable lane snapshot. The sampler
shallow-clones the outer `AgentTunnels` slice, whose nested `Lanes` slices still
share backing storage; the high-water accumulator is seeded from the same lane
slice and later mutates its fields in place
(`internal/server/proxy_qualification_test.go:1956-1965,2244-2277`). This can
turn the reported initial lane into a mixture of initial and later maxima. Do
not use it to infer starting credit. The separately captured final row can show
that the selected lane returned to full availability (`available == limit`,
`free == total`, zero buffered/pending). That rejects residual post-transfer
starvation, not a transient stall.

The row's `agent_tunnels_high_water` is assembled from independent per-field
maxima sampled approximately every 20 ms
(`internal/server/proxy_qualification_test.go:1949-1979,2217-2278`). It is not
one coherent instant and records the maximum, not the minimum, available
credit. It can prove that buffering or pending refund was observed, but cannot
show that either overlapped a particular long read/write or that available
credit reached zero. Boundary pool statistics alone therefore cannot separate
Yamux credit exhaustion from origin, physical-network, scheduler, or internal
credit-publication delay within an upstream `Read`.

## Observable decision invariants

Apply these per transfer and compare the same phase/lane/order between H1 and
H2. Do not pool away the reproduced H2 retained row.

1. **Downstream H2 `Write` blocking.** If the H2 wall-time excess is carried by
   downstream-write total and long-write buckets while upstream-read total is
   near the healthy H1 value, the delay is at the public writer boundary. The
   longest write timestamps should fall inside corresponding inter-read gaps.
   Buffered/pending pool high water is compatible with this result because the
   serial loop cannot consume/refund more upstream data until `Write` returns.
2. **Upstream-side blocking.** If H2's excess is carried by upstream-read total,
   while write total and non-write gap overhead remain near H1, the delay occurs
   before the response-body `Read` returns. That is compatible with sender
   credit exhaustion, synchronous byte-credit publication, adaptive growth
   reservation/publication, origin delivery, or physical tunnel delay. Existing
   boundary pool maxima do not distinguish those mechanisms.
3. **Downstream-to-credit feedback.** If H2 first shows long writes/gaps and
   later shows smaller/slower lane growth or long reads, source ordering supports
   downstream pacing delaying consumption-driven refunds/growth. It does not
   support labeling the later credit symptom as the initiating cause. The fixed
   longest-stall timestamps can establish ordering, while aggregate totals
   cannot.
4. **Mixed read/write excess.** Material excess in both totals is evidence of a
   feedback loop or two boundaries, not enough to assign primacy. A time-aligned
   internal credit state would be required for a credit-specific claim; the
   componentwise row high water is insufficient.
5. **Gap-only excess.** Inter-read gaps begin at a read return and include the
   intervening write plus copy-loop/scheduling work. They overlap write time and
   must never be added to read/write totals. Gap excess without write excess is
   outside the measured upstream `Read`; it is not evidence of upstream-credit
   blocking. A final write may occur after the last measured gap.
6. **Fully replenished final pools.** Full final availability and zero
   buffered/pending state prove cleanup and credit conservation at the boundary.
   They do not falsify a transient mid-transfer credit wait. Conversely, a
   sampled pending-refund or buffered-unit maximum proves occurrence only; its
   size and lack of time alignment prevent a duration or causal inference.

As a measurement sanity check, read total plus write total must not exceed the
handler interval. The residual includes response-header work, loop overhead,
finalization, and other unmeasured handler time. Inter-read gap remains
non-additive. Upload rows measure only their small response-body copy and say
nothing about request-body upload pacing.

These observations localize a blocking boundary, not the mechanism below it.
A long `Write` cannot distinguish H2 scheduling, client flow-control, kernel
socket backpressure, or TCP delivery, and it does not reveal DATA-frame sizes.
A long `Read` cannot distinguish an empty Yamux buffer, sender credit exhaustion,
refund/window-update publication, adaptive growth work, origin delivery, or the
physical tunnel. The fixed histograms and top-eight stalls contain no per-chunk
credit/buffer/socket state; uncaptured stalls have no timestamps. Even when two
captured timestamps establish order, they do not prove which scheduler or
flow-control event caused the other.

## Hypotheses, pending the attribution rows

- **H2 sink hypothesis:** the 14.24 MB/s retained download will have much larger
  write total and long-write stalls than matched H1, with read total near H1.
  This would localize the first-order delay to the H2 public writer boundary.
- **Upstream-credit-compatible hypothesis:** the slow H2 row will instead have
  much larger read total, prompt writes, and no gap-only excess. Pool boundary
  evidence may show observed refund/buffer activity, but cannot by itself prove
  credit exhaustion; this result would narrow the next question to inside the
  upstream `Read` boundary.
- **Growth-feedback hypothesis:** long H2 writes will precede reduced growth or
  later read stalls because growth intentionally includes time outside `Read`.
  This is a source-supported feedback mechanism, not yet an observed cause.

The rejected DATA emission, refund batching/queue/epoch, TCP low-water,
allocation/growth-policy, SQL, and request-event variants remain closed. This
map interprets the pending observation only; it proposes no repeat or runtime
change.

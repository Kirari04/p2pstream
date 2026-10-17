# Public copy and receive-credit feedback map

Scope: read-only source analysis of the audited current diagnostic source and pinned Go 1.26.6. This report does not change runtime code, resource policy, window constants, or qualification traffic.

## Evidence first

### The 8 KiB reader is the public load client

The qualification origin does not produce 8 KiB chunks. It writes a repeated 35-byte pattern in blocks of 35,840 bytes and declares the complete response `Content-Length` (`internal/server/proxy_qualification_test.go:64`, `:720-733`). The public load client allocates an 8 KiB buffer and repeatedly calls `resp.Body.Read(buf)` with no sleep in `growth-download` (`:1656-1674`). Thus 8 KiB is the maximum application read presented by the load client; an individual read may return less.

The public server installs a 32 KiB `httputil.ReverseProxy` buffer pool (`internal/server/reverse_proxy_buffer_pool.go:8-35`) and does not set `FlushInterval` (`internal/server/public_routing.go:595-734`). Go's copy loop performs one `src.Read` followed synchronously by one `dst.Write`, and cannot begin the next source read until that write returns (pinned `src/net/http/httputil/reverseproxy.go:696-753`). The sustained records agree: the selected 256 MiB responses used 8,193-8,471 upstream reads, approximately one 32 KiB read per call, rather than roughly 32,768 8 KiB reads.

No response-retry pump adds read-ahead in this qualification. No public retry rule is registered by the fixture, and the runtime explicitly classifies a nil rule as `response_body_streamed` (`internal/server/public_retry_transport.go:704-707`). The response body is therefore presented directly to the unchanged ReverseProxy copy loop.

### Flush behavior does not expose an H1/H2 discrepancy

The origin supplies a known `Content-Length`. With `ReverseProxy.FlushInterval == 0`, Go returns a zero flush interval and does not wrap the downstream writer in `maxLatencyWriter` (pinned `reverseproxy.go:677-720`). There is no per-chunk explicit flush in either arm.

For H1, the handler path has 2 KiB and 4 KiB buffered writers, but a roughly 32 KiB write takes the `bufio.Writer` large-write fast path and reaches the connection writer synchronously (pinned `src/net/http/server.go:341-406`, `:1593-1645`; `src/bufio/bufio.go:216-239`). For H2, the handler has a 4 KiB buffer, but the same large-write fast path reaches `writeDataFromHandler`; that call queues DATA and waits for its completion or stream/connection failure (pinned `src/net/http/h2_bundle.go:4038-4061`, `:5147-5186`, `:6728-6737`, `:6993-7031`). H2 DATA is split by stream/connection flow and frame size, and zero available flow prevents progress (`h2_bundle.go:11206-11276`).

This is a real H2 application-flow-control boundary, but it is expected standard-library behavior rather than a discovered p2pstream implementation error. H1 can also block synchronously at its connection write; it does not have the additional per-stream HTTP flow window.

### Exact 8 KiB H2 feedback path

The pinned Go H2 client advertises a default 4 MiB per-stream receive window (`h2_bundle.go:7331-7340`, `:8082-8095`, `:8143-8158`). After `resp.Body.Read(p)` consumes bytes, it adds the actual returned `n` to connection and stream inflow and synchronously writes and flushes WINDOW_UPDATE when the threshold is met (`:9845-9894`). The threshold is 4 KiB (`:1471-1514`), so a full 8 KiB qualification read normally makes an 8 KiB stream update immediately.

Once the H2 stream window constrains a roughly 32 KiB ReverseProxy `Write`, a single 8 KiB client read need not complete that write. Up to four full 8 KiB reads can be needed to release the whole 32 KiB write. Until the write completes, ReverseProxy cannot issue its next upstream read.

### Where tunnel receive credit is returned

The HTTP/1 origin response over the Yamux stream is a known-length body. Go wraps the transport's buffered reader in `io.LimitedReader` (`src/net/http/transfer.go:561-579`); when that buffer is empty and ReverseProxy asks for 32 KiB, `bufio.Reader` reads directly into that buffer (`src/bufio/bufio.go:216-239`). The server's connection wrapper serializes this call and delegates to `GrowingConn.Read` (`internal/server/agent_request_limiter.go:89-96`).

`GrowingConn.Read` first calls the underlying Yamux stream. After a successful nonterminal read it runs its growth observer before returning the bytes to net/http (`internal/tunnel/growing_conn.go:69-80`). Inside Yamux, `Stream.Read` removes buffered data, then synchronously calls `sendWindowUpdate` before it returns (`third_party/yamux/stream.go:123-193`). Normal per-stream updates are batched until the accumulated delta reaches one quarter of the current receive limit; a published update waits for the Yamux control send result (`stream.go:426-505`). Aggregate storage/session refunds occur in the same read path before this per-stream update (`stream.go:162-177`), but their policy internals are outside this report.

The ordering is therefore:

1. ReverseProxy asks for up to 32 KiB from the origin response.
2. Yamux consumption makes receive credit returnable and may synchronously publish it.
3. `GrowingConn` observes the successful bytes and may publish bounded growth.
4. The read returns to ReverseProxy.
5. ReverseProxy synchronously writes those bytes to the public H1/H2 response.
6. No further Yamux consumption or credit return occurs until that public write returns.

This is a concrete feedback path: downstream public backpressure delays the next tunnel read, which delays the next opportunity to replenish tunnel receive credit. It does not establish that downstream backpressure initiated the WAN slowdown.

### Current measurements confirm serialization but do not prove initiation

For sustained sequences 9/11/14/15, copy attribution recorded the following totals in milliseconds:

| Protocol | Seq | downstream Write | inter-read gap | gap minus Write | upstream Read |
|---|---:|---:|---:|---:|---:|
| H1 | 9 | 324.997 | 330.269 | 5.272 | 8295.063 |
| H1 | 11 | 314.723 | 319.800 | 5.077 | 7207.333 |
| H1 | 14 | 324.489 | 329.342 | 4.853 | 8308.765 |
| H1 | 15 | 313.933 | 318.832 | 4.899 | 7120.170 |
| H2 | 9 | 679.594 | 685.922 | 6.328 | 8742.359 |
| H2 | 11 | 660.120 | 666.443 | 6.324 | 8762.428 |
| H2 | 14 | 650.293 | 656.310 | 6.018 | 8847.299 |
| H2 | 15 | 577.386 | 582.868 | 5.482 | 6913.064 |

The near equality of inter-read gaps and downstream writes is direct evidence that the write occupies almost the whole pause before the next tunnel read. H2 adds about 264-355 ms of total downstream write/gap time in every selected row. Sequence 15 nevertheless remains fast and has zero sender credit wait. Public H2 write overhead is therefore a persistent feedback term, but it is not sufficient by itself to explain why only direction-cold sequences 9, 11, and 14 accumulate additional sender credit wait.

### Legacy and aggregate byte-window targets

There is no configured aggregate-versus-legacy byte-window target mismatch in the audited production setup:

- A new Yamux stream begins with 256 KiB immediately available byte credit (`third_party/yamux/stream.go:83-107`; `internal/tunnel/yamux.go:13-17`).
- `InitialReceiveWindow` limits the initial byte receive target to 512 KiB (`internal/tunnel/yamux.go:27-34`, `:178-181`). Both the legacy and aggregate connection setup use that value (`internal/agent/agent.go:929-932`, `:1120-1128`; `internal/server/server.go:935-948`, `:1114-1122`).
- Both use the same configured maximum byte-window policy, 1 GiB by default, through the same `GrowingConn` estimator. Aggregate mode additionally has independent 512 KiB per-stream allocation credit and a 2 MiB initial session pool (`internal/tunnel/protocol.go:43-47`); those are separate from the per-stream byte-window target.

The sustained H1/H2 diagnostic used aggregate mode in both arms. Its post-transfer byte receive limits differ dynamically (H1 selected rows: 24,641,536; 24,051,712; 24,772,608; 24,051,712 bytes. H2: 23,199,744; 21,889,024; 23,265,280; 21,889,024 bytes), despite identical configured initial/maximum targets. These are estimator outcomes under different observed cadence, not evidence of an aggregate-versus-legacy configuration discrepancy.

## Evidence gaps

- The copy observer aggregates each side independently. It proves the serial copy boundary, but it does not join a particular downstream Write stall to the immediately following Yamux credit publication or sender wake.
- H2 client WINDOW_UPDATE timing is established by pinned source, not captured in the current transfer records.
- Yamux update batching means one 32 KiB proxy read need not publish byte credit immediately when one quarter of the current receive limit is larger than 32 KiB. Current records retain totals and growth snapshots, not every consumption-to-update interval.
- Durations from the load client, server copy loop, server Yamux receiver, and agent sender have different process clocks and instrument boundaries. They cannot be added into one exact wall-time partition.
- H1 connection write blocking depends on kernel/socket state; an H1 client pause is not a deterministic control unless socket policy is changed, which this investigation does not propose.

## Smallest discriminating local test

Add one bounded qualification-only sibling of the existing H1/H2 copy-attribution fixture; do not change production code or any window/resource constant.

Use the actual Go H2 front server and its default 4 MiB stream window, the existing 32 KiB ReverseProxy buffer, and a custom RoundTripper whose response body is an actual `GrowingConn` over one local Yamux stream configured with the production 512 KiB initial byte target. Send a fixed payload slightly larger than the standard H2 stream window. The client must use the existing 8 KiB read loop, but reads are released by a test barrier.

The contract should establish only observable ordering:

1. With client reads held, H2 eventually blocks an in-flight public write; upstream `GrowingConn.Read` calls and peer write progress stop after bounded existing buffers fill.
2. Releasing 8 KiB reads advances H2 WINDOW_UPDATE in the pinned implementation. The in-flight roughly 32 KiB proxy write must finish before the next upstream read can start.
3. Continued releases eventually cause the existing Yamux batching threshold to publish receive credit and unblock the peer writer. Assert progress and byte conservation, not elapsed-time thresholds.
4. A control using the same Yamux body with an unblocked downstream sink continues reads and credit returns. This isolates downstream pacing from the unchanged estimator and resource policy.
5. All goroutines, streams, sessions, and servers join naturally; error, early close, and short payload fail the test rather than being normalized.

This test can establish that H2 consumption is capable of causing the observed feedback ordering. It still cannot show that this feedback initiated the WAN-only direction-cold slowdown. If it passes, the implementation is behaving as the source predicts and no buffering/window change is justified. If it fails at a documented ordering invariant, that gives a specific implementation discrepancy to fix.

No production fix is supported by the present evidence. Increasing buffers, changing the 32 KiB copy size, changing flush policy, or changing receive-window growth would mix policy changes with diagnosis and would repeat already rejected allocation/growth directions.

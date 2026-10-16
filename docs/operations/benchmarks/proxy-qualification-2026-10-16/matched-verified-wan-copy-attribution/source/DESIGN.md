# Matched public copy-path attribution proposal

Status: source-only architecture draft. No build, tests, smoke, or sustained traffic are authorized by this document. The approved `wan-protocol-source` tree and its evidence remain frozen.

## Question

For the current-source matched H1/H2 diagnostic, partition ReverseProxy response-copy time into:

- time blocked in the reader presented to `httputil.ReverseProxy.copyBuffer`;
- time blocked in the existing public `proxyResponseRecorder.Write` call to the downstream client;
- gaps from one upstream `Read` return to the next `Read` invocation, which include the intervening downstream write and the unchanged copy-loop bookkeeping. This overlaps write time and is never summed with Read and Write as an independent component; a final `n+EOF` write can occur after the last measured gap.

This is attribution evidence only. It does not change qualification and does not establish causality. The historical native fixture differs in four `third_party/yamux` paths and is not a same-source control.

## Exact hooks

1. The existing test-only `/agent/` ServeMux route gains an outer opt-in wrapper. It creates one bounded attribution record for the existing diagnostic sequence and places only a pointer in request context. The existing WAN httptrace wrapper remains the inner wrapper and the production handler is unchanged.
2. In `proxyRouteTargetRequest.ModifyResponse`, after existing cache and download-shaper body wrappers, a non-101 response body is wrapped with an `io.ReadCloser` that forwards each exact `Read(p)` and `Close()` call. It does not alter `p`, add buffering, call `Read` again, retain body bytes, or implement `WriterTo`.
3. `initializePublicProxyContext` copies the attribution pointer into the existing `proxyResponseRecorder`. Its existing `Write` method times the single unchanged underlying `ResponseWriter.Write(data)` call. No new ResponseWriter layer or method substitution is introduced. The nil path executes the original branch without clock reads. Existing `ReadFrom` dispatch is preserved and counted; the known ReverseProxy path must report zero `ReadFrom` calls.
4. The test-only private admin query snapshots the record only after handler return. Missing, repeated, overflowed, or incomplete records invalidate the diagnostic.

The 101 upgrade path is deliberately excluded because replacing its `io.ReadWriteCloser` body with a read-only wrapper would alter its method set.

## Fixed data shape

The per-request record contains no per-chunk slice. Each of upstream read, downstream write, and inter-read gap contains:

- call count and byte count;
- total and maximum duration;
- first-start and last-end Unix timestamps;
- eight fixed histogram counters with bounds `<1 us`, `<10 us`, `<100 us`, `<1 ms`, `<10 ms`, `<100 ms`, `<1 s`, and `>=1 s`;
- the fixed eight longest stalls: call index, bytes, duration, and start timestamp.

The record also contains body-wrap count, EOF count, read/write error counts, short-write count, and downstream `ReadFrom` count. POST/upload rows are explicitly labeled `response-body-copy`; their 32-byte response measurements do not attribute request upload-body time. Callback updates use no heap allocation or mutex. Handler completion uses one atomic release; the admin snapshot uses one atomic acquire. Resource, lane, pool, and capacity state continues to come from the existing per-row boundary snapshot, avoiding locks in resource owners for every copy chunk.

The test-only store retains at most 32 request records. The diagnostic schedule uses 16. Existing trace sequence, concrete connection key, and exact origin connection ID provide the join; no lane or connection identity is synthesized from copy timing.

## Analyzer contract

For every scheduled row, require exactly one complete copy record and exact string sequence equality. Require store overflow false, body-wrap count one, read/write/short-write errors zero, downstream `ReadFrom` count zero, each histogram sum equal its call count, and all durations finite/nonnegative. For known 256 MiB downloads, require upstream-read bytes and downstream-write bytes both equal the payload. Join the row to the existing httptrace connection key and TLS-recorded origin ID before comparing H1/H2.

Report every phase independently. Do not convert the probe into a performance gate, discard slow rows, infer health from medians, extend the 30 second idle policy, or change buffer size, limits, pools, refund scheduling, ownership, HTTP framing, flush behavior, or timeouts.

## Focused source-test plan

- Verify the disabled/nil path preserves the original `Write` and `ReadFrom` dispatch and results.
- Verify the reader wrapper forwards the same slice, byte count, error, EOF, and Close exactly once without implementing extra interfaces.
- Verify fixed histogram totals, longest-stall bound/order, byte totals, error counters, and completion publication.
- Verify store duplicate, overflow, missing, and incomplete records fail closed; snapshot across the handler-completion publication barrier under the race detector.
- Verify repeated metric, reader, and writer observations allocate no objects per chunk after setup.
- Exercise an in-memory ReverseProxy H1 response and H2 response to prove one body wrapper, zero `ReadFrom`, exact payload bytes, and actual negotiated protocol without changing response headers or content length.
- Only after source and test review: one tiny matched H1/H2 probe to estimate instrumentation overhead before any full diagnostic run.

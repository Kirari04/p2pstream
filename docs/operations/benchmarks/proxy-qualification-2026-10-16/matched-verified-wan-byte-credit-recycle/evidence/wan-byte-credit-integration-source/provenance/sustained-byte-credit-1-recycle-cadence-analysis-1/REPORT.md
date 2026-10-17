# Normal byte-credit recycle cadence after sustained cohort 1

This is an offline, source-bound analysis of the conditioning-clean selected download intervals, sequences 9 and 11. The cohort remains rejected: both arms failed performance floors and had separate upstream-reuse conditioning failures outside these two clean comparisons. This analysis ran no endpoints or traffic and supports no runtime policy change.

## What the retained records can resolve

The exact ordinal/delta/cumulative join permits adjacent-update cadence comparison without subtracting receiver and peer clocks. For every adjacent pair whose next record is a zero-flag `normal_read` update, the analysis separately subtracts receiver eligibility labels, receiver completion-result labels, and peer handler-entry labels within their own process. It never subtracts a receiver timestamp from a peer timestamp.

| sequence | arm | receiver eligibility spacing total | receiver completion spacing total | peer handler spacing total | peer minus receiver completion |
|---:|:---:|---:|---:|---:|---:|
| 9 | H1 | 7893.716 ms | 7893.072 ms | 7892.992 ms | -0.080 ms |
| 9 | H2 | 8501.606 ms | 8501.191 ms | 8500.434 ms | -0.757 ms |
| 11 | H1 | 7840.274 ms | 7839.611 ms | 7838.861 ms | -0.750 ms |
| 11 | H2 | 13264.071 ms | 13263.834 ms | 13263.387 ms | -0.447 ms |

The H2-minus-H1 cadence changes match at both ends: sequence 9 is +607.890 ms at receiver eligibility, +608.119 ms at receiver completion, and +607.442 ms at the peer handler; sequence 11 is +5423.797 ms, +5424.223 ms, and +5424.526 ms respectively. Conditional on these serialized wall labels, the peer cadence changes closely follow receiver publication cadence; the retained data do not show material post-publication cadence expansion. This is not a strict causal bound. It does not measure absolute transit latency, exclude sub-interval transport/read-loop jitter, or turn wall labels into monotonic durations.

These are coarse wall-label comparisons. The observer derived within-record durations with `time.Time.Sub`, but serialization retained only Unix nanoseconds between records. The close cadence agreement and exact ordering make the comparison useful; the values are not validation-quality monotonic durations and cannot establish a causal transport bound.

## Exact normal batching and eligibility buffer

`Stream.Read` consumes aggregate buffered bytes and immediately calls `sendWindowUpdate`. For a zero-flag normal update, `sendWindowUpdateMode` returns until `delta >= maxReceiveWindow/4`. At eligibility it records `receive_window_after = recvWindow + delta`, while `delta = (maxReceiveWindow - buffered) - recvWindow`; therefore `buffered = maxReceiveWindow - receive_window_after` at that exact point.

The retained growth events and forced records have equal cardinality in each clean interval (12/12, 13/13, 13/13, 11/11), and their ordered `current -> proposed` chain is exact. The aggregate configuration initializes both byte and allocation windows to 512 KiB. `GrowingConn` reads the allocation limit; `AggregateReceiveWindowGrowth.Grow` publishes session allocation, stream allocation, then the byte target synchronously. Every successful target in these rows is 4 KiB aligned, so allocation rounding leaves it unchanged, and the `grown` event is recorded only after all three calls return successfully. Under those source- and row-specific checks, the ordered proposed targets reconstruct the byte maximum without interpolating the event's sample-start timestamp. Every zero-flag normal record passes the exact quarter-window predicate.

| sequence | arm | normal records | zero-flag threshold records | empty at eligibility | maximum buffered at eligibility |
|---:|:---:|---:|---:|---:|---:|
| 9 | H1 | 68 | 67 | 53 | 297.8 KiB |
| 9 | H2 | 69 | 68 | 50 | 189.9 KiB |
| 11 | H1 | 63 | 62 | 38 | 104.0 KiB |
| 11 | H2 | 106 | 105 | 65 | 160.0 KiB |

The one excluded normal record per interval carries the initial/open flag and is not treated as consumed-byte recycle. The empty/max columns cover only zero-flag recycled-normal records. Most such eligibility snapshots are empty; the table reports the measured nonempty maxima without interpreting them as a ratio to the per-record receive limit. This says only that the receiver did not retain a large backlog at those exact eligibility snapshots. It cannot rule out transient buffering between snapshots or distinguish lack of inbound DATA from prompt application consumption.

## Public copy path and H2 backpressure

The public reverse proxy uses one 32 KiB copy buffer. Its unchanged copy loop reads the attributed upstream body and then writes the returned bytes downstream. The attribution wrapper measures each upstream Read and downstream Write with monotonic `time.Time.Sub`; `inter_read_gap` spans the previous Read return to the next Read call and therefore overlaps the intervening downstream Write rather than forming an additive duration.

| sequence | metric | H1 total | H2 total | H2-H1 |
|---:|:---|---:|---:|---:|
| 9 | upstream Read | 8313.380 ms | 8759.331 ms | +445.951 ms |
| 9 | downstream Write | 323.427 ms | 670.543 ms | +347.116 ms |
| 9 | inter-read gap | 329.010 ms | 677.240 ms | +348.229 ms |
| 9 | copy span | 8642.390 ms | 9436.571 ms | +794.181 ms |
| 11 | upstream Read | 8278.627 ms | 13215.415 ms | +4936.788 ms |
| 11 | downstream Write | 360.528 ms | 718.696 ms | +358.168 ms |
| 11 | inter-read gap | 366.390 ms | 725.359 ms | +358.970 ms |
| 11 | copy span | 8645.016 ms | 13940.774 ms | +5295.758 ms |

The installed, build-bound Go 1.26.6 HTTP/2 server waits for the scheduled DATA frame write result in `writeDataFromHandler`, so downstream Write duration includes local HTTP/2 scheduling, flow-control, and socket-write backpressure. Its directly observed duration is real and is not the dominant sequence-11 accounting term; this does not exclude feedback amplification in which delayed downstream consumption later increases upstream credit stalls. Sequence 11 adds about 4.94 seconds inside upstream Read and 0.36 seconds between Reads/downstream writes. Sequence 9 splits its 0.79-second copy-span increase between both aggregates. Upstream Read itself includes waiting for Yamux DATA and the synchronous credit publication check, so it is not a pure application-consumption metric.

## Decision and minimal missing discriminator

No defect or runtime behavior change is supported. Conditional on the coarse wall-label comparison, the retained evidence shows no material *cadence expansion* after receiver publication for the clean rows and places the observed cadence increase before positive-update eligibility. It does not establish a strict causal or transit bound. It does not divide that pre-eligibility time into public-consumer/backpressure time and waiting for inbound Yamux DATA. Growth-policy, rounding, window constants, and the 30-second reuse timeout remain outside the supported change scope.

If another diagnostic is required before considering a runtime change, the smallest useful addition is qualification-only fixed per-stream accumulation, joined to the existing positive receiver ordinal; it is not another event ring:

- monotonic time spent inside `Stream.Read` before bytes become consumable;
- monotonic time outside `Stream.Read`, from the prior return to the next entry;
- successful-read byte/count totals and maximum outside gap for the batch;
- snapshot and reset only when an accepted positive WINDOW_UPDATE record is owned, retaining forced/open flags separately and invalidating overflow/ambiguity as today.

This produces a falsifiable split. Excess outside-Read time supports public-consumer/H2 write pacing. Excess inside-Read time with low eligibility buffers supports inbound-data/credit-feedback pacing. A change confined to larger quarter-window batches would instead appear as larger bytes/read counts per accepted normal update without corresponding per-byte timing inflation. Receiver-to-peer transit remains unclassified; measuring absolute one-way transit would require synchronization or an acknowledgment and is not justified by the current evidence.

## Frozen inputs and source scope

The machine-readable draft binds the accepted run files, prior exact trace analysis, audited build-attempt-5 source and receipt, and the pinned build toolchain source `/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/src/net/http/h2_bundle.go` (SHA-256 `6541e350500d6b3525d99dce470034c7756bd5c931fd507f9f0352280a16c31e`). The build receipt binds the same Go 1.26.6 toolchain binary as SHA-256 `29e6e0b8be61beb1489ceae62b304343566de8a1dc700af74bde7aeb9c80ad45`. It preserves the prior trace analysis unchanged.

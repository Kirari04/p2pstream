# Sustained byte-credit source feedback review

Scope: read-only review of the audited build-5 source and already-frozen sustained-byte-credit-1 evidence. This review does not change runtime source, policy, constants, or experiment inputs.

## Finding

No correctness defect is established in the normal byte-credit update, priority-control queue, or peer wake path.

There is a concrete feedback path: the public proxy copies one chunk at a time as `Read` followed by `Write`; Yamux returns aggregate storage while consuming the read, then synchronously considers a byte-window update before that `Read` returns. Normal positive updates are withheld until accumulated returned byte credit reaches one quarter of the current maximum receive window. Consequently, time spent after one upstream read returns and before the next begins delays consumption toward the next threshold. This behavior can amplify consumer pacing, but the retained evidence does not establish that the quarter-window policy is itself defective or justify changing its constant.

The control-send path is not a plausible source of the observed hundreds-of-milliseconds cadence in this cohort. Eligible ordinary updates use the aggregate priority queue; the send loop checks that queue first and permits at most one already-selected DATA frame ahead. Frozen trace evidence reports receiver eligibility-to-send-completion at no more than 264 microseconds overall (209 microseconds on clean sequences), peer handler-to-notify at no more than 17 microseconds, and wake-to-retry at no more than 44 microseconds, with no drop, ambiguity, overflow, or retry-zero defect.

The retained same-process copy data also limits the public-writer explanation. On clean selected downloads, H1/H2 downstream-write totals were 0.323/0.671 seconds for sequence 9 and 0.361/0.719 seconds for sequence 11; corresponding maximum individual writes were 0.215/0.568 milliseconds and 0.265/0.575 milliseconds. Inter-read gaps were nearly identical to those write totals and every maximum was below 0.577 milliseconds. These aggregates show that public response writes contribute feedback, especially under H2, but do not support a single long public write as the cause of a long byte-zero wait. Durations from different observers are not added or subtracted as though their intervals were disjoint.

## Existing observability and gap

The server-side receiver record already measures `eligibility_observed` through enqueue and local send completion. The proxy copy attribution in the same process measures each upstream `Read`, downstream `Write`, and the gap from the previous read's end to the next read's start, but retains only aggregates, buckets, and the eight longest calls. Because the standard proxy loop is serial, that gap includes the intervening public `Write` plus small loop/wrapper scheduling costs.

The sender-side peer record independently measures handler entry, credit application, notification, and writer wake/retry. Receiver and sender clocks are in different processes, so their Unix timestamps cannot be subtracted to derive wire transit. Existing retained data therefore distinguishes local receiver queue/send delay and bounds transfer-wide public-consumer feedback, but it cannot assign each sender byte-zero wait between (a) receiver read/consumption before threshold eligibility and (b) completion-to-peer transport.

If another diagnostic is ever approved, the smallest discriminating extension is a qualification-only fixed-scalar accumulator per selected stream, reset at each positive publication: bytes and read calls since the preceding publication, total/max time inside `Stream.Read`, and total/max gap from the preceding `Stream.Read` return to the next entry. Emit those scalars with the existing receiver publication record and current `max`, threshold, `delta`, and buffer state. This avoids a per-read ring and attacker-controlled cardinality. It would separate receiver-active waiting from consumer/scheduler gaps before eligibility while leaving post-completion wire transit explicitly unresolved.

## Source anchors

- Yamux read, aggregate return, and synchronous update: `third_party/yamux/stream.go:121-192`.
- Quarter-window normal-update gate and publication: `third_party/yamux/stream_window_update_send_qualification.go:10-84`.
- Aggregate priority routing: `third_party/yamux/session.go:465-473,535-546`.
- Priority dequeue and physical write: `third_party/yamux/session_send_loop_qualification.go:20-40,80-129`.
- Proxy setup and attribution placement: `internal/server/public_routing.go:595-653,733-736`.
- Fixed 32 KiB copy buffer: `internal/server/reverse_proxy_buffer_pool.go:8-10,26-42`.
- Attribution gap/read bookkeeping: `internal/server/proxy_copy_attribution.go:100-143,194-209`.
- Downstream write timing: `internal/server/traffic_trace_request.go:35-50`.
- Go 1.26.6 serial reverse-proxy copy loop: `src/net/http/httputil/reverseproxy.go:696-754` in the pinned toolchain module.

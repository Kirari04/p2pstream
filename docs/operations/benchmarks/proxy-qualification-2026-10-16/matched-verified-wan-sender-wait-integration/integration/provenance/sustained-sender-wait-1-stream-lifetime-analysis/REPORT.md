# Sustained sender-wait stream lifetime and growth evidence

This is a read-only analysis of the closed `sustained-sender-wait-1` cohort. It
did not run a test, parser, endpoint, or network workload and does not propose a
window, queue, refund, timeout, or policy change.

## Result

The H2 selected rows separate into three receiver-lifetime classes:

| seq | lane / stream | actual origin relation | server receive lifetime | byte-zero wait | stream-allocation wait | physical write |
| ---: | --- | --- | --- | ---: | ---: | ---: |
| 9 | 1 / 6 | first post-close visit, origin `10.212.0.2:51902` | fresh stream, first bulk download | 24 / 1.663008381 s | 3 / 0.079041748 s | 7.516358513 s |
| 11 | 3 / 6 | first post-close visit, origin `10.212.0.2:34772` | fresh stream, first bulk download | 17 / 0.707907746 s | 6 / 0.159358921 s | 6.622475667 s |
| 14 | 2 / 6 | exact reuse of seq10 origin `10.212.0.2:36696`, idle 24.622228686 s | same stream after a bulk upload, but first bulk download/server receive direction | 23 / 1.587929620 s | 4 / 0.159980045 s | 7.635943754 s |
| 15 | 3 / 6 | exact reuse of seq11 origin `10.212.0.2:34772`, idle 26.670673067 s | same stream after a bulk download; server receive estimator/windows retained | 0 / 0 s | 0 / 0 s | 7.344823842 s |

All four have zero session-allocation waits. The observed distinction is
per-stream byte and allocation credit, rather than exhaustion of the retained
session pool. Sequence 15 is the only selected H2 row that reuses a stream
after a bulk transfer in the same receive direction, and it is the only one
with no credit wait. This is consistent with retained same-direction stream
growth. It does not show why the three direction-cold rows have different wait
totals.

Sequence 9 and sequence 11 are both fresh stream-6 first downloads, yet their
byte-zero totals differ by 0.955100635 s. Sequence 14 is an exact connection
and stream reuse, but only its *agent receive* direction was grown by the seq10
upload; its server receive direction is cold and it records 1.587929620 s of
byte-zero wait. Stream lifetime/direction therefore explains the zero versus
nonzero distinction at seq15, but does not explain the 1.6 s versus 0.7 s
spread among direction-cold rows.

The H1 records reinforce the boundary without serving as a controlled growth
trace. Sequence 15 exactly reuses seq11 and has zero credit wait. H1 seq14 did
not reuse seq10: it crossed the 30 s idle deadline (34.215061244 s), opened
stream 8 with a new origin, and therefore belongs to the fresh-replacement
class. It must not be described as a warm seq10 stream.

## Directionality check from the captured growth ring

The only growth ring captured by this cohort is returned by the agent-side
`growth_snapshot=drain` action. For a download, that ring observes the small
request entering the agent. The 256 MiB response flows in the other direction,
where the server is the receiver.

That opposite-direction ring makes the distinction concrete:

* H2 seq14's post event is lane 2 / stream 6 at Unix ns
  `1792196183710526255`. It reports current and proposed receive limits of
  24,969,216 bytes, retained from the seq10 upload. The same seq14 download
  nevertheless records 1.587929620 s of sender byte-zero wait. A grown agent
  receiver does not pre-grow the server receiver on the same stream.
  The preserved seq10 agent-side publications grew through the exact proposed
  targets 851,968; 1,572,864; 2,097,152; 2,883,584; 4,849,664; 7,274,496;
  10,485,760; 14,614,528; 19,791,872; and 24,969,216 bytes, from Unix ns
  `1792196152304415906` through `1792196153518077488`. Each individual
  publication time/current/proposed/rate is in the machine JSON.
* H2 seq15's post event is lane 3 / stream 6 at Unix ns
  `1792196194179389370`. It reports the agent receive side still at 524,288
  bytes because seq11 was a download. The seq15 sender records zero byte and
  allocation waits because the opposite server receive direction is the one
  previously grown.
* H2 seq9 and seq11 post events also show 524,288 bytes on the agent receive
  side. Those values cannot explain their server-facing sender waits.

The event timestamps, current/proposed values, exact row mappings, and both H1
and H2 records are retained in `STREAM-LIFETIME-CREDIT-ANALYSIS.json`.

## Current aggregate policy versus the frozen legacy source

The current and frozen legacy `receive_window_growth.go` files are byte
identical (SHA-256
`5954cf0472d92031945c2f8f1417488437ac640103dd927e9fcc390b5ef8f49d`).
Both therefore use the same demand estimator: an epoch is at least one RTT,
two consecutive samples must support growth, one decision grows at most 2x,
the value is rounded to 64 KiB, and a pause beyond `max(1s, 8*RTT)` resets the
sample (`receive_window_growth.go:29-82`).

The publication path differs. Frozen legacy reserves the additional legacy
receive memory and publishes the byte-window target. Current aggregate uses the
same estimator against the stream allocation limit, then publishes an accepted
target in dependency order: grow the session pool if required, grow stream
allocation, then grow the byte window (`growing_conn.go:83-145,164-212`). A
retained pool can skip the session-growth step, but it does not seed a new
stream's estimator, allocation, or byte window. This matches the sender record:
the selected H2 rows have no session-allocation-zero waits, while each
direction-cold row still has byte and stream-allocation waits.

No discrepancy from that intended ordering is established. The sender record
shows which credit was zero and how long its caller waited; wake type is only
the selected notification, not proof that the notification uniquely resolved
the cause.

## Missing evidence

The raw run contains no per-stream server credit snapshot fields and no server
receiver growth-ring records. Its `pre_server_capacity` and
`post_server_capacity` objects contain adaptive resource/admission state only.
The reviewed qualification source had suitable test-only server endpoints:
`credit_snapshot=1` exposes byte send availability, byte receive limit, and
allocation send/receive limits; `growth_snapshot=drain` exposes accepted
targets and sample times. The sender-duration runner did not query those server
routes. A raw search for their credit fields in both closed arms returned zero
files.

Consequently this cohort cannot reconstruct the requested server receiver
window targets, advertised byte-credit values, or publication times. It cannot
decide whether seq9/14 accumulated more byte-zero time because their receiver
estimator proposed later/smaller targets, because publication arrived later,
or because different physical pacing changed the demand samples. Physical
write time also differs (7.516 s, 6.622 s, 7.636 s, and 7.345 s for H2
seq9/11/14/15), so credit-wait totals are not the whole row-duration delta.

The smallest evidence gap is a time-aligned server-side per-stream growth and
credit record for the already fixed selected rows, joined to lane, stream ID,
and actual origin reuse. This is an observation gap, not a basis for increasing
windows or reopening the rejected growth/refund/DATA experiments.


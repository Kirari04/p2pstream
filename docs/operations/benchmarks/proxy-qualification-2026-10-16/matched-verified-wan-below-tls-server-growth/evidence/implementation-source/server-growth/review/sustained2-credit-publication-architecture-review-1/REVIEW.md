# Sustained-2 credit publication architecture review

This is a read-only source/evidence review. It changes no runtime source,
policy, constants, tests, binary, workload, timeout, or floor and authorizes no
new experiment. The complete sustained-2 cohort remains rejected on its fixed
throughput floors even though its integrity, conditioning, lifecycle, resource,
raw-write, and growth-evidence gates all close.

## What the paired measurement localizes

The H2-minus-H1 credit-wait increase on direction-cold selected downloads is
643 ms at sequence 9, 878 ms at sequence 11, and 801 ms at sequence 14. The
cause rows show that this difference is almost entirely in the observer's
`byte_zero` category:

| sequence | H1 byte-zero | H2 byte-zero | delta | H1 stream-allocation-zero | H2 stream-allocation-zero |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 9 | 0.950 s | 1.670 s | +0.720 s | 0.234 s | 0.157 s |
| 11 | 0.711 s | 1.669 s | +0.958 s | 0.159 s | 0.079 s |
| 14 | 1.027 s | 1.827 s | +0.800 s | 0.079 s | 0.080 s |
| 15 | 0 | 0 | 0 | 0 | 0 |

No selected interval records session-allocation-zero time. Sequence 15 is warm
in the download direction in both arms: neither arm grows credit or waits for
credit, and throughput differs by only 8,420 B/s. The cold H2 streams finish
with somewhat lower receive limits than their H1 peers (about 1.4--2.2 MiB),
with only `grown` and `no_proposal` outcomes and zero ring drops. The measurement
therefore supports a direction-history/cadence association. It does not show a
reservation denial, publication error, or failed wake.

The cause labels have one important boundary. Tagged `Stream.write` checks the
byte window first and records `byte_zero` before it calls aggregate reservation
(`third_party/yamux/stream_write_qualification.go:34-51`). A byte-zero interval
therefore does not independently prove that allocation credit was positive at
the same instant. Allocation causes are measured only when byte credit is
already usable and `reserveAggregateSend` returns no payload. The table is an
exact decision-path attribution, not a simultaneous three-ledger snapshot.

## Current publication and wake semantics

Legacy and aggregate streams share the same demand estimator. It samples bytes
consumed over wall time, requires two supporting samples, smooths the observed
rate, caps one increase at 2x, rounds to receive chunks, and resets after a
long pause (`internal/tunnel/receive_window_growth.go:25-82`). Because wall time
includes time outside `Read`, a slower downstream consumer can reduce the next
proposal; smaller credit can in turn increase later sender waits. That is a
possible feedback loop, not a direction of causation established by this run.

The aggregate wrapper uses the stream allocation as the estimator's `current`
window and then publishes a successful proposal in dependency order
(`internal/tunnel/growing_conn.go:83-121,164-212`):

1. reserve memory and grow the session pool if the target requires it;
2. grow this stream's allocation allowance;
3. grow the ordinary per-stream byte window.

The ordering protects real invariants. A peer must not receive stream
allocation beyond one quarter of the session limit, and byte credit must not be
advertised before backing session/allocation storage exists. Reservations are
retained because publication is irrevocable or ambiguous after enqueue.

Each of the three calls waits for its control frame's local send completion,
but there is no peer acknowledgement or explicit WAN round trip in these
methods (`third_party/yamux/aggregate_window.go:762-858` and
`third_party/yamux/stream.go:340-418`). On receipt, session and allocation grow
frames update their respective sender ledgers and broadcast the aggregate
wakeup; the final per-stream window update increments byte credit and wakes the
stream sender (`third_party/yamux/aggregate_window.go:253-334` and
`third_party/yamux/stream.go:713-738`). Intermediate broadcasts can make a
writer recheck before all dependencies are ready, but they preserve correctness
and are bounded by actual control publications.

Normal aggregate recycling is separate. Complete received chunks queue paired
session/allocation refunds to one asynchronous publisher, while the ordinary
per-stream byte-window update still occurs synchronously after application
`Read`. That difference from legacy is intentional and was already reviewed;
the new data does not identify refund loss or session-pool starvation.

## Discrepancy assessment

There is no demonstrated source invariant violation or concrete production bug.
The additional H2 time aligns most strongly with byte-window exhaustion on
streams that have not already grown in the download direction. It does not
align with the extra aggregate session gate, and the measured stream-allocation
wait is smaller in H2 for sequences 9 and 11 and essentially equal for 14.
Sequential dependency publication is visible in source, but the methods do not
wait for network acknowledgements, and this cohort does not measure enough of
the control path to assign the 0.6--0.9 second H2 delta to those three local
send commits.

Physical raw occupancy also cannot close the cause: it is only 69--80 ms higher
for H2 on sequences 9, 14, and the fast sequence 15, although sequence 11 has a
1.031-second raw delta. Public H2 downstream writes are 263--355 ms higher even
on fast sequence 15. The strongest defensible interpretation is an interaction
between H2 consumption cadence and the existing demand-driven byte-credit ramp
on direction-cold streams. Cause versus consequence remains unresolved.

This result does not justify changing the initial window, headroom, doubling,
sample count, refund batching, allocation ratio, or other growth constants. It
also does not reopen the rejected DATA-emission, refund/epoch, TCP-low-water,
allocation-rounding, or growth-policy experiments described by the prior credit
lifecycle review and growth-trace record.

## Smallest remaining discriminator

The existing growth ring timestamps the controller sample start and records the
outcome only after publication. It does not expose the completion time of the
session, allocation, and byte publications, nor the peer's corresponding
ledger update/wakeup. Consequently it cannot distinguish a slow demand proposal
from delay between an accepted proposal and usable sender credit.

If further evidence is authorized, the smallest discriminator is
qualification-only timing around these existing boundaries for the selected
stream: controller decision, completion of each required local control-frame
send, peer handler receipt, and the first sender wake that observes all usable
credit. A bounded deterministic control should gate each existing control
frame in turn and prove the exact dependency/wakeup order and that no step waits
for a protocol acknowledgement. It must retain the current reservation,
publication, queue, timeout, and resource policy. This would measure the
remaining boundary; it is not a proposal to batch frames or alter growth.

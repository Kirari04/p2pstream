# Sustained byte-credit trace analysis

This is a read-only analysis of the eight selected byte-credit intervals in `sustained-byte-credit-1`. The cohort remains rejected: both arms have fully evidenced upstream-reuse conditioning failures and download-floor failures. Both arms nevertheless passed all integrity, ownership, trace, resource, and natural-closure gates.

## Exact trace integrity

- 645 receiver records join exactly to 645 peer records by selected physical tuple, lane, generation, stream, relative ordinal, delta, and cumulative credit.
- 138 byte-zero wait records are complete. No trace drops, ambiguity, overflow, terminal wait, or retry with zero credit after an applied advance occurred.
- Sender byte-zero wait cardinality equals trace wait cardinality in every interval. The trace entry-to-retry totals exceed the mature sender byte-zero buckets by only 0.001–0.060 ms per interval, consistent with the extra trace snapshots around the same wait.

## Conditioning-clean comparisons

Only sequences 9 and 11 are conditioning-clean in both arms. Their byte-zero wait differences are:

| Sequence | H1 byte-zero | H2 byte-zero | H2−H1 | Normal-read range H2−H1 | Forced-growth range H2−H1 |
|---:|---:|---:|---:|---:|---:|
| 9 | 950.003 ms | 1664.784 ms | +714.781 ms | +630.953 ms | +83.820 ms |
| 11 | 867.343 ms | 1741.099 ms | +873.756 ms | +947.188 ms | -73.425 ms |

The clean H2 excess is concentrated in waits whose exact applied range contains normal `normal_read` updates. Forced-growth ranges add little in sequence 9 and are lower in H2 sequence 11. This falsifies a growth-only explanation for the clean H2 excess. It does not identify the cause of normal recycling cadence.

## Local measured sections

| Arm/sequence | Receiver eligibility→completion total / max | Peer handler→notify total / max | Wake→retry max | Byte-zero wait total |
|---|---:|---:|---:|---:|
| H1 9 | 4.274 / 264.0 µs | 0.300 / 16.1 µs | 2.9 µs | 950.003 ms |
| H1 11 | 4.140 / 161.2 µs | 0.312 / 14.9 µs | 2.8 µs | 867.343 ms |
| H1 14 | 5.393 / 315.1 µs | 0.384 / 17.8 µs | 3.1 µs | 948.453 ms |
| H1 15 * | 3.290 / 161.9 µs | 0.256 / 18.4 µs | 2.5 µs | 790.169 ms |
| H2 9 | 4.609 / 208.5 µs | 0.371 / 17.3 µs | 44.0 µs | 1664.784 ms |
| H2 11 | 5.975 / 203.7 µs | 0.467 / 12.8 µs | 2.6 µs | 1741.099 ms |
| H2 14 * | 3.244 / 147.2 µs | 0.283 / 26.8 µs | 2.3 µs | 792.412 ms |
| H2 15 | 2.001 / 103.3 µs | 0.138 / 9.5 µs | 0.0 µs | 0.000 ms |

`*` The selected row is within that arm’s retained upstream-reuse conditioning failure; it is not used for a cross-arm performance inference.

The receiver and peer local sections are microsecond-scale, while byte-zero waits are hundreds of milliseconds to seconds. These sections include observer work, protocol work, and local scheduling, so they are conservative bounds rather than pure observer-overhead measurements. They cannot explain the large clean H2 wait excess.

H2 sequence 15 applied 52 exact positive credit updates, all observed as coalesced notifications, without entering byte-zero wait. Positive recycling therefore continued while the writer retained usable credit. This row is not used to infer performance because the paired H1 sequence 15 has a reuse-conditioning failure.

## Remaining unmeasured interval

The retained data does not measure time before receiver eligibility, including application consumption and quarter-window batching. It also cannot subtract clocks between receiver completion-result observation and peer handler entry, which contains send scheduling, TLS/socket transit, network transit, receive, and dispatch. Same-endpoint Unix timestamps establish ordering only; all reported durations above come from monotonic duration fields.

The result supports a normal byte-credit cadence discrepancy at clean H2 sequences 9 and 11. It does not support changing the 30-second idle threshold, growth constants, window policy, or any runtime behavior, and it does not establish HTTP/2, TLS, or the network as the cause.

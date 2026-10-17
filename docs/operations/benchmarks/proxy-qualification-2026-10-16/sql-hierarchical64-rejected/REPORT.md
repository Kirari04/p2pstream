# Hierarchical 64 -> 16 -> single SQL candidate

## Decision

**Reject and stop.** Candidate C passes correctness, race, SQLite-capacity, 512-event speed, and 512-event allocation gates, but fails the predeclared smaller-batch stability gate: pair 2 at 127 events measured C/B = **1.150520616**, above the maximum permitted **1.10**. No HTTP/E2E workload was run.

This is a candidate-overlay-only diagnostic against immutable source checkpoint `8d97b45d7f6c7dc5cf510cc3f4b29e1a54b0debf`. It is not a production change or release result.

## Sources and behavior

- B source: `source-baseline`, current fixed-16 inserter, SHA-256 `bdd9bd1b658e7d0f20e5a0965d1b710cb8f9e0d92b46fa7e2e2a2b06ed47921f`.
- C source: `source-candidate`, lazy prepared 64-row statement followed by lazy prepared 16-row statement and single-row remainder, SHA-256 `0d6065fc2b3a1823aa8b36c28d746dd7edfde335a372a4686e78a414aa990565`.
- Identical B/C test and benchmark overlay SHA-256: `4ea3a60cd7e6886b7ca474514b80be1db692df43f27f8f93c0757031df3bcf69`.
- `diff -qr` reports only `internal/db/proxy_request_event_inserter.go` differs between B and C.
- C retains the caller transaction and context, 23 bound columns, 64 -> 16 -> single decomposition, lazy fixed statements, and joined close errors. It does not bypass the driver or interpolate event values.

## Correctness

Both B and C passed all five focused tests normally and with `-race`:

- retained 35-row atomic rows/rollups/touch and cancellation cases;
- 131-row equality for all 23 raw fields plus primary, tuple, status, and retry rollups and cache touch;
- cancellation at row 65 after the first 64 rows executed, with raw rows, all rollups, and cache touch fully rolled back;
- actual pinned SQLite `MAX_VARIABLE_NUMBER=32766`, exceeding the 1,472 variables required by 64 rows x 23 columns.

The recorder's existing 512 flush cap and five-second production deadline path remain in the shared benchmark helper.

## Timed results

Each cell used Go 1.26.6, `GOMAXPROCS=4`, `taskset -c 0-3`, a one-second benchmark duration, `-benchmem`, identical seeded data/indexes/cleanup/DB settings, and owned ext4 `TMPDIR`, `GOTMPDIR`, and `GOCACHE`. Pair order was B -> C, C -> B, B -> C.

| Events | Pair | B ns/op | C ns/op | C/B | B B/op | C B/op | B allocs/op | C allocs/op |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 63 | 1 | 1,425,654 | 1,385,547 | 0.971868 | 126,849 | 126,868 | 1,427 | 1,427 |
| 63 | 2 | 1,414,626 | 1,411,546 | 0.997823 | 126,859 | 126,839 | 1,427 | 1,427 |
| 63 | 3 | 1,404,911 | 1,400,827 | 0.997093 | 126,861 | 126,848 | 1,427 | 1,427 |
| 127 | 1 | 2,230,590 | 2,292,900 | 1.027934 | 211,568 | 211,043 | 2,375 | 2,355 |
| 127 | 2 | 2,212,667 | 2,545,719 | **1.150521** | 211,548 | 211,053 | 2,375 | 2,355 |
| 127 | 3 | 2,262,091 | 2,327,911 | 1.029097 | 211,558 | 211,052 | 2,375 | 2,355 |
| 512 | 1 | 4,860,373 | 4,164,897 | 0.856909 | 716,474 | 709,634 | 7,858 | 7,642 |
| 512 | 2 | 4,878,048 | 4,209,616 | 0.862971 | 716,493 | 709,566 | 7,858 | 7,642 |
| 512 | 3 | 4,893,550 | 4,309,623 | 0.880674 | 716,472 | 709,585 | 7,858 | 7,642 |

| Events | Median C/B ns/op | Maximum C/B ns/op | Gate | Result |
|---:|---:|---:|---|---|
| 63 | 0.997093 | 0.997823 | median <=1.05; every pair <=1.10 | pass |
| 127 | 1.029097 | **1.150521** | median <=1.05; every pair <=1.10 | **fail** |
| 512 | 0.862971 | 0.880674 | every pair <=0.95 | pass |

At 512 events, median B/op fell from 716,474 to 709,585: 1,399.36 to 1,385.91 bytes/event, a 0.96% decrease. All three cells reported 7,858 B allocations/op and 7,642 C allocations/op: 15.3477 to 14.9258 allocations/event, a 2.75% decrease. There is no >5% per-event allocation increase.

## Limits and next recommendation

These are one-second microbenchmark samples of the full recorder flush path against local SQLite. The balanced order reduces simple order bias but does not turn three pairs into a production throughput result. The isolated 127-event regression may be host variance, but the finite protocol deliberately makes the per-pair ceiling decisive and does not authorize retries after a failure.

Do not advance this hierarchical-64 candidate to the proposed HTTP B/C stage or production. Preserve the measured 512-event gain as diagnostic evidence only, and prioritize the already planned public WAN H1/H2 fixture work rather than spending another runtime cycle on this rejected SQL shape.

Exact commands are in `artifacts/COMMANDS.sh`; raw test and benchmark output is in `results/`; `results/SUMMARY.tsv` is the direct numerical extraction. Source and artifact lineage is in `artifacts/SOURCE_PLAN.md` and the two patch files in `artifacts/`.

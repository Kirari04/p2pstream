# Independent H1 smoke3 handler-state partition audit

This audit independently streamed the three frozen `go tool trace -d=parsed`
files named by the closed-run reduction index. It did not import, copy, or
invoke the qualification analyzer. The source audit is
`audit_handler_partitions.py` (SHA-256
`8e59bed3301abe3097bbee8aaa208e1dd1539e94af74a8e4e3c9537d2d973757`),
and its machine-readable result is `audit.json` (SHA-256
`d88a2959c3a8e6366c422c5039f0f67eecbfc278241e4d07384a178e3f0ce406`).

The frozen input index SHA-256 is
`22956c32da48ad42e74766ec81227ab3782e1eec99238e421d18a124618a8477`.
For each sequence, the audit recomputed and matched the index's raw trace,
parsed text, and parser-status hashes; each parser status is exactly `0`.

## Method and invariants

The audit requires exactly one `wan-read-wait-request` task begin/end and one
`public-proxy-handler` region begin/end. The four marker header GIDs and task
IDs must agree, their event order must be TaskBegin, RegionBegin, RegionEnd,
TaskEnd, and the region duration must be positive. Global main-event timestamps
must be nondecreasing.

For state accounting, the audit uses the `StateTransition` **Resource GoID**,
not the event header GID. It filters transitions to the marker's handler GID,
requires ordered transitions and exact old-state/new-state continuity, forms
state intervals from successive transitions, and clips those intervals to the
handler region. Equal-time transitions remain ordered and may form zero-length
intervals. The clipped Running, Runnable, Syscall, and Waiting durations must
partition every region nanosecond exactly.

## Results

| Sequence | Task / handler GID | Region ns | Running ns | Runnable ns | Syscall ns | Waiting ns | Exact sum |
|---:|---:|---:|---:|---:|---:|---:|:---:|
| 9 | 1 / 887 | 889,906,816 | 4,335,552 | 1,554,239 | 4,882,304 | 879,134,721 | yes |
| 11 | 2 / 1050 | 897,186,496 | 5,050,690 | 1,807,487 | 5,289,025 | 885,039,294 | yes |
| 13 | 3 / 1215 | 726,468,352 | 3,406,976 | 1,591,296 | 3,974,080 | 717,496,000 | yes |

All marker/GID checks, event ordering, old-state continuity, interval coverage,
and exact partition checks pass. Waiting accounts for 98.790%, 98.646%, and
98.765% of the respective handler regions. These are scheduler-state totals;
this audit does not classify individual wait sites or infer a cause from the
coarse Waiting state.

## Zero-PC frame disposition

Each parsed file contains exactly one `Stack` frame with PC `0x0`, empty
function/file, and source line `0`. None belongs to the handler transition
path:

| Sequence | Parsed line | Event time | Header GID | Resource GoID | Transition | Relative to handler end |
|---:|---:|---:|---:|---:|---|---:|
| 9 | 293606 | 18974907170048 | 861 | 860 | Waiting to Runnable | +92,771,584 ns |
| 11 | 277730 | 18977499566912 | 1059 | 1058 | Waiting to Runnable | +84,649,536 ns |
| 13 | 267216 | 18980103792640 | 1224 | 1223 | Waiting to Runnable | +253,547,200 ns |

In every case the resource GoID differs from the handler GID and the owning
event occurs after RegionEnd. The zero-PC frame is therefore unrelated to the
relevant handler transitions; rejecting the handler partition because of this
frame would be a false rejection.

## Scope

The audit is read-only with respect to the closed run. It did not run an
endpoint, build Go code, or generate traffic. It binds but does not duplicate
the raw traces or the 73 MB of parsed text. It establishes scheduler-state
partition integrity only; it does not identify buffer-credit, network, TCP,
or application causality.


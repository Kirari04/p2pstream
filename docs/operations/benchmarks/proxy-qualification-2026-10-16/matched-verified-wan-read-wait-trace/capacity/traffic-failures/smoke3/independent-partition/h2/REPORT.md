# Independent H2 smoke3 handler-state partition audit

This audit streamed only the three frozen H2 parsed traces identified by the
closed-run reduction index. It reused the previously frozen independent audit
source byte-for-byte (SHA-256
`8e59bed3301abe3097bbee8aaa208e1dd1539e94af74a8e4e3c9537d2d973757`)
and did not import or invoke the qualification analyzer.

The index SHA-256 is
`92d33ce0540671ca168d3f8a978fead362a85509ea65c2bae4e762900ccbbe20`.
Every raw, parsed, and parser-status hash was recomputed and matched its index
binding; all parser statuses are `0`. The machine report is `audit.json`,
SHA-256 `b608014581f4a39c7f92135e346568e894269725d7d207f677297f0b49b5380e`.

| Sequence | Task / handler GID | Region ns | Running ns | Runnable ns | Syscall ns | Waiting ns | Exact sum |
|---:|---:|---:|---:|---:|---:|---:|:---:|
| 9 | 1 / 3495 | 892,117,440 | 1,916,738 | 2,586,059 | 16,832 | 887,597,811 | yes |
| 11 | 2 / 4286 | 891,638,400 | 2,378,627 | 2,182,786 | 15,232 | 887,061,755 | yes |
| 13 | 3 / 5141 | 726,153,344 | 1,636,476 | 1,936,644 | 19,968 | 722,560,256 | yes |

For all three sequences, exact task/region marker counts and types, marker task
IDs, marker header GIDs, marker ordering, global event timestamp ordering,
Resource-GoID transition ordering, and old-state/new-state continuity pass.
Running, Runnable, Syscall, and Waiting cover every handler-region nanosecond.

Sequences 9 and 11 each contain one zero-PC `Stack` frame. Neither is on the
handler path:

| Sequence | Parsed line | Header / Resource GID | Transition | Position |
|---:|---:|---:|---|---:|
| 9 | 510277 | 3437 / 3436 | Waiting to Runnable | 86,443,968 ns after RegionEnd |
| 11 | 514222 | 4281 / 4280 | Waiting to Runnable | 87,119,360 ns after RegionEnd |

Those Resource GoIDs differ from handler GIDs 3495 and 4286. Sequence 13 has
no zero-PC frame. This independently confirms that the original sequence 9/11
zero-PC rejection concerns unrelated post-handler events, while preserving the
original analyzer statuses. It makes no performance or causal claim; the
coarse Waiting totals do not identify a wait site.


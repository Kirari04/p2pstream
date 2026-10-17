# Credit-window report provenance supplement

This supplement preserves
`CREDIT-WINDOW-LIFECYCLE-INVESTIGATION.md` at SHA-256
`c2237c9d6a3045ddd120a3695e23806a2b8e3ff55962bb05fc84129b6ebb770d`.
It does not replace or silently revise that report's table.

The table is transcribed from the original on-run
`read-wait-reductions/sequence-{9,11,14,15}.reduction.json` files. Those four
reducers exited nonzero because each rejected positive `Waiting -> Waiting`
intervals with no `TransitionStack`; the original artifacts remain the
authoritative failed gate. The separately implemented independent canonical
partition audit matched their marker identities, complete state partitions,
and the table's original category totals exactly.

Later generation-status-aware offline reductions accepted the same canonical
events by resolving the valid no-stack transition form. That correction changes
some category attribution by microseconds without changing marker timestamps,
region duration, or complete state conservation. For example, sequence 9's
original `waiting_yamux_empty_receive` value is `12,958,344,754 ns`, as retained
in the table; the corrected offline value is `12,958,353,970 ns`, an increase
of `9,216 ns`. The report's conclusion depends on seconds of empty-receive and
`recvLock` waiting and is unchanged by that correction. Corrected values must
be cited from their separate generation-status reduction artifacts rather than
substituted into the original rejected reduction.


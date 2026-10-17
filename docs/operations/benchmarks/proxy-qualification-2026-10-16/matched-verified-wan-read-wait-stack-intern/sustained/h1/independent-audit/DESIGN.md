# Independent sustained handler partition audit

This is a source-only adaptation of the byte-identical H1/H2 smoke 3
independent audit at SHA-256
`8e59bed3301abe3097bbee8aaa208e1dd1539e94af74a8e4e3c9537d2d973757`.
It retains that audit's marker cardinality and type checks, exact task and
handler GID joins, event ordering, handler old/new state continuity, region
clipping, zero-duration interval retention, gap-free positive coverage, and
exact Running/Runnable/Syscall/Waiting conservation.

The only input-model change is that logical parsed-trace lines come from the
reviewed `CanonicalReader` in `stack_intern_format.py`, exact SHA-256
`f370243b753a94a122157e9b0b13eb535065fe88746269bd70de5475d82e4dbf`.
The audit binds that module before importing it, consumes the reader through
its public `iter_lines()` interface, requires a validated footer, and checks
the reader's event count against its independent header count. It also binds
the raw and canonical hashes and exact `0\n` adapter, parser, and canonicalizer
statuses from the closed arm's index.

The audit is independent only above decoding. It shares canonical decoding
with the qualification analyzer and cannot independently verify record tags,
dictionary/reference expansion, footer validation, or the compact format
implementation. Its report states this limitation explicitly.

The accepted input shape is one closed sustained arm with exact selected
sequences 9, 11, 14, and 15, all from exactly one `h1` or `h2` protocol.
Execution is intentionally deferred until the sustained run is closed and the
protocol worker releases the process slot. No trace parsing is added to the
timed workload.

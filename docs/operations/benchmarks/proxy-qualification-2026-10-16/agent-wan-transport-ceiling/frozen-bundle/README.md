# Raw TLS versus aggregate Yamux transport ceiling fixture

This is the corrected diagnostic-only Linux fixture built from production base
`ccb9c9cb683b555044033d68ff59112aa4bf21d9` plus the portable four-file overlay
`transport-ceiling.patch`. Its source tree identity is
`40e1046c2f68ee3d545dfd582cd424c2f3604b2a`. The overlay is selected only by
`p2pstream_transport_ceiling`; existing `p2pstream_qualification` DATA counters,
growth traces, and snapshots stay disabled.

It supersedes the immutable failed fixture source `326e4a0016ff14d818eb0483d7789849db0a9fde`.
That fixture completed all 16 aggregate transfers and released both ledgers, but
classified the server's final per-lane Accept EOF as invalid. Its preserved smoke
evidence remains at
`/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/proxy-qualification/transport-ceiling-smoke.1NJkTYCf`.

The correction adds a fixture-local post-phase-16 latch. It is published under the
fixture mutex only after all 16 phases complete, active logical streams reach zero,
logical relays join, five physical sockets are freshly verified TCP_ESTABLISHED,
and `MarkLive` succeeds. The aggregate Accept loop permits EOF only while that
entire state remains true. Earlier EOF and every non-EOF error remain invalid;
`yamux.ErrSessionShutdown` retains its prior behavior. Focused tests cover each
boundary and verify an expected final EOF cannot erase a prior invalid state.

Both endpoint binaries implement `aggregate-yamux` and `raw-tls`, using five
authenticated physical TLS sockets, the fixed 16-phase schedule, and exact 256 MiB
payload digest `ad818ce955253fd9030cbefa352527321b308cfc574cb4bbee9cbcd0d4930790`.
Raw TLS remains an upper-bound comparator: it omits Yamux framing, credit/refund,
keepalive, and per-logical-stream origin lifecycle. TLS runtime heap buffers and
kernel socket memory remain unmodeled by the conservative working ledger and are
reported separately.

Pinned Go 1.26.6 validation on the corrected agent passed normal x10 and race x5;
ordinary tag-off agent compilation passed. Both endpoint binaries were rebuilt.
The helper/server tagged files and all Go/module/Yamux inputs outside the corrected
agent test file are byte-identical to the prior validated bundle, so their earlier
focused normal/race results are carried with exact hashes. No corrected external
smoke or 64-transfer workload has run yet.

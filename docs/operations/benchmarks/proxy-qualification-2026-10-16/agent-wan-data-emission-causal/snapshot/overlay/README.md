Diagnostic-only agent build overlay.

Base ref: 5eeccaaa797254d7bde4db9854a337a02c3bc28e
Apply only before building the aggregate agent binary. Keep the server binary,
legacy comparator, harness, flow-control configuration, and all other source at
the base ref.

The patch preserves the exact current gathered header+body copy and buffer
capacity, then changes only Yamux DATA physical emission from one write to two
writes split at the header boundary. It retains caller-body detachment, the
commit-before-first-byte rule, exact short-write checks, control scheduling,
and ambiguous-error session abort behavior.

No production behavior decision is implied by this diagnostic overlay.

Focused validation after applying the patch:
  go test ./third_party/yamux -run '^TestDiagnosticSplitWrite' -count=20
  go test -race ./third_party/yamux -run '^TestDiagnosticSplitWrite' -count=20
Build the tag-off agent only after those checks pass.

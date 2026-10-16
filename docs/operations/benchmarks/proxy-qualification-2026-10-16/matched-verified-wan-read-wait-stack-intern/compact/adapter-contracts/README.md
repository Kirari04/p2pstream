# Adapter-only lifecycle contracts

This source owns only focused lifecycle, path, environment, stderr, deadline,
and cleanup contracts for the frozen stack-intern parser adapter. It does not
test canonical format semantics, equivalence, reducer behavior, or WAN traffic.

The driver imports the exact checkpoint 7 adapter at SHA-256
`719eac5dac37a0dc686283025e7005aba3cbf5408436b6257e6fde8811a50df8`
and requires checkpoint manifest SHA-256
`8a642d8cd435700f95d8016d8c526fcd178cd5197860228e5db12d275086e6b0`.
The adapter and protocol-worker sources remain unchanged. Each case runs in a
separate process and fresh mode-0700 directory. Tiny fixture children replace
the two external commands only after the adapter has completed its production
path, source, raw-input, and output-path preflight. Test-only constants shorten
the normal/TERM/KILL deadlines and stderr cap.

The cases cover natural status/join behavior and clean environment; signals at
both child-registration boundaries plus another signal during publication;
TERM-ignoring children and KILL fallback; deadline TERM; second-child startup
failure; stderr overflow; parser nonzero with a syntactically complete partial;
raw empty/oversize/nonregular rejection; tool hash mismatch; unsafe working
directory; and duplicate outputs.

No proxy endpoint, Go build, or network traffic is used.

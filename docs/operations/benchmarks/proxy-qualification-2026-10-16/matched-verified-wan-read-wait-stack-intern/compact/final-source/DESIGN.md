# Lossless parsed-trace stack interning derivative

This is an offline diagnostic representation change. It does not change the
WAN endpoint, handler region, trace capture, timed runner, public listener,
copy observer, transport policy, resource policy, or performance gates. The
original direct parsed-text path remains frozen and rejected for sustained
use because its 512 MiB file cap is plausibly exceeded.

The new path keeps the existing 64 MiB raw runtime-trace cap and 512 MiB
physical analyzer-input cap. It introduces a separately declared logical
expanded-text cap of exactly 4,294,967,296 bytes. This is a new offline
representation budget, chosen before traffic to contain the conservative
2.56 GB H2 projection. No buffer is sized to that limit.

`run_interned_trace_parser.py` starts pinned Go 1.26.6 `tool trace -d=parsed`
and `intern_parsed_trace.py` as separate joined processes connected by one
pipe. It waits for both and writes their exact statuses and PIDs to fresh
files. A valid prefix cannot pass: the analyzer requires exact parser and
canonicalizer status `0\n`, and the canonical stream requires its footer.
The adapter pins the exact Go, Python, canonicalizer, and format-module paths
and hashes; uses a fixed non-secret environment; binds a regular raw trace no
larger than 64 MiB before and after parsing; and requires distinct fresh output
paths in one owned working directory. Each child owns a new process group.
The clean environment pins `TZ=Europe/Zurich` to match the frozen parser
evidence and workspace timezone; exact Sync wall lines remain part of the
byte-for-byte expanded-stream contract.
The offline normal deadline is 600 seconds; TERM and KILL join windows are five
seconds each. Parser and canonicalizer stderr are independently drained with a
1 MiB stored cap; overflow invalidates while the pipe continues to drain.
The working directory must be owned by the effective user and cannot be group
or world writable; its identity and mode must remain stable. The signal handler
records rather than raises during child creation, so ownership registration
cannot be interrupted between `Popen` and process-group tracking. Cleanup then
ignores further INT/TERM through exact sidecar publication. Invalid
canonical/ledger remnants are hashed, recorded, and removed only after every
started owned process group has joined; an unjoined group leaves them preserved
and invalid.

The canonical format starts with a fixed versioned magic and uses length
framed records:

- literal records preserve all event headers, markers, blank lines, ordering,
  timestamps, and non-stack continuations byte-for-byte;
- definition records contain a sequential ID, SHA-256, exact byte length, and
  exact stack body, and also represent the first occurrence;
- reference records contain the already-defined ID, SHA-256, and exact byte
  length;
- the terminal footer binds expanded byte count and SHA-256, event count,
  definition count/bytes, record count, and SHA-256 of every canonical byte
  preceding the footer.

Definitions must precede references. IDs are sequential. Digests cannot be
redefined. The producer compares exact bytes whenever a digest repeats, so a
SHA-256 collision with unequal bytes fails. The decoder independently hashes
each definition and rejects dangling references, redefinitions, mismatched
lengths/digests, unknown tags, trailing bytes, and malformed stack bodies.
The separate ledger binds the final canonical file byte count and SHA-256 to
the footer's expanded ledger.

Streaming bounds are exact: 1 MiB per input line, 4 MiB per event, 4,096
continuations per event, 5,000,000 events, 4,096 stack definitions, and 64 MiB
total decoded unique stack bytes. The explicitly bounded payload buffers are
the 64 MiB dictionary, one at-most-4-MiB stack body, and a 1 MiB literal
buffer. Python objects, parsed frame objects, and runtime overhead make total
RSS larger; this design does not claim a hard process-memory bound. The 4 GiB
logical stream is never materialized. The reducer still caps wait stack groups
at 4,096 and output at 8 MiB.

`analyze_interned_trace.py` changes only the input reader and provenance
checks. Marker binding, state partitioning, zero-PC handling, wait
classification, conservation, and acceptance rules are copied unchanged from
the reviewed zero-PC analyzer SHA-256
`93e77e225cc4fae271dfe31a4903564c080600b3218d5d11a787262e164e3455`.


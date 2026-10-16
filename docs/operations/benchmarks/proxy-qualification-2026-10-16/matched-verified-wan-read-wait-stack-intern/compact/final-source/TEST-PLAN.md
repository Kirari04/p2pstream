# Focused verification plan (not yet executed)

No test or trace parser is run before source review.

## Exact closed-trace controls

For each of the same six frozen tiny parsed artifacts (H1 and H2 sequences
9, 11, and 13):

1. Intern into a fresh canonical file and ledger.
2. Stream-expand it into a fresh file and require byte-for-byte equality,
   equal byte count, and equal SHA-256 with the original parsed artifact.
3. Run the derivative reducer with exact parser/canonicalizer `0\n` statuses.
4. Require marker and reduction objects to equal the already accepted
   corrected reduction exactly. Differences are permitted only in the new
   input-representation/provenance fields.
5. Bind process statuses/PIDs, require both children joined, and require no
   remaining process. Parser or canonicalizer nonzero makes the record
   invalid even if the other produced a syntactically complete prefix.

## Format and bound negatives

Construct fixed small canonical fixtures for: missing footer, truncated
record, trailing data, unknown tag, dangling reference, nonsequential ID,
duplicate/redefined ID or digest, definition digest mismatch, reference
length/digest mismatch, malformed stack lines, literal replacing a required
stack record, and a stack record without a marker. Each must fail closed.
An empty background `Stack=` or `TransitionStack=` must round-trip exactly;
the inherited analyzer must still reject a positive handler waiting interval
whose transition has no stack.
The empty-background fixture also places adjacent `TransitionStack=` and
`Stack=` blocks in one event; exact expanded byte/hash equality must preserve
that marker boundary before its final empty stack.

Use locally reduced test-only constants to exercise logical expansion,
physical output, line, event, continuation, definition-count, and total
definition-byte exhaustion without allocating near production bounds. Verify
the production constants separately by exact source/schema assertions. A
forced digest collision with unequal bodies must fail at the producer.

## Status, mutation, and cleanup negatives

- parser nonzero with a valid canonical prefix;
- canonicalizer nonzero with parser zero;
- missing, malformed, or changed status and ledger files;
- ledger canonical/expanded hash, byte, count, bounds, or footer mismatch;
- canonical or ledger mutation between analyzer passes;
- producer input error and output/ledger creation error, requiring no partial
  canonical output or ledger owned by that failed invocation;
- adapter interruption, startup failure, and producer/consumer failure,
  requiring both started child process groups joined and exact statuses
  recorded; TERM-ignoring descendants exercise the bounded KILL fallback;
- SIGINT/SIGTERM delivered at each child-start ownership boundary and again
  during sidecar publication, requiring no unowned group or missing record;
- raw trace nonregular/empty/over-64-MiB/mutated cases, inherited-environment
  exclusion, source/tool hash mismatch, duplicate/aliased/preexisting outputs,
  unsafe or replaced working directory, stderr overflow, and the fixed offline
  deadline. Forced cleanup records and removes partial canonical/ledger files
  only after every started process group joins.

## Static integration checks

The later runner derivative must bind the exact canonicalizer, format module,
adapter, analyzer, schema, and test hashes. It must retain the frozen timed
runner byte-for-byte, run canonicalization only after handler/endpoint exit,
capture parser/canonicalizer/adapter statuses separately, require the process
record's exact natural joins, pin per-sequence canonical/ledger/status paths,
and retain all inherited TLS, protocol, payload, copy, reuse, resource,
lifecycle, floor, and conditioning checks.


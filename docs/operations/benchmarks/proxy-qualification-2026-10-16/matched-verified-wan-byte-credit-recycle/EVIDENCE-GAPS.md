# Evidence gaps and limits

- Both sustained arms have upstream-reuse conditioning failures, so the cohort
  cannot support cross-arm or phase performance attribution.
- The trace begins at receiver eligibility. Application consumption and batching
  before eligibility remain unmeasured.
- The serial proxy copy data retains aggregates, buckets, and the eight longest
  calls, not a per-read record aligned to each byte-credit publication. It
  cannot split pre-eligibility time between receiver reads and consumer or
  scheduler gaps.
- No cross-process timestamp subtraction is used. Local monotonic segments are
  exact only within their owning process.
- Below-TLS raw-write occupancy includes TLS and TCP/poller effects. It does not
  identify kernel backpressure, delivery, congestion, peer consumption, or the
  ciphertext identity of concurrent writers.
- `normal_read` includes initial/open positive updates identified by flags; it
  is not synonymous with consumed-byte recycling.
- The strict ordinary proof failed a generated-symbol-name equality gate. The
  accepted supplement is narrower and is published beside that failure.
- Historical cohorts used Yamux placeholder agent session addresses and cannot
  be retroactively joined by a physical TCP tuple.
- The process-local trace bearer was never persisted and cannot be included in
  exact-value secret comparison.
- The original trace-analysis table renders a millisecond total beside a
  microsecond maximum without labeling the first unit. Its immutable erratum
  corrects the label; the original bytes and all measurements remain unchanged.

These limits leave the original WAN and release qualification gates open.

The staged whitespace check flags preserved trailing spaces and patch indentation
inside immutable raw command/report artifacts. Those bytes are intentionally
unchanged; the package content-hash and authored-metadata checks pass.

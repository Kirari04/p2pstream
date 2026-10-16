# Yamux publisher capture evidence

This bundle records the bounded compiler and correctness evidence for commit
`dc738e9000e60cd7ce3a0279127486f3bc05067a`. The change keeps the fixed
aggregate-refund work arrays out of the queued commit closure and reads the
already-owned in-flight refund ledger when the send commits.

The Go compiler's escape report changed from explicitly moving `work` and
`activeParts` to the heap to reporting `work does not escape`, with neither
fixed array moved to the heap. The callback still escapes because it is queued;
the report shows that it captures the aggregate pointer and the global-credit
scalar. `compiler-before.txt` and `compiler-after.txt` contain the relevant
verbatim excerpts.

This evidence supports elimination of those two allocation sites. It does not
quantify RSS reduction or claim a sustained throughput or latency gain. The
separate tiny-response qualification determines whether the frozen runtime
meets the end-to-end performance gates.

`validation.txt` records the exact compiler and test commands and their
results. `provenance.json` records the toolchain, immutable source references,
and source-file hashes. `SHA256SUMS` authenticates the evidence files in this
directory.

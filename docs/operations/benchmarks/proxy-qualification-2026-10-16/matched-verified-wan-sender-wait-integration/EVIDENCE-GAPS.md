# Evidence gaps and limits

1. The sender observer has no direct public-request-to-Yamux-stream marker.
   Stable interval identity and exclusivity support descriptive attribution,
   not a causal claim.
2. H1 sequences 13 and 14 exceed the 30-second idle policy and open fresh
   origins. H1 is therefore inconclusive for the intended retained-reuse
   comparison even though its correctness and sender records are complete.
3. Exact H2 retained revisits bind the same HTTP connection, origin, lane and
   Yamux stream. A physical session alone could carry a new stream, and numeric
   stream IDs are scoped to their lane rather than globally comparable.
4. The recorded bytes are Yamux-framed plaintext submitted to `conn.Write`.
   Its elapsed time includes TLS serialization, output-lock work and lower-I/O
   blocking; ciphertext bytes, delivery, TCP completion and NIC completion are
   unmeasured.
5. The read-only lifetime analysis has agent receive-growth history but no
   time-aligned server receiver growth or credit snapshot. It cannot explain
   the observed throughput spread or assign a cause.
6. The original static and offline direct-status-0 results retain outer
   tool-envelope status 1. Independent artifact review closes their files but
   does not explain or normalize that discrepancy.
7. One audited build establishes successful compilation with bound provenance.
   It does not establish reproducible compilation from an independent repeat.
8. Binary bytes, endpoint logs/state, readiness files, private keys, caches and
   full reconstructed source trees are excluded. Their available hashes and
   provenance bindings remain; their bytes are not claimed to be published.
9. The process-local trace bearers were never persisted. Exact scans cover the
   recoverable management tokens and private-key values, plus heuristic bearer,
   token-field and private-key markers, but cannot compare the unavailable
   bearer bytes.
10. The package records a failed diagnostic. It does not clear the 29.7 MB/s
   floor, the original 90% WAN gate, HAProxy parity, pressure, capacity,
   integration, release or merge requirements.

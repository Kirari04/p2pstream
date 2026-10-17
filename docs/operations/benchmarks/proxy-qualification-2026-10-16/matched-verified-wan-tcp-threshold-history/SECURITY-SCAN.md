# Nonprinting exact-value selection scan

The mature TCP scanner was adapted to explicit tiny1/tiny2 JSON registrations. It recovers private-key bytes and authentication-like JSON values only from omitted frozen files after checking their hashes, then scans exactly the candidate allowlist. Current result accepted: 1364 candidates/40,296,280 bytes, five recovered labels, zero genuine-hit artifacts, zero values printed. SECRET-SCAN-BINDING.json binds scanner/config/candidate/omission/allowlist/result inputs; PACKAGE-MAP.tsv is required to match the scanned selection exactly.

This bounded exact-value comparison cannot recover process-local values never persisted and does not prove absence of every possible transformed representation. Independent verifier and builder plan checks plus a negative scan-selection mismatch control are required before archive build.

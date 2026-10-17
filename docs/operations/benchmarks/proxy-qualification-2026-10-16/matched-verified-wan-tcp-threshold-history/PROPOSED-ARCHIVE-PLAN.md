# Proposed archive plan

Proposed destination: docs/operations/benchmarks/proxy-qualification-2026-10-16/matched-verified-wan-tcp-threshold-history (not created).

Current candidate map:1364 files/40,296,280 bytes. Archive 1199 files/34,431,145 source bytes once, under ancillary-threshold-history-evidence.tar.gz; loose 165 files/5,865,135 bytes. Large selected sender/control JSON is archived once. Key failed-run reviews/full ledgers/launch/native results remain loose. Additional package metadata is separate from mapped evidence counts.

The reused mature builder requires exact source hashes, one-to-one map/allowlist, accepted exact scan selection and safe regular files. Eventual gzip mtime0 and tar uid/gid/mtime0, empty uname/gname, normalized0644/0755, ordered unique regular members; root SHA256SUMS covers every final file except itself. Independent verifier checks all loose and archived bytes/modes, membership, deterministic metadata, scan bindings and complete root checksums without source-tree dependency.

No archive or tracked file has been created. Coordinator review and an explicit workload-slot grant precede compression. This history cutoff is immutable. The coordinator review is snapshotted in WAN-ASTRA-REVIEW.md and bound by IMMUTABLE-CUTOFF.json; the mutable main review log is excluded. Later failed-capture 106 contracts and local cancellation tests require a separate supplement. Never edit frozen run/source evidence to accommodate packaging. After archive build, run full verifier, root checksums, exact scan-selection negative control and repository checks before any authorized commit/push. PR destination remains dev; no merge/release/deployment is part of this proposal.

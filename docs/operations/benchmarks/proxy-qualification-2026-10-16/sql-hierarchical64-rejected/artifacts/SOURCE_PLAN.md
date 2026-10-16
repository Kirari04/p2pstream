# SQL hierarchical 64 candidate source plan

- Classification: candidate-overlay-only diagnostic; no production source edit, commit, push, release claim, or HTTP/E2E workload.
- Immutable source checkpoint: `8d97b45d7f6c7dc5cf510cc3f4b29e1a54b0debf`.
- Source archive: `source-8d97b45.tar`, SHA-256 `1028e17f0af1be75826be4dbdc1f9570b63dddea60c1b59ccd60c6374ee4c231`.
- Baseline B: `../source-baseline`; fixed-16 inserter SHA-256 `bdd9bd1b658e7d0f20e5a0965d1b710cb8f9e0d92b46fa7e2e2a2b06ed47921f`.
- Candidate C: `../source-candidate`; hierarchical 64 -> 16 -> single inserter SHA-256 `0d6065fc2b3a1823aa8b36c28d746dd7edfde335a372a4686e78a414aa990565`.
- Identical B/C test and benchmark overlay SHA-256: `4ea3a60cd7e6886b7ca474514b80be1db692df43f27f8f93c0757031df3bcf`.
- B/C source trees differ only at `internal/db/proxy_request_event_inserter.go`.
- Toolchain: `/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin/go` (`go1.26.6 linux/amd64`).
- Root, owned temp/build-cache/result directories, and existing module cache resolve to ext4 on `/dev/mapper/ubuntu--vg-ubuntu--lv`; each Go call explicitly sets owned `TMPDIR`, `GOTMPDIR`, and `GOCACHE`. Existing `/home/user/go/pkg/mod` is the identical read-only input module cache for both variants.

The shared overlay preserves the existing 35-row correctness and cancellation cases. It adds a 131-row all-23-fields equality case with primary, tuple, status, retry-rollup, and cache-touch assertions; cancellation at row 65 after the first 64-row execution with whole-transaction rollback assertions; and a runtime query of the pinned SQLite build's `MAX_VARIABLE_NUMBER`, requiring at least `64 * 23 = 1472`.

Focused normal and race tests run on B and C before timing. The finite benchmark is exactly three pairs in order B -> C, C -> B, B -> C, one second per count/cell for 63, 127, and 512 events, with `GOMAXPROCS=4`, `taskset -c 0-3`, `-test.benchmem`, and owned ext4 temporary directories. Advance requires every 512-event C/B ns/op ratio <= 0.95, 63- and 127-event median ratios <= 1.05 with no pair > 1.10, all correctness checks passing, and no unexplained >5% per-event 512 allocation increase. No HTTP/E2E stage is authorized here.

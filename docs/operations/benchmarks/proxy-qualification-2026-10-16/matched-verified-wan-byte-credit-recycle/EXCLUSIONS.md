# Exclusions

The package omits private TLS keys, authentication values, readiness and private
configuration, endpoint logs/state, runtime directories, binaries, compiled
archives, caches, full build/source trees, and database files. No selected raw
artifact is rewritten or sanitized.

`EXCLUDED-RUN-ARTIFACTS.tsv` lists each omitted file from the three closed WAN
runs with its exact source-ledger SHA-256 and reason. `HASH-ONLY-BINARIES.tsv`
records excluded audited binaries. The full immutable run ledgers remain loose
and cover selected and omitted files together.

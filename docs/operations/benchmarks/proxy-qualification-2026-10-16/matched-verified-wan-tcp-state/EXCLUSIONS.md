# Exclusions

The package omits private TLS keys, bearer and authentication values, readiness
and private configuration, endpoint logs/state, runtime directories, compiled
binaries and archives, caches, full reconstructed repositories, active mutable
workspaces, and database files. No selected raw artifact is rewritten or
sanitized.

`EXCLUDED-RUN-ARTIFACTS.tsv` lists each omitted file from the two closed WAN
runs with its exact source-ledger SHA-256 and reason. Each 271-file run is
partitioned into 135 selected and 136 omitted files. `HASH-ONLY-BINARIES.tsv`
records excluded binaries and reconstructed source inventories. The immutable
full-run ledgers remain loose and cover the selected and omitted bytes together.

Public self-signed certificate PEM files are selected so their leaf identity can
be recomputed. Private-key bytes remain excluded and are bound only by their
full-run ledger hashes.

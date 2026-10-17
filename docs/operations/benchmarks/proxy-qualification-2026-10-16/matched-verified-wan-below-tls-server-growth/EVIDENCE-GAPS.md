# Evidence gaps and limits

- The completed H1/H2 cohort fails the fixed performance gate. No result here clears the original 90% WAN blocker or the HAProxy, pressure, capacity, integration, release or merge gates.
- The selected intervals show an association between direction-cold H2 throughput, credit-window growth and sender wait. Peer window-update publication/wakeup timing is absent, so cause versus consequence remains unresolved.
- The below-TLS observer counts encrypted buffers submitted by `tls.Conn`; it does not decode records or measure socket delivery, TCP acknowledgement or NIC completion. The Yamux ledger separately counts framed plaintext above TLS.
- The strict ordinary-build equivalence proof remains rejected. The supplemental six-target/eight-site result is deliberately narrower and does not convert the earlier result into a pass.
- The first sustained attempt has nine H1 rows and forced inner cleanup. It is preserved as invalid partial evidence and is never combined with the completed cohort.
- Full binaries, compiled archives, caches and reconstructed source trees are omitted. Immutable source/build ledgers preserve their hashes and provenance, but the omitted bytes are not reconstructable from this package alone.
- Private TLS keys, readiness/authentication values, endpoint logs/state and temporary runtime trees are excluded. The exact-value scan cannot compare a process-local bearer that was never persisted and does not prove absence of every transformed secret representation.
- Historical outer-tool exit discrepancies are preserved. Direct, saved, wrapper, validator and native statuses are not normalized into a synthetic status.
- Raw command and compiler evidence may contain intentional trailing spaces or blank EOF lines. Those bytes remain unchanged; package content and checksum verification is authoritative.
- This package makes no CI claim newer than repository head `913dfc4`.

# Matched monotonic copy-attribution sustained cohort

This is diagnostic evidence, not qualification evidence. The observer adds timing work, and both arms failed at least one 29.7 MB/s phase gate. The cohort ran once with the same audited server (`0530cdc308bf06964f61d55ca730309e125cabd3c296ed3c22f0cfe218552013`), unchanged agent (`9e635d6e1e8fbd8651260eb081fb5fc7847438bc51c653a5f7785cbecde4a137`), and one independently fingerprinted certificate (`7d3625723c898ac278292007eab9dc19ec5df4dcd5b544fe0528149eeb38f0c8` DER SHA-256). H1 ran before H2 in fresh owned outer topologies.

Both arms produced all 16 ordered rows. Every response had the requested HTTP major, ALPN, certificate fingerprint, payload, status, and one persisted event. Every response-copy record had one body wrapper, zero `ReadFrom` calls, exact response bytes, no read/write errors or short writes, conserved histograms, bounded stalls, and one complete publication. For every row, each metric span was within monotonic handler elapsed, `read.span_ns == read.total_ns + gap.total_ns`, and the separate Read+Write and Read+gap budgets fit the handler duration. Inter-read gap overlaps downstream Write and bookkeeping and is never added as a third independent component. POST records cover only the 32-byte response copy.

## Download phases

| Protocol | Seq | Phase | Rate MB/s | Handler s | Read s | Write s | Inter-read gap s |
|---|---:|---|---:|---:|---:|---:|---:|
| H1 | 1 | warmup | 33.500331 | 7.835841 | 7.354541 | 0.314490 | 0.319195 |
| H1 | 2 | warmup | 34.075148 | 7.698502 | 7.206510 | 0.324407 | 0.329286 |
| H1 | 3 | warmup | 33.937051 | 7.751066 | 7.274522 | 0.310087 | 0.314753 |
| H1 | 4 | warmup | 33.837774 | 7.756445 | 7.276299 | 0.313973 | 0.318722 |
| H1 | 9 | retained-warmup | 29.451921 | 8.936370 | 8.441698 | 0.328359 | 0.333305 |
| H1 | 11 | steady | 29.673001 | 8.870669 | 8.392723 | 0.310712 | 0.315457 |
| H1 | 14 | steady | 29.738119 | 8.908625 | 8.521379 | 0.301976 | 0.306629 |
| H1 | 15 | steady | 34.174107 | 7.693073 | 7.337393 | 0.270949 | 0.274954 |
| H2 | 1 | warmup | 33.589915 | 7.812034 | 7.032550 | 0.610681 | 0.616324 |
| H2 | 2 | warmup | 33.305726 | 7.931299 | 7.132345 | 0.551835 | 0.556880 |
| H2 | 3 | warmup | 33.946594 | 7.747486 | 6.908355 | 0.590803 | 0.596197 |
| H2 | 4 | warmup | 33.603096 | 7.810850 | 7.046616 | 0.596933 | 0.602464 |
| H2 | 9 | retained-warmup | 33.387702 | 7.870142 | 7.150884 | 0.551375 | 0.556597 |
| H2 | 11 | steady | 33.841246 | 7.755608 | 6.967185 | 0.620029 | 0.625834 |
| H2 | 14 | steady | 14.550217 | 18.305310 | 17.333642 | 0.802516 | 0.809880 |
| H2 | 15 | steady | 33.603938 | 7.810570 | 7.048709 | 0.594716 | 0.600291 |

H1 is `observational-complete`: all four post-close lane revisits reused the exact physical key and origin identity after 25.153–25.420 seconds. Its retained-warmup sequence 9 and steady sequence 11 fell below the diagnostic floor.

H2 sequence 14 was an exact same-key, same-origin reuse after 23.982 seconds and then took 18.305 seconds. Sequences 15 and 16 reached their original lane keys after 34.588 and 35.001 seconds, beyond the unchanged 30-second idle deadline. Both opened new origin connections with `Reused=false` and `WasIdle=false`. The arm therefore remains `inconclusive-upstream-reuse`; those rows are expiry-confounded and are not classified healthy. H2 sequence 14 also caused the steady-phase floor failure.

The measurements distinguish time inside upstream `Read` calls, downstream `Write` calls, and the overlapping intervals between Read calls. They do not prove why an upstream Read blocked or attribute causality to a particular flow-control, scheduler, or yamux implementation. The inherited shallow `agent_tunnels_initial` lane fields may reflect later maxima, and high-water fields are componentwise samples rather than coherent state snapshots.

All ten endpoint and namespace processes exited naturally with unique identities inside each arm. Both outer process groups joined, all requested namespaces were private, and pair and arm pre/post manifests match. The wrapper and tool envelope both returned status 1 because the preserved diagnostic gates failed; their statuses agree and no tool-envelope discrepancy occurred.

The machine-readable independent audit is `offline-audit.json` (`98178534cae530d1cef826ee95a8eb725ce5613792cf214b8076e783cdb9bbb0`). The safe artifact index is the sibling `matched-copy-monotonic-sustained-1.artifact-index.json` (`812ccdc66a07b27554a8fe8b5b358af0e61622af4baddb69d4a5ce92dc45da6d`).

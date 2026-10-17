# Exact nonprinting selection scan

The mature exact-value scanner checks this supplement's exact 70-file, 317,108-byte loose selection. It recovers private-key bytes and authentication-like JSON values from the original frozen tiny1/tiny2 omitted-artifact tables only after checking those file hashes. Those private bytes are never copied into this supplement. Five recoverable labels were checked; the scan accepted with zero genuine-hit artifacts and zero values printed.

PACKAGE-MAP.tsv, ALLOWLIST.tsv and SECRET-SCAN-INPUT-ALLOWLIST.tsv bind the same exact candidate selection. SECRET-SCAN-BINDING.json binds scanner/config/selection/omission/allowlist/result inputs. Builder and independent verifier reject a mismatch between the scanned selection and map. The retained negative control removes one scan row and must reject before copying.

This bounded exact-value comparison does not recover process-local values never persisted and does not prove absence of every possible transformed secret representation. Go cache/module/temp payloads are outside the evidence selection and were not scanned or traversed; their records boundary is explicit in OMITTED-ROOTS.tsv. No archive or compression is used.

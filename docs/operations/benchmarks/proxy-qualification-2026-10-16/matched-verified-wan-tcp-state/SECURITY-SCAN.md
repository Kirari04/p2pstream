# Security and omission scan

The reviewed 1,372-file selection received one nonprinting exact-value scan.
The scanner source, candidate allowlist, both 271-file selected/omitted
partitions and result are bound by `SECRET-SCAN-BINDING.json`.

The scan recovered six exact labels from excluded closed-run files: two private
keys and four authentication-like JSON values. It found no matching selected
artifact and printed no secret values or matched content. Process-local values
that were never persisted cannot be recovered for comparison, and exact-value
matching does not prove the absence of every possible secret representation.

The package verifier enforces a one-to-one relationship among the allowlist,
package map, archive members and loose files, validates deterministic archive
metadata and covers every package file except the package root checksum itself.
It must not infer source filesystem modes: selected content bytes remain exact,
while package modes are explicit normalized metadata.

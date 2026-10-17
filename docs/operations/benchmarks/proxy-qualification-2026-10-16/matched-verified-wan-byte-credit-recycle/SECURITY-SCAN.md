# Export safety review

The selection is explicit and rejects symlinks, traversal, duplicate sources and
destinations, private-key suffixes, readiness/private endpoint trees, binaries,
caches, compiled archives, databases, and full source/build trees.

The final nonprinting scan recovered eight private-key or authentication-like
values from excluded closed-run artifacts and compared their exact bytes against
all 3,713 selected files. It found zero genuine hits and printed zero values.
The scan limitation for the unavailable process-local bearer is recorded in
`SECRET-SCAN.json`.

Public self-signed certificate PEM files are included so the leaf DER hash and
SAN can be independently recomputed. Private key bytes are excluded and remain
bound only through the immutable run ledgers.

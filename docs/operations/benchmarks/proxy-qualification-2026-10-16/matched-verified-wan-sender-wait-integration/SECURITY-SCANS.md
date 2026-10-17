# Nonprinting secret scans

The small-smoke selection scan covered 168 files and 2,018,084 bytes. It
recovered six value labels and reported zero exact or heuristic hit artifacts.
Its report SHA-256 is
`5a66145cd2d86c6216b3e8b262b84931355b7fb595c4492c344583af33239ff7`;
the frozen scanner source is
`2d56cce2eb9ba7b2f3ae8649f0f78ab1bf85d8230ede2d3c116d172d5ff25599`,
and the exact scanned allowlist is
`4624d0b6faaa4ff74afd62146066d83a9955267236cc7862dcd937b0c6d13503`.

The sustained selection scan covered 168 files and 3,349,314 bytes. It also
recovered six value labels and reported zero exact or heuristic hit artifacts.
Its report SHA-256 is
`b79735cdddc27b976ac3bdc7b3eb815e2eea4637487d2e89aa03223423ee696d`;
the frozen scanner source is
`f9b627f141957cfda9acfc9d74f6ac80beb966ebbc8975c82e7658be7ce907c4`,
and the exact scanned allowlist is
`567dd28daff782e4826bb280260686fc99d1fe2888c9aa6d07458139e7d19e5e`.

Both scans emit labels, counts and artifact names only. They never print a
secret value or matching content. A genuine hit would omit the whole artifact;
raw bytes are never rewritten. The process-local trace bearers were not
persisted, so their exact values could not be compared.

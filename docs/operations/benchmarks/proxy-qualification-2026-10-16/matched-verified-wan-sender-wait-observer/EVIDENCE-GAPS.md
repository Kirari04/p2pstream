# Evidence limits

1. This package covers observer source and offline validation attempts 1–5.
   It does not include the later integration runner, authenticated controls,
   supervisor/coordinator, build, or traffic evidence.
2. Attempt 1 is a materialization failure. Attempt 2 is a test-source compile
   failure. Neither is evidence about runtime observer behavior.
3. Attempt 3's first comparator source was not separately frozen. Its exact
   output and status are preserved, but exact source replay is unavailable.
4. Full source trees, source tar archives, build caches, compiled artifacts and
   the large checkpoint-1 whole-repository patch are omitted. Existing hashes
   and manifests bind the available origins; the omitted bytes are not
   published by this package.
5. The secret screen is restricted to this offline corpus and any relevant
   recoverable excluded fixtures. No such secret-bearing fixture exists here,
   so no exact secret values were available. This is not a scan of unrelated
   workspace credentials.
6. Raw artifacts retain original whitespace and end-of-file form. Package
   checksum and content verification, rather than a repository-wide whitespace
   rewrite, protects those bytes.

The package establishes no runtime cause, performance fix, or qualification
gate clearance.

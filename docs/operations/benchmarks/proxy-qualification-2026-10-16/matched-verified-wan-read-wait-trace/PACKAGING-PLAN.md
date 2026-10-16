# Package layout

`ALLOWLIST.tsv` is the reviewed byte-selection authority. `PACKAGE-MAP.tsv`
partitions each allowlisted source exactly once into an archive member or a
readable loose artifact; the export did not sanitize or rewrite evidence.

## Boundary

`ancillary-source-contracts.tar.gz` contains source checkpoints,
patches, synthetic fixtures, no-traffic contracts, test/build provenance, and
historical source attempts. These 4,745 entries account for 28,884,065 source
bytes and most of the fixture-heavy file count.

The 597 actual diagnostic-run entries remain loose and readable, totaling
5,912,833 source bytes. This side contains scope-probe attempts, failed smoke
rows and statuses, lifecycle and resource records, public certificates,
original rejected reductions, the six reviewed raw traces, and the separate
zero-PC offline reductions. A path is assigned to one side only, so the tracked
package does not store the same selected artifact both loose and in the
archive. Checkpoint paths with equal content remain distinct only where their
path identity records a reviewed historical checkpoint.

The package metadata stays loose and readable:

- `README.md` explains evidence meaning and limitations.
- `ALLOWLIST.tsv` binds original source paths, proposed destinations, categories,
  and SHA-256 values.
- `PACKAGE-MAP.tsv` adds placement, byte size, and normalized file mode.
- `ANCILLARY-BUNDLE.sha256` binds the complete compressed archive byte stream.
- `verify-evidence-package.py` independently checks the bundle SHA, every member
  name, type, normalized metadata, size and SHA-256, plus every loose file.
- `build-ancillary-bundle.py` validates source bytes and creates the bundle only
  with an explicit `--build --output-root ...` invocation.

## Deterministic archive contract

The builder processes archive members in lexical destination order. Every
member is a regular file; directories, symbolic links, hard links, absolute
paths, `.` components and `..` components are rejected. Source files and every
source path component are also rejected if symbolic links are present.

GNU tar headers use the mapped `0644` or `0755` mode, size from the map, UID and
GID zero, empty user/group names, and mtime zero. Gzip uses level 9, an empty
embedded filename and mtime zero. Original file content is streamed unchanged.
The builder writes a temporary file and atomically replaces the final archive,
then writes `ANCILLARY-BUNDLE.sha256`.

The verifier does not import builder code. It streams archive members without
extracting them and rejects missing, duplicate, unexpected, linked, or
non-regular entries. This provides an independent per-entry check rather than
relying only on the compressed bundle hash.

## Validation

The source-corpus validation commands are:

```text
python3 check-allowlist.py --source-root /path/to/frozen-source-corpus
python3 build-ancillary-bundle.py --source-root /path/to/frozen-source-corpus --check-plan
python3 verify-evidence-package.py --check-plan
```

The first two commands independently hash selected source bytes. The last
checks the standalone package map structure. The published package is verified
with `python3 verify-evidence-package.py`.

The builder is retained as historical export provenance. It requires the
ignored frozen source corpus named by `PACKAGE-MAP.tsv`, supplied explicitly
with `--source-root`; a clean checkout containing only the published package
cannot reconstruct that absent corpus. The standalone verifier needs only the
published package and does not depend on the source corpus.

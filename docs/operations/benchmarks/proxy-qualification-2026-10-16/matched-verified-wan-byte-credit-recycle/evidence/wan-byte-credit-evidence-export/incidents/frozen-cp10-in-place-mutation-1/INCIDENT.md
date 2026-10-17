# Frozen checkpoint 10 in-place metadata mutation incident

During audited build attempt 4, two files in the already reviewed adapter
checkpoint 10 were briefly rewritten in place. No Go overlay, patch, test,
runtime, binary, runner, or traffic artifact changed.

Changed files:

- `CHECKPOINT.json`: reviewed/restored SHA-256 `0c0abe6a8df3f774896ccb0e34f1110d29c20fa9677ff6f621b9cb2b166d016e`; transient
  SHA-256 `94b975c9122e48e9464dd07d5fbf155b65add09743f654d3a3041721a7e5ed86`.
- `SHA256SUMS.txt`: reviewed/restored SHA-256
  `34ed296a37112ce876873b0a7d04ef41ae0f5e4caa3a76416c8119fea742f9f0`;
  transient SHA-256
  `e7182d2aa8711355550cce662b471f7ece1e56bd4863a2b36e01738ee3b6a942`.

The transient metadata renamed the attempt-3 `FINAL` field to identify
`records/FINAL.json` and added the distinct top-level `FINAL` marker hash.
It did not change the checkpoint's source or test bytes. The files were then
restored byte-for-byte to the reviewed 34ed manifest, whose complete internal
checksum verification passes.

The exact mutation start time was not recorded and is not reconstructed. The
restored filesystem mtimes were observed as:

- `CHECKPOINT.json`: `2026-10-17 06:53:28.489133841 +0200`
- `SHA256SUMS.txt`: `2026-10-17 06:53:28.489673941 +0200`

Audited build attempt 4 is provenance-invalid regardless of its command/test
outcome because it overlapped this in-place mutation. It must not be used as an
accepted build. Checkpoint 10 remains restored and must never be edited again.
The metadata correction will be represented only in a new checkpoint 11 after
attempt 4 closes.

# Reconstructed failed focused-test helpers

These two files reconstruct the exact `proxy_qualification_wan_protocol_test.go`
snapshots used by the first two focused normal-test checkpoints. The original
files were superseded in the temporary build tree, but their SHA-256 values
were recorded before each test.

`reconstruct-failed-helpers.py` starts from the preserved final helper with
SHA-256
`b0b6d037d51892e89d39e6f55107f73c30647ad9a20283a026007fd69b3be42d`.
It reverses only the documented fixture edits:

- Attempt 2 replaces the conditional test-server ALPN list with its preceding
  unconditional `h2,http/1.1` list.
- Attempt 1 additionally removes the later trace-store test and its `net`
  import, and removes the test server's explicit `NextProtos` field.

The script refuses to write either file unless its digest exactly matches the
recorded pre-test value. The verified results are:

- Attempt 1:
  `a1b82b984ec77769f8f27e7d593764819b6de06aef5a140daee9ad30dde84f05`
- Attempt 2:
  `6bddf6b6b46910edacdfc67570469998d06b5289c6be8d9f7fd0b7377b500220`

No Go build or test was run during reconstruction. The original failed status
and output logs remain the execution evidence; these reconstructed files add
the exact hash-verified helper inputs needed to review or replay those
checkpoints against the recorded base source and overlay.

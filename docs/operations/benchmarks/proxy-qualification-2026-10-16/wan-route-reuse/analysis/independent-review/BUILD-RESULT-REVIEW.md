# Independent saved ordinary build review

Accepted build evidence. This reviewer read saved artifacts and hashed files only; no validator, test, build or endpoint was executed.

Receipt `../wan-route-reuse-build-20261017/build-once/receipt.json` has SHA256 `d428e45fd861c8ed7a4346920e53c2a56d1fe678660fd343707f9841f53af624`, matching the reviewed plan `ffa870be730c767237622d4472548bae3bdf628e6a439e99efe827c99fc3c0b2` and accepted focused-check result `aa21fb8cff45b18d041f62282c29bb7815c30c1f9f76000dbb81bdfdccece251`.

All six fresh command records show source unchanged, natural zero exit, accepted cleanup, no forced cleanup, no adopted descendants and no remaining ownership. All 24 saved command-proof/log file hashes and sizes were independently checked with no mismatch. Settings match ordinary Go1.26.6/linux/amd64/CGO1 with empty GOFLAGS, and module verification reports success. Both endpoint metadata files contain ordinary test-binary builds with no qualification tag or race instrumentation.

Both actual executable hashes and sizes were independently checked:

- Server: `5d7552f7a51f907dcbbd4c7b3e73590a3a887cbf1333cc7da058a8e23ee27577`, 58,637,352 bytes.
- Aggregate agent: `c8f541da6c4be5ea3ed4ae3cc1df9bfb188da210d78ffc00962602354ff41537`, 19,994,870 bytes.

The receipt preserves the exact 430-file source identity `b359dc67c290193f68bf33519853df3473e31f9a4d8e718c0bd1ccc282d14947` and the honest patched-source reference. Recorded combined artifact size is 1,227,198,067 bytes, below 4 GiB; new build proof is 47,494 bytes, below 256 MiB. No endpoint or WAN traffic ran as part of this build. Runtime-input guard and qualification results remain separate approval/evidence boundaries.

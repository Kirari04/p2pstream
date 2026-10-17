# Preserved partial run: validator rejected the legacy control lane

This diagnostic fixture stopped after 17 result rows and 33 saved drain
responses. The helper incorrectly required every session in a negotiated
aggregate group to expose aggregate send counters. Lane 0 intentionally uses
legacy Yamux and reports `enabled:false`; only bulk lanes 1--4 are aggregate.

The helper decoded the next response, performed that incorrect assertion, and
raised `ValueError: send-counter wire mode mismatch` before saving the decoded
response. That final response is therefore absent. The runner exited 1 after
140.429 seconds. This artifact is retained to make the fixture failure and its
data loss explicit; it supports no runtime, correctness or timing conclusion.

The successful parent evidence uses the same clean binaries. Its
`validator-correction.patch.txt` changes only the lane-shape check and saves
decoded metadata before subsequent validation. This directory preserves the
original helper, runner diff, log, raw rows, saved drains, manifest and exact
command.

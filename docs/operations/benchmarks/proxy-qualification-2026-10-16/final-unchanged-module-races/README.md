# Frozen unchanged-module race checks

Both race phases pass on immutable source
`5f8dd01f70994dd8b3e1907ccc1f24b4f68f99e6`, using Go 1.26.6 on Linux/amd64.
The source was extracted with `git archive`; no experimental overlay was applied.
All four temporary-directory variables point to an owned ext4 directory.

| Phase | Passed tests | Skipped tests | Test elapsed |
| --- | ---: | ---: | ---: |
| Full Yamux, `-race -short -count=1 -timeout=5m` | 195 | 2 | 20.576 s |
| System metrics, `-race -count=1 -timeout=2m` | 32 | 0 | 1.025 s |

The two deliberate Yamux stress-test skips follow `-short`; no test fails.
The raw JSONL logs, exact wrapper, source-input hashes and execution manifest
are preserved beside this file. The manifest also binds the complete source
archive by hash; that archive is reconstructible from the Git commit and is
not duplicated here. `SHA256SUMS` binds these retained files.

These results can carry forward only while the hashed module files and their
module inputs remain byte-identical. Agent, tunnel and server integration races
remain pending any retained physical-tunnel socket change. Passing these phases
does not clear the repeated WAN download failure or establish performance parity.

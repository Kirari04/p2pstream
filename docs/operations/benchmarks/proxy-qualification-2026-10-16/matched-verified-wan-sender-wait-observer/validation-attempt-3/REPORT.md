# Sender-duration observer focused validation attempt 3

## Result

The cp4 source compiles and all authorized focused contracts pass in normal and race modes. The strict ordinary-build proof is **not accepted**: ordinary code generation has one real five-instruction difference in `(*Session).sendLoop`, and the compiler's inliner cost for `(*Stream).write` rises from 1227 to 1231. The observer therefore does not meet an exact ordinary-instruction-identity claim in this checkpoint. No source was changed or test repeated after this finding.

This attempt is a fresh successor to preserved attempts 1 and 2. It uses cp4, whose only cp3-to-cp4 change is the authorized test compile correction (`[]int` to `[]uint32`, and `int(payload)` at the one `beginQualificationDataEnqueue` call).

## Frozen inputs and materialization

- Source commit: `8ac4c8df8237e37777ba2fac0da6f949da17bdea`
- Source tree: `6c460a3b1362b2711bbd3d48c722fb86f5210e14`
- Explicit active-worktree archive: `inputs/source-8ac4c8d.tar`, SHA-256 `b625da9ccbe17c8b43ff1e5c1078bd8fb24aff4c995fcfd23302ac83c0c3d9f9`
- cp4 checkpoint manifest: `bd5e8d7ae486e460749ae06fd701ce562e3ef678c872edce014aa08a8fa78b4f`
- cp4 source patch: `e35020414a624f7105394fb268e9061472cfd2a536380532edc873ffcdc0d96e`
- cp3-to-cp4 diff: `a75a688f197b101454a4e2579ad1dcaa8b54e3dabb3100ed1ec91abb856c7e7f`
- Baseline and candidate pre-overlay inventories are byte-identical: `0f5c95ac540b8c9b065a0077def6517933aeec69e68465ce5c6b5ca12689b5c5`.
- Candidate post-overlay inventory is `inputs/candidate-post-overlay.json`, SHA-256 `1c3d21c5e614479c384a3e4470c7d17be114fc375ca2bae8654fa161049ebf1b`; the full-path diff against baseline is exactly the cp4 overlay.
- Root and nested Yamux `go env GOMOD` gates passed before execution.
- Baseline and candidate source inventories were unchanged after validation; both comparison status files contain `0`.

Pinned Go 1.26.6 binary SHA-256 is `29e6e0b8be61beb1489ceae62b304343566de8a1dc700af74bde7aeb9c80ad45`. All commands used CPUs 0-15, `GOMAXPROCS=16`, offline module resolution, and owned ext4 cache/temp roots. Exact commands and environment selections are retained.

## Focused contracts

Every direct command, enumeration audit, and the focused outer shell returned 0.

| Group | Exact top-level tests | Result |
|---|---:|---|
| Yamux normal, qualification tag | 19 | pass |
| Yamux race, qualification tag | 19 | pass |
| Agent schema | 2 | pass |
| Server schema | 2 | pass |
| Ordinary layout | 1 | pass |

The JSON audits bind the expected, run, and passed test names exactly. They include actual credit-wait wake/terminal paths, blocked enqueue timeout and join, physical write outliving caller shutdown, Session.Close/sendDone terminal publication, late accepted publication after send-loop exit, reservation-to-submission ownership, prepublication race accounting, and the prior aggregate-counter contracts.

## Ordinary-build proof

Baseline and candidate ordinary builds, package exports, compiler assembly dumps, and escape dumps all returned 0. Relevant `go tool nm -size` symbol sizes match exactly, and compiler STEXT size/args/locals/frame metadata match for all nine affected functions.

The strict comparator preserves instruction bytes, instruction order, opcodes/operands, relocation annotations, frames, and compiler decision text. It normalizes only whole-function load addresses and in-function target addresses. Source file line/column positions are retained separately.

Eight affected functions have identical ordinary machine instructions. `(*Session).sendLoop` differs at five instruction records around the indirect `conn.Write` interface method resolution:

| Relative offset | Baseline | Candidate |
|---:|---|---|
| 1020 | `488b7230  MOVQ 0x30(DX), SI` | `488b7228  MOVQ 0x28(DX), SI` |
| 1024 | `488b5228  MOVQ 0x28(DX), DX` | `488b5230  MOVQ 0x30(DX), DX` |
| 1028 | `488b5228  MOVQ 0x28(DX), DX` | `488b7628  MOVQ 0x28(SI), SI` |
| 1041 | `4889f0  MOVQ SI, AX` | `4889d0  MOVQ DX, AX` |
| 1044 | `ffd2  CALL DX` | `ffd6  CALL SI` |

This looks like a register/interface-temporary allocation change, but the bytes are genuinely different and the proof does not normalize them. Function length and stack frame remain equal.

Escape classifications selected for the affected functions are otherwise unchanged after source-position-only normalization. One compiler decision text differs: `(*Stream).write` remains non-inlineable, while its reported cost changes from 1227 to 1231. Raw compiler outputs, canonical records, and the exact difference JSON are retained.

The baseline/candidate package archives and test binaries have different whole-file hashes, consistent with the retained metadata and code difference. They are not treated as equal artifacts.

## Attempt and status provenance

- Started: `2026-10-16T22:26:00.440850582Z`
- Ended: `2026-10-16T22:32:30Z`
- Focused outer status: `0`
- Ordinary build outer status: `0`
- Ordinary export outer status: `0`
- Ordinary compiler outer status: `0`
- Relevant nm comparison status: `0`
- Strict ordinary comparator attempt 1 status: `1`, due solely to its failure to consume blank objdump separators; stdout, stderr, and status are preserved. Its pre-correction source was not separately frozen, so this report does not claim a source hash or reconstruct it.
- The mechanically corrected strict comparator ran once more and returned `1` for the substantive code/escape differences above. Its source hash is `3ce9fd012160d7b537c7763f99808b65d0ca1185cb989914d59d3a8c9476b944`.

No endpoint, WAN traffic, production binary, tracked runtime file, deployment, commit, push, or merge was involved.

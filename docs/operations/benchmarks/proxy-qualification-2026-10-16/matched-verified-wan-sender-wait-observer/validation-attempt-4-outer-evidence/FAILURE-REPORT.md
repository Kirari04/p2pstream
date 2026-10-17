# Sender-duration observer validation attempt 4

This was the single authorized execution of frozen source checkpoint 7. It failed and was not retried or modified.

## Provenance and outer status

- Frozen checkpoint: `review/source-checkpoint-7`
- Required and observed `SHA256SUMS.txt` SHA-256: `4b59cc0bf6a38a4d8ad577122f7ea393dcffb89fdc1f60cfdbbd7aafa50794f1`
- Whole-checkpoint verification immediately before `run-all.sh`: status 0
- Whole-checkpoint verification after `run-all.sh`: status 0
- Launcher SHA-256: `1ab3374bbefec1b525eb99694311de3ebfacefd9adeea45961b4204e616d667f`
- Tool envelope status: 1
- Outer final status: 1
- Inner `run-all.sh` status: 1
- Start: `2026-10-16T22:48:34.141703648Z`
- End: `2026-10-16T22:48:43.876686177Z`

The inner `CP5_VALIDATION_FINAL` string is a stale label only. The checkpoint hash and whole-checkpoint checks bind this run to checkpoint 7.

## First failing gate

Preparation and every preflight gate passed. The first validation phase, `layout`, failed:

- baseline layout `go test -json`: direct status 0
- baseline layout evidence audit: status 1
- candidate layout: not run
- focused normal/race/schema tests: not run
- ordinary builds, exports, compiler diagnostics, and proof: not run

The raw baseline JSONL SHA-256 is `b5a60bffd13f44c0f8da839ce63302442140f852af182428b5f8359d25a59b56`. Go emitted the long layout log over 13 test `Output` events. The marker event contains only a 946-byte fragment; ten following events are 1,024-byte continuations and the final data continuation is 563 bytes. `extract-layout.py` collects only the event containing `QUALIFICATION_LAYOUT_JSON=` and calls `json.loads` on that fragment, which raises `JSONDecodeError` at character 945.

A read-only offline diagnosis concatenated the existing ordered test `Output` values. The resulting 10,724-byte payload parses and names `Stream`, `Session`, `aggregateWindow`, and `sendReady`. This diagnosis does not change or accept the failed audit gate.

## Preservation

The raw traceback, JSONL, direct/audit statuses, commands, timestamps, materialization inventories, and source post-run checks remain in the attempt directory. Post-run baseline and candidate inventories completed with status 0, and both unchanged comparisons returned 0. No endpoint or WAN traffic ran.

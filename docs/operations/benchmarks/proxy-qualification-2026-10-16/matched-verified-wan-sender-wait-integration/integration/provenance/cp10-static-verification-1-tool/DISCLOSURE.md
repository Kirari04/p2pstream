# Cp10 scoped static verification tool envelope

The saved direct result is complete and all seven direct checks are status zero:
the two Bash syntax checks, Python parse check, reviewed-binding contract,
whole-corpus verifier, canonical-validator byte equality, and source pre/post
comparison. `RESULT` is `complete`; the reviewed verifier accepted all 31
files. The closed result ledger is
`d3d19790e91f412d050573ebd9b0a15c4bb67fba8cd3a545ca0cebd007192e13`.

The surrounding tool returned exit status 1 with empty output. The exact tool
result is retained in `RAW-TOOL-RESULT.json`. `COMMAND.txt` in the result root
records the intended inner operations, but the exact full outer shell body is
not available after context compaction and is not reconstructed here. No
failing saved shell condition is supported by the direct artifacts. The outer
status remains non-green and unexplained. No check was rerun.

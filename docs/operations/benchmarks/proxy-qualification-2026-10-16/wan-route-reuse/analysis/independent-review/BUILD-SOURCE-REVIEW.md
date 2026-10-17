# Ordinary endpoint build source review

GO for root-owned execution of the six-command build plan below. This reviewer inspected source and saved proof only; no build, test, validator or endpoint was executed.

The plan builds the server and aggregate agent from the same actual current production worktree, with its exact 430-file identity pinned to the accepted fresh normal/race result (`aa21fb8cff45b18d041f62282c29bb7815c30c1f9f76000dbb81bdfdccece251`). The source identity is checked before and after every command. The source reference honestly includes the base revision plus the reviewed patched-source hash. There is no copied diagnostic tree, early-share helper, tagged compilation, old current-binary substitution or altered compiler flag intended to change runtime performance.

All six commands are fresh and sequential: settings, offline module verification, two ordinary `go test -c` builds and two `go version -m` observations. They cannot launch test endpoints. The metadata checker requires Go1.26.6/linux/amd64/CGO1 and rejects qualification tags and race instrumentation. Output binary paths are new and cannot overwrite a prior attempt. The exact legacy executable remains a separate qualification input rather than a new build output.

The executor requires a separate exact root grant. It reuses only the mature command-ownership supervisor and `natural()` checker, with their pinned source; it does not reuse their historical source/settings/build receipts. Command argv, natural zero exit, joined ownership, no adoption/remnants, exact complete log sizes/hashes and source stability are checked. Limits are explicit polled rejection thresholds: combined dependency/cache/temp/binary/check/build artifacts 4 GiB, new build proof 256 MiB, each stdout/stderr 1 MiB, each binary 256 MiB. Logs remain retained and overshoot is acknowledged. Failure preserves the attempt and cannot silently retry.

Reviewed hashes:

- `../wan-route-reuse-build-20261017/PLAN.json`: `ffa870be730c767237622d4472548bae3bdf628e6a439e99efe827c99fc3c0b2`
- `run-build.py`: `1f8671ac2758e3700cf2f83e5f1d08002debe8676310d8fafc26b5290b10bb96`
- `build-proof.py`: `6c784f971cddd7a499ebf88165f28aec07f5ca69265fdba9c3314268f4c3a340`
- `verify-build.py`: `46dfdbc58e9adaeec86c26209f3727dc5bbce37210e31714cebc580ebb5e7260`

No remaining source blocker was found. Build success and later qualification acceptance still require their own saved results; this source approval establishes neither.

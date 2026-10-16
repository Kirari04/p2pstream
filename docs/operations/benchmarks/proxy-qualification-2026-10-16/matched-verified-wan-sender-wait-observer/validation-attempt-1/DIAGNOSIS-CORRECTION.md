# Diagnosis sidecar correction

`DIAGNOSIS.md` is preserved unchanged. Its statement that
`outputs/candidate-vs-checkpoint.status` was zero is inaccurate: the first
post-attempt comparison command itself used the nonexistent relative path
`../../review/source-checkpoint-3/source`, emitted a shell `cd` error, produced
an empty comparison input, and recorded status 1. The corrected read-only
comparison uses `../review/source-checkpoint-3/source`, writes distinct `-v2`
artifacts, and records status 0. No source or original attempt output was
modified.

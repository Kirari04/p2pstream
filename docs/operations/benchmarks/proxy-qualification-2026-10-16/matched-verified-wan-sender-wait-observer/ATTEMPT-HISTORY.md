# Observer validation attempt history

| Attempt | Frozen source | Result | Preserved decisive evidence |
|---|---|---|---|
| 1 | checkpoint 3 overlay | Failed before a valid compile because source archives materialized empty roots | Original and corrected diagnoses, compiler error, status 1, comparison failure and corrected comparison |
| 2 | checkpoint 3 | Failed real compile on typed `uint32` values in an `[]int` test literal | Go JSON `build-fail`, direct/outer status 1, unchanged source inventory |
| 3 | checkpoint 4 | Focused phases pass; strict ordinary identity fails | First comparator parse failure, corrected comparator status 1, five `sendLoop` instruction differences, `Stream.write` cost 1227→1231 |
| 4 | checkpoint 7 | Baseline layout Go test passes; extractor fails on split JSON output | Raw 13-event JSONL, direct status 0, audit/outer status 1, offline concatenation diagnosis, unrun phase statuses |
| 5 | checkpoint 10 | Accepted bounded offline validation | Hash-bound attempt-4 baseline reuse, fresh candidate/proof roots, exact tests, nine extractor cases, six accepted ordinary-proof gates, pre/post source checks |

Attempt 3's first comparator source was not separately frozen. Its logs and
status are preserved, but the package does not claim that first comparator is
source-replayable. Attempt 4's stale inner checkpoint label and attempt 5's
stale resume label remain disclosed; whole-checkpoint hashes and pre/post
verification establish the actual executed checkpoints.

No attempt starts an endpoint or WAN workload.

# Recorder nullable-ID allocation screen and retained implementation

The fixed screen passed all ten predeclared cases. The retained helper is byte-identical to the measured candidate. The eight nullable IDs convert to nil/int64 before interface boxing; SQL ordinals, batch sizes, schedules, transactions, cancellation, rollups and cache touches remain unchanged.

`proposal/REPORT.md` and `proposal/SCREEN.md` are frozen pre-execution documents; their preparatory wording is historical. This index records the outcome.

## Fixed measurement outcome

Exactly ten balanced pairs (AB/BA alternating), 300 ms per case, CPU0/GOMAXPROCS1, Go1.26.6; 512 events/op, unchanged 16-row SQL groups. Every one of the 20 timed invocations exited0. All 200 samples remain bound to raw logs and line hashes. Criteria were >=64 B/event and >=4 allocations/event saved, and candidate/baseline median ns/op <=1.03 for every case.

| Case | Baseline median ns/op | Candidate median ns/op | Ratio | B saved/event | Allocs saved/event |
|---|---:|---:|---:|---:|---:|
| full flush/background | 3767894.5 | 3651395.5 | 0.969081 | 127.999023 | 8 |
| full flush/production_deadline | 3915644.0 | 3808404.0 | 0.972612 | 128.000977 | 8 |
| insert/all_large/background | 3816101.5 | 3662027.0 | 0.959625 | 128.000000 | 8 |
| insert/all_large/production_deadline | 3810664.0 | 3722996.0 | 0.976994 | 128.000000 | 8 |
| insert/all_null/background | 3525340.5 | 3380184.0 | 0.958825 | 128.000000 | 8 |
| insert/all_null/production_deadline | 3594479.5 | 3423508.0 | 0.952435 | 128.000000 | 8 |
| insert/all_small/background | 3606315.0 | 3506981.0 | 0.972456 | 128.000000 | 8 |
| insert/all_small/production_deadline | 3637685.0 | 3524404.5 | 0.968859 | 128.000000 | 8 |
| insert/ordinary_direct/background | 3589009.0 | 3384067.5 | 0.942897 | 128.000000 | 8 |
| insert/ordinary_direct/production_deadline | 3592932.5 | 3442018.0 | 0.957997 | 128.000000 | 8 |

The results establish this bounded allocation screen only. They establish no whole-application throughput improvement, HTTP/WAN result or native/HAProxy/Caddy parity.

## Preserved failures and corrections

The frozen collector exited1 after all timings completed because old flush benchmark JSON logs separated the benchmark header from its numeric line. Its source, failure traceback, raw records and ledger are retained unchanged. A separately reviewed stateful postprocessor ran once, accepted the same 200 samples, and enforced exact known case/log sets, one metric record per header, 512 events/op, raw line/hash binding, and the original criteria. No timed command was repeated. An earlier setup path failure before compilation is also retained.

The first integration invocation ran in the evidence-filled worktree and discovered ignored tmp source fragments: 128 package failures, all under p2pstream/tmp, no outside-tmp failures. It is invalid as integration evidence. The serial runner had already entered race compilation when the scope correction arrived; the owned runner and compiler were terminated, and vet never launched. Its partial race output and interruption receipt are retained. A clean tracked-source archive was then used; git apply initially skipped ignored-path inputs, caught by the sourcehash check before any test, so the two exact approved files were explicitly copied and rehashed.

## Correctness and source binding

The isolated screen correctness set passed 11 top-level tests in each of baseline, candidate, and candidate race, with persisted single/batched23-field coverage, invalid payload→NULL, generated-SQL edge/error equivalence, rollups/touches, FK rollback and cancellation. The permanent regression adds81lines to existing fixtures; it compares generated SQL database outcomes for each of8 IDs across NULL, invalid nonzero, valid zero, negative and signed extremes, using single inserts and16-row batches. It does not duplicate the helper as an oracle.

Clean integration uses tracked HEAD dc4c25df876c238204ecaa16630471a94dda264a plus the exact approved two-file patch. Normal `go test ./...`, focused recorder race and relevant vet all exited0 with no source changes. Test counts (top-level and subtests separated) are in `test-counts.json`; filtered Go JSON results omit application Output; original raw log hashes/bytes bind full local logs. No failed check was treated as a pass.

Source and executable identity are in `source-identity.json`, `screen/frozen-inputs.json`, and file inventories. The runtime helper hash is `8a267bc1ee80ac408d089fbde27db37af756ed6585a221016d1d2d22d3b3bcd2`; the permanent test hash is `d8fa5fba12361152287710f7f3797174637e8660f90c95ffb09ccd739c0b8fc3`. The screen overlay is separate from the permanent test and remains frozen.

## Artifact layout

- `screen/logs/pair-*.log`: all20 raw timing logs, byte-for-byte.
- `screen/postprocessing-reviewed/all-200-samples.json`: every sample with original raw line/hash provenance.
- `screen/postprocessing-reviewed/results.json`: medians and unchanged acceptance decisions.
- `screen/`: frozen collector, reviewed correction, overlays, commands, correctness and identity receipts. Source/overlay Go bytes use .go.txt destinations; full inventories remain local and are bound by count/hash/size summaries.
- `integration-clean/`: clean source inventory, exact composite patch, commands, filtered correctness results and outcomes, with original raw hash/size bindings.
- `integration-invalid-scope/`: invalid first invocation, partial race compile and cancellation receipts.
- `proposal/`: frozen source proof and screen design.

All original failures and raw records remain in their original ignored evidence directories. This export excludes binaries, source archives, source tree copies, full application logs and environment credentials. The 20 required raw benchmark logs contain only locally generated SQLite fixtures and temporary paths; no remote service credentials were supplied to these commands. Integration logs omit all Output content; focused screen correctness logs export only test action/result lines. `provenance.json` binds exported files to their originals; `SHA256SUMS` binds every exported artifact.

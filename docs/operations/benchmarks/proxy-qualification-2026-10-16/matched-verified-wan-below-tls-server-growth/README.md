# Matched verified WAN below-TLS and server-growth evidence

This package preserves the bounded source, contract, build, smoke, failed-run and completed sustained-run history for the diagnostic below-TLS raw-write observer and server receive-growth ledger. The completed sustained cohort **fails qualification**:

| Arm | Warmup minimum | Retained-warmup minimum | Steady minimum | Result |
| --- | ---: | ---: | ---: | --- |
| H1 | 33.747923 MB/s | **29.673138 MB/s** | 30.266590 MB/s | failed retained floor |
| H2 | 33.946013 MB/s | **27.473338 MB/s** | **27.517686 MB/s** | failed retained and steady floors |

The fixed floor is 29.7 MB/s. H1 remains a failure without rounding it up. Both arms retain runner and validator status 1 solely because the fixed performance gates failed, and the outer native tool result remains 1. The completed failed performance cohort was not rerun.

All 32 transfers completed with status 200, exact payload, verified public TLS and the requested H1/H2 protocol. All eight selected sender, raw-write and server-growth intervals pass their schema, compound identity, wall containment, byte/count/duration conservation and drop checks. Each arm's five owned endpoint and namespace processes exits naturally with status 0 and `forced=false`; terminal raw closure, resources, outer groups and the systemd unit also close naturally.

The evidence does not establish a runtime cause, a production defect or a fix. It does not clear the original current-production WAN failure, the 90% throughput floor, HAProxy parity, pressure, capacity, broader integration, release or merge gates.

## What was measured

The selected download intervals are sequences 9, 11, 14 and 15 in each arm. The unchanged timed transfer body is 7,350 bytes with SHA-256 `b86593361c76c3d2903d722b1b0292fa1bc833210d5efb2438dea1aac08ad798`. The fixed 256 MiB payload, four fresh warmups per direction, one retained warmup per direction, three steady repetitions per direction and 80 ms topology schedule are unchanged.

The ledgers keep distinct byte and timing domains:

- Yamux DATA payload and Yamux-framed plaintext submitted above TLS;
- encrypted write buffers passed by `tls.Conn` to the below-TLS raw connection;
- sender credit-decision and whole DATA-call durations; and
- server receive-side stream capacity before and after each selected transfer.

The below-TLS byte counter does not identify TLS records or independently attribute ciphertext to a DATA envelope. Neither byte domain measures TCP acknowledgement, socket delivery or NIC completion.

Every selected interval has an exact non-placeholder below-TLS connection tuple. The agent and server records use the same lane, generation and session identity with inverse connection tuples. Older closed cohorts used Yamux placeholder values for the agent session addresses and do not independently prove this physical tuple join; this package does not revise those older records.

## Result interpretation

The complete cohort associates the slower direction-cold H2 selections with a credit-window ramp and positive sender wait. Relative to the corresponding H1 rows, H2 sequences 9, 11 and 14 add approximately 643, 878 and 801 ms of selected credit wait, mostly `byte_zero`; the same-direction reused steady sequence 15 records no selected wait and passes at 34.104 MB/s. All four H2 server-growth records report zero dropped events.

That association does not decide whether the credit state causes the throughput interval or is a consequence of work progressing during the interval. The observer has no peer window-update publication/wakeup timeline. The reviewed architecture report therefore finds no demonstrated source invariant violation, no concrete production defect and no justified constant or policy change.

The package also keeps the original and corrected read-only public copy/credit feedback reports. The revision corrects the aggregate effective maximum and withdraws an ordering-only local test. It describes a synchronous feedback path in which downstream public writes delay the next tunnel read and hence the next opportunity to replenish receive credit, while explicitly leaving initiation and causality unresolved. A later peer-wake discriminator is source-only proposal material; no new diagnostic was executed.

## Source and proof binding

The diagnostic uses production runtime base `8ac4c8df8237e37777ba2fac0da6f949da17bdea` and adds build-tagged test-only observation. Repository head `913dfc4` adds documentation/evidence only and is production-source equivalent to that base; the current-source binding and checkpoint manifests preserve the exact relationship. The selected checkpoints preserve every reviewed correction and rejection through final integration checkpoint 10, the accepted file-backed capacity serializer, the audited build and the locally published binary hashes. Binary bytes, reconstructed full source trees and build caches are omitted; their exact hashes and build metadata remain in the manifests.

The strict ordinary-build proof remains rejected. Two early attempts failed in validation fixtures, the corrected strict comparison rejected code/compiler/compiler-data/escape equality, and the function-isolation attempt retained a false code gate caused by compiler-generated relocation symbol renumbering. A later supplemental analyzer proved only the exact six generated relocation-target bijections and eight affected sites, with equal target bytes, flags, sizes and incoming graphs. That narrow result does not replace or weaken the original strict failures.

## Preserved run history

Tiny attempts 1 and 2 failed before endpoint traffic because of an executable-mode guard and a restricted `PATH` missing `tc`; both remain unchanged. Tiny attempt 3 completed both H1/H2 arms with exact correctness, raw/server-growth closure and natural cleanup, while keeping performance non-gating.

The first sustained attempt is preserved as **invalid and partial**. H1 produced nine rows, then the old `jq --argjson` capacity serializer exceeded the process argument limit. All five inner processes required TERM cleanup with status 143; the summary, terminal raw closure and H2 arm are absent. Natural outer scope joins do not turn that run into a valid cohort.

A bounded offline contract then validated the file-backed serializer with five cases, including a 177,178-byte capacity response and 778,043-byte combined record, exact small-record equivalence, and malformed/missing rejection. The completed sustained attempt used that source and produced the rejected result at the top of this document.

`ATTEMPT-HISTORY.md` summarizes the history. Original source, status, stderr, lifecycle, manifests and failed outputs remain selected; summaries never replace them.

## Package structure

`PACKAGE-MAP.tsv` maps every selected source artifact to one destination. Short primary run records, reports, ledgers and reviews remain readable as loose files. Repeated source checkpoints, contracts, proof logs and ancillary records are stored once in `ancillary-below-tls-server-growth-evidence.tar.gz`. The archive preserves selected content bytes while using deterministic member order and metadata. Package modes are explicit in `PACKAGE-MAP.tsv`; where original runtime modes matter, the original provenance records them.

`ALLOWLIST.tsv` is the explicit selection. `EXCLUSIONS.tsv` records omitted categories and their binding. `ANCILLARY-BUNDLE.sha256` binds the archive, `SHA256SUMS` binds every package file, and `verify-evidence-package.py` verifies every archive and loose entry without extraction.

The builder needs the ignored source corpus through an explicit `--source-root`. Its inclusion is historical build documentation, not a promise that the package can reconstruct absent tmp evidence from a normal checkout.

## Security limits

TLS private keys, readiness files, authentication values, endpoint fixture logs/state, temporary runtime directories, binaries, compiled archives, caches and full reconstructed source trees are excluded. The complete run ledgers and freeze manifests bind omitted files by path, size and hash.

A nonprinting scan recovered private-key bytes and authentication-like values only from excluded closed-run files, then compared exact values against all selected candidates. It found zero selected hit artifacts and printed no values. The process-local trace bearer was not persisted, so exact comparison against it is unavailable. This exact-value scan does not prove absence of every transformed or unknown secret representation.

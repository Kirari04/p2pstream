# Final grown-pool pressure readiness

Status: **the single authorized final clean build and original grown-pool pressure cohort passed** on `27c8e31f3826ab5326d941b20644c1f56159d99d`. Root selected a pressure-only frozen derivative; the tracked general growth runner is unchanged. See `REPORT.md`, `independent-result.json` and `publishable-artifacts.json` for final outcomes and exact evidence. No retry or source changes occurred during the full attempt.

Source review base: `27c8e31f3826ab5326d941b20644c1f56159d99d`. Tracked growth runner SHA256: `a493280e839efc1b38325481c94d7a453415a3f5dc68a9777d727703b6b2477a`.

## Exact reviewed predecessor and preserved topology

The predecessor is `tmp/continuation-20261016/current-wan-prep/runner/qualify-proxy-agent-growth-owned.sh`, SHA256 `b29242b6891105fd6231fecd7739ae48e6909049d0583a184765839ad2827c5f`. It is byte-identical to the tracked `current-production-wan/runner/qualify-proxy-agent-growth-owned.sh.txt` snapshot and its export-map hash. That WAN derivative explicitly rejects pressure; its old WAN evidence does not establish pressure behavior.

The pressure-only derivative retains original user/net namespace entry and host PID topology. It does not import the WAN supervisor's private PID/mount namespace. Actual attempt 2 below verified that this topology supports the exact `setsid systemd-run --user --scope ... nsenter ... taskset` structure with finite substituted Python payloads.

## Frozen proposal files

- `proposal/scripts/qualify-proxy-agent-growth-pressure-owned.sh`: pressure-only derivative with natural process/group/namespace joins, namespace identity checks, bounded forced/emergency paths, cancellation-load ownership, raw identity and atomic final status.
- `proposal/scripts/qualify-proxy-pressure-checkpoint.sh`: clean archive builds, immutable snapshots/manifests, one fixed original pressure attempt, outer supervisor/group lifecycle and failure retention.
- `proposal/scripts/validate-owned-pressure.py`: exact original workload/schema gates, natural lifecycle and input binding.
- `proposal/scripts/contract-pressure-scopes.sh`: the separately reviewed finite scope/cgroup contract used by actual attempt 2.

`pressure-additions-vs-reviewed-owned.patch` and `pressure-owned-vs-tracked.patch` show the initial derivative. `followup-changes.patch` is the compact wrapper/validator follow-up diff; `followup-diff-base.json` proves its reconstructed preimages match the previous frozen SHA manifest exactly. `scope-extractor-fix.patch` is the separately approved one-line contract extraction correction. These comparisons do not authorize changing the tracked general growth runner.

`OFFLINE-FROZEN-SOURCE-v2.sha256` binds the source used for actual attempt 2. Final review source is now `OFFLINE-FROZEN-SOURCE-v3.sha256`: only the unexecuted checkpoint adds per-build deadlines and records them in its manifest. Its exact delta is `v3-per-build-timeouts.patch`; the pressure runner, validator and actual scope contract remain unchanged from v2. The original `OFFLINE-FROZEN-SOURCE.sha256` and failed attempt 1 source remain separate.

## Workload and input guarantees

Static source comparison in `preserved-workload-source.json` shows `run_transfer`, warmup/pressure dispatch, the entire `run_pressure_qualification`, and the canonical summary byte-identical to tracked source. The cancellation trace gains only an authoritative owner-group join after the existing asynchronous load.

The checkpoint fixes the original flags: `--pressure --warmup-mib 256 --steady-mib 256 --transfer-repetitions 1 --required-pool-mib 80 --rtt-ms 80`. Original gates remain four 256 MiB transfers in each direction, retained transfers in each direction, every receiving pool strictly above 80 MiB, actual critical-memory pressure with at least 256 MiB hard-limit margin, distinct 4 GiB/no-swap cgroups and zero OOM, recorded public 503 with ordinary admission closed, cold reserved-control progress without ordinary rejection consumption, origin-observed 32 MiB cancellation and complete refunds, and exact 16 MiB recovery in both directions with retained pools. No observer variant or measurement instrumentation was added.

The builder uses clean current/legacy `git archive` sources and only the canonical historical-agent fixture overlay, with an explicit pinned Go executable, exact commands/environment and ordinary Go content-addressed cache. It avoids a redundant private compiler cache. The manifest binds archives, overlay, all three actual binaries, runner, validator, checkpoint, Go settings and actual Go toolchain. The supervisor captures the manifest digest before launch. The final validator requires that captured digest and independently rehashes every exact bound actual input plus the actual Go executable; editable manifest strings alone cannot satisfy it. Inputs are checked before and after execution.

The inner emergency cleanup has shared five-second TERM and five-second KILL polls. The wrapper gives it fifteen seconds before escalation, and the independent overall timeout gives it twenty seconds. An offline cross-script contract checks these bounds. Any forced/unjoined owner invalidates qualification.

## Executed offline validation

```sh
python3 tmp/pressure-final-readiness-20261017/offline-contracts.py -v
bash tmp/pressure-final-readiness-20261017/offline-lifecycle-contracts.sh
bash -n tmp/pressure-final-readiness-20261017/proposal/scripts/qualify-proxy-pressure-checkpoint.sh \
  tmp/pressure-final-readiness-20261017/proposal/scripts/qualify-proxy-agent-growth-pressure-owned.sh \
  tmp/pressure-final-readiness-20261017/proposal/scripts/contract-pressure-scopes.sh
python3 -m py_compile tmp/pressure-final-readiness-20261017/proposal/scripts/validate-owned-pressure.py
```

Ten unittest groups passed. Forty-six individual positive/negative schema/input cases use the preserved original **76** grown-pool-pressure rows, with their actual twelve-transfer order, payload sizes and field names. Historical input SHA256: `8a60527dd243dfe4b054f744064e19044535ed0ec07ce34a95eeb5f429c1d11e`.

The fixture keeps historical measurements unchanged. Its explicit `FIXTURE-ONLY-SUBSTITUTIONS.json` records all local inert-input provenance substitutions and newly explicit formerly-omitted zero drop/queue/drain-error fields. Namespace/lifecycle/manifest inputs in this offline fixture are explicitly synthetic. They are not build or runtime evidence. Replayable successful fixture: `contracts/preserved-original-fixture/`; expected manifest digest: its `expected-manifest.sha256`.

Negatives cover missing/duplicate required stages and transfers, ordering/payload drift, empty/nonboolean acceptance, all ten bound actual-input changes, changed actual Go executable, row source/hash mismatches, pressure-sample provenance mismatch, edited manifest plus edited row strings, strict telemetry and response classes, and missing/duplicate/forced lifecycle exits. `contracts/*.json` retain case statuses and diagnostics. Source-extracted Bash children also proved natural statuses 0/7 and rejection of a forced TERM handler returning zero; only fixture polling limits were shortened. Logs: `offline-contracts.log`, `offline-lifecycle-contracts.log`.

The new extraction contract assembles all eight actual helper functions using the contract's own extractor, then parses the complete module. It reproduces the old extractor's exit 2 and verifies the corrected module exits 0. Receipt: `contracts/scope-extracted-syntax.json`; module: `contracts/scope-extracted-functions.sh`.

## Actual scope/cgroup attempts and cleanup

Each attempt had a separate explicit bounded runtime grant and directory. Neither was an automatic retry. Both were pinned to CPUs 12–13 and wrapped with a ninety-second deadline plus thirty-five-second kill grace. No Go/proxy/pressure workload ran.

**Attempt 1 — failed, exit 2.** Original user/net entry succeeded and retained the host PID namespace. Before launching any holder or scope, the extractor's `/^}/` prefix match truncated the embedded Python dictionary in `cgroup_snapshot`; `eval` failed while parsing its heredoc. The old contract source, source hashes, topology, invocation, stdout/stderr and outer exit status are retained in `runtime-scope-attempt-1*`. No owner process group or systemd scope had been created. The exact closing-line correction was approved, and its old/new behavior was reproduced offline before a new runtime grant.

**Attempt 2 — passed, exit 0.** The corrected v2 contract launched four uniquely named actual systemd user scopes across expected agent-exit-0 and agent-exit-7 cases. Actual payloads occupied distinct readable 4 GiB/no-swap cgroups with zero OOM, server/agent network namespaces differed, and both retained the host PID namespace. Both expected agent statuses propagated naturally; all server and namespace-holder owners joined with zero, and no forced cleanup occurred.

Evidence: `runtime-scope-attempt-2/{source.sha256,topology.txt,lifecycle.jsonl,exit-*/contract-result.json,exit-*/*-cgroup.json,results.status.json}` plus outer invocation/logs/exit status. The post-run owned-resource audit found all six payload PIDs and all six process groups absent; all four scope units had `LoadState=not-found`, `ActiveState=inactive`, and empty control groups. No additional unit reset or cleanup was needed. Audit: `runtime-scope-attempt-2/post-run-owned-resource-audit.json`. CPU ownership was released immediately after this audit.

## Final full-cohort outcome

Root separately reviewed and granted the exact v3 launch in `LAUNCH-27c8e31.sh`. Its clean archive builds and one original pressure cohort completed with all outer/checkpoint/inner statuses zero; the final validator accepted all thirteen literal-true pressure gates, twelve transfers, strict telemetry and six natural owner exits. Actual critical memory was 3,887,235,072 of 4,294,967,296 bytes with 407,732,224-byte margin; server and agent retained all four pools strictly above 80 MiB through exact 16 MiB recovery. Final PID/group/scope audit passed, and runtime ownership was released.

`REPORT.md` is the concise result; `independent-result.json` independently rehashes every bound actual input and records exact 503/control/cancellation/refund/recovery and cleanup receipts. No private readiness/key files or duplicate source/binary bundle were copied. The archive/source/binary storage estimate and bound timings in `LAUNCH-PLAN.md` remain planning history; no additional pressure attempt is authorized or needed for this same accepted input.

This final-runtime grown-pool pressure gate passed. Historical `f2213cf14c158c62c158f2be3d270354d38a7639` evidence remains separate. WAN throughput, standing, capacity, race, release and deployment gates remain separate qualifications.

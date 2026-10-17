# Non-WAN qualification harness fixes — validation

Base commit: `c09c1d5bbe8ad4406f7205755a59d8eb6a3fedee`. Changes remain uncommitted for the coordinating agent's review. No commit or push was performed by this worker.

## Changes

- `qualify-proxy-performance.sh` propagates natural child nonzero exits, rejects any forced stop even if the child's TERM handler exits zero, bounds TERM/KILL joins, and verifies the real retained Docker container state before removing it. Docker client exit is not substituted for container state. Completed children/containers are deregistered. HAProxy receives graceful SIGUSR1 and must join with zero status.
- Every stop writes a small atomic `*.stop.exit.json` with PID, container name, forced-cleanup flag, shell status, inspected container state when applicable, and the natural-exit verification result. Final raw `status.json` preserves the runner status and records emergency cleanup. Nominal success with remaining owned children/containers is invalidated.
- Standing acceptance requires the exact nineteen producer keys with literal boolean true; exact scenario, cohort label, agent and lane counts; both process/tunnel cardinalities; and exactly one indexed health roundtrip per expected agent. Parse/measurement failures produce rejected summaries. A nonzero delegated runner rejects an otherwise accepted row.
- Capacity telemetry requires explicit integer zero drop/queue/OOM counters, an explicit empty drain-error string, and exact successful response-class totals. Strict Python integer checks reject booleans/floats. Six JSON tags in `internal/server/proxy_qualification_test.go` emit zero-valued counters/errors explicitly, including `persisted_event_delta` for zero-event boundary diagnostics. Existing explicit pre-HTTP diagnostic classification remains scoped to diagnostics; it does not relax ordinary capacity acceptance.
- Capacity and standing allocate and index raw directories before launch, retain separate stdout/stderr logs, append started/finished attempt records with exit status, and atomically write final statuses. Reused nonempty output/raw directories are rejected.

## Commands and observed results

```sh
python3 scripts/test-qualification-harness.py -v
bash -n scripts/qualify-proxy-performance.sh scripts/qualify-proxy-agent-standing.sh scripts/qualify-proxy-capacity-checkpoint.sh
git diff --check
```

All sixteen offline contract tests passed. They extract the shipped Bash/Python/jq gates and cover lifecycle exit 0/7, forced-stop invalidation, mocked Docker state rejection, successful/failed per-stop records, emergency-cleanup invalidation, one owned bounded container batch and batch-failure invalidation, failed capacity attempt preservation, exact standing schema/cardinality, malformed JSON/measurement rejection, strict telemetry, exact response totals, and continued explicit boundary diagnostic behavior. Syntax and whitespace checks passed. Logs: `offline-contracts.log`.

After the coordinator released a workload window, this finite probe ran only on CPU 12:

```sh
timeout -k 2 50 taskset -c 12 bash tmp/qualification-harness-fix-20261017/lifecycle-contract.sh
```

The probe passed actual host child exits 0 and 7; the production unshortened ten-second forced-stop path with a TERM handler exiting 0 was rejected as status 1; actual cached Debian container exits 0 and 7 were observed and containers removed; HAProxy 3.2.9 graceful SIGUSR1 exited zero. No tracked fixture children/containers remained. Evidence: `lifecycle-contract-with-forced-stop.log` and `actual-lifecycle.FLmchL/*.stop.exit.json`. Docker fixtures used 64 MiB/no-swap cgroups, network disabled, and the already cached `debian:trixie-slim` image. They did not run the proxy application or load traffic.

A separate actual two-container emergency-cleanup probe also passed on CPU 12:

```sh
timeout -k 2 25 taskset -c 12 bash tmp/qualification-harness-fix-20261017/batch-cleanup-contract.sh
```

Its deliberate original exit 7 remained 7; final `status.json` recorded `emergency_cleanup: true`; the one bounded owned-container removal batch removed both actual containers; subsequent Docker inspect returned `No such object` for both names. Evidence: `batch-cleanup-contract.log` and the referenced `actual-batch-cleanup.*` raw directory.

The first actual probe failed because its private Unix socket pathname exceeded HAProxy's length limit. Its logs and raw directory remain as `lifecycle-contract-failed-long-socket.log` and `actual-lifecycle.pJFB8W/`. A second probe used a short owned temporary socket directory and passed; a third added the actual unshortened forced-stop check and passed. The temporary socket directories were removed. These attempts are retained separately.

## Limits

No Go compilation/test, full server/agent test-process qualification, standing 16/32-agent cohort, capacity 1,536/512-request qualification, throughput, WAN, namespace, race, or production endpoint workload was run by this worker. The actual lifecycle fixture exercises the shipped lifecycle helpers with ordinary host/Docker fixtures and HAProxy; it does not establish application shutdown behavior or performance. Emergency Docker removal now uses one ten-second batch with a one-second kill grace, followed by shared two-second TERM/KILL join windows; this fits within the standing wrapper's twenty-second signal grace. Full final-commit qualification remains pending.

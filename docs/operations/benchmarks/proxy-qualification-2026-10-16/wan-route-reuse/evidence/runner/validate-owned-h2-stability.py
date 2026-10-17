#!/usr/bin/env python3
import hashlib
import json
import math
import pathlib
import statistics
import sys


def fail(message: str) -> None:
    raise SystemExit(message)


if len(sys.argv) != 7:
    fail("usage: validate-owned-h2-stability.py RESULTS LIFECYCLE ATTESTATION OUTER_NAMESPACES SUPERVISOR_LIFECYCLE BUILD_MANIFEST")

results_path, lifecycle_path, attestation_path, outer_path, supervisor_path, manifest_path = map(pathlib.Path, sys.argv[1:])
rows = [json.loads(line) for line in results_path.read_text().splitlines() if line.strip()]
lifecycle = [json.loads(line) for line in lifecycle_path.read_text().splitlines() if line.strip()]
outer = json.loads(outer_path.read_text())
supervisor = json.loads(supervisor_path.read_text())
manifest = json.loads(manifest_path.read_text())

pair_orders = (("legacy", "aggregate-v1"), ("aggregate-v1", "legacy"), ("legacy", "aggregate-v1"))
expected = []
expected_pairs = []
for pair, modes in enumerate(pair_orders, 1):
    for mode in modes:
        cohort = []
        for direction in ("download", "upload"):
            cohort.extend((mode, "warmup", direction, repetition) for repetition in range(1, 5))
        cohort.extend(((mode, "retained-warmup", "download", 1), (mode, "retained-warmup", "upload", 1)))
        cohort.extend(((mode, "steady", "download", 1), (mode, "steady", "upload", 1),
                       (mode, "steady", "upload", 2), (mode, "steady", "download", 2),
                       (mode, "steady", "download", 3), (mode, "steady", "upload", 3)))
        expected.extend(cohort)
        expected_pairs.extend([pair] * len(cohort))

actual = [(row.get("transport_mode"), row.get("measurement_stage"), row.get("transfer_direction"), row.get("repetition")) for row in rows]
if actual != expected:
    fail(f"result order/shape changed: {actual!r}")

hashes = manifest["sha256"]
for index, row in enumerate(rows, 1):
    if row.get("observed_http_major") != 2:
        fail(f"row {index} observed_http_major={row.get('observed_http_major')!r}, want 2")
    if row.get("protocol") != "h2" or row.get("status_codes") != {"200": 1}:
        fail(f"row {index} requested protocol or exact status changed")
    direction = row["transfer_direction"]
    expected_response = 268435456 if direction == "download" else 32
    expected_rate = row.get("configured_payload_bytes", 0) / row.get("wall_seconds", 0) if row.get("wall_seconds", 0) > 0 else 0
    if not (row.get("attempted") == row.get("useful_completed") == 1 and row.get("errors") == 0 and
            row.get("status_classes") == {"2xx": 1} and row.get("configured_payload_bytes") == 268435456 and
            row.get("response_bytes") == expected_response and row.get("event_stats_captured") is True and
            row.get("persisted_event_delta") == 1 and (row.get("dropped_event_delta") or 0) == 0 and
            (row.get("event_queue_after_drain") or 0) == 0 and (row.get("event_drain_error") or "") == "" and
            math.isfinite(row.get("useful_transfer_bytes_per_second", 0)) and row.get("useful_transfer_bytes_per_second", 0) > 0 and
            math.isclose(row["useful_transfer_bytes_per_second"], expected_rate, rel_tol=1e-12, abs_tol=1e-9)):
        fail(f"row {index} failed exact payload/status/event conservation")
    if row.get("configured_rtt_ms") != 80:
        fail(f"row {index} configured_rtt_ms changed")
    if row.get("provenance", {}).get("server_source_ref") != manifest["source"]["current_ref"]:
        fail(f"row {index} server source provenance changed")
    if row.get("provenance", {}).get("aggregate_agent_source_ref") != manifest["source"]["current_ref"]:
        fail(f"row {index} aggregate source provenance changed")
    if row.get("provenance", {}).get("legacy_agent_source_ref") != manifest["source"]["legacy_ref"]:
        fail(f"row {index} legacy source provenance changed")
    provenance = row["provenance"]
    if provenance.get("server_binary_sha256") != hashes["current_server_binary"] or provenance.get("aggregate_agent_binary_sha256") != hashes["current_agent_binary"] or provenance.get("legacy_agent_binary_sha256") != hashes["legacy_agent_binary"] or provenance.get("harness_sha256") != hashes["owned_runner"]:
        fail(f"row {index} binary or runner hash identity changed")

expected_labels = {"origin", "agent-namespace", "router-namespace"}
for pair in range(1, 4):
    for label in ("current-server-legacy-agent-compat", "current-server-aggregate-v1"):
        expected_labels.update((f"{label}-{pair}-agent", f"{label}-{pair}-server"))
natural = {
    row.get("label")
    for row in lifecycle
    if row.get("event") == "process-exit"
    and row.get("forced") is False
    and row.get("status") == 0
    and row.get("group_joined") is True
    and row.get("invalidates_success") is False
}
process_exits = [row for row in lifecycle if row.get("event") == "process-exit"]
if len(process_exits) != 15 or natural != expected_labels or any(row.get("invalidates_success") is True for row in lifecycle):
    fail(f"lifecycle was not wholly natural: {lifecycle!r}")
namespace_start = [row for row in lifecycle if row.get("event") == "namespace-helpers-start"]
if len(namespace_start) != 1:
    fail("missing unique namespace helper identity row")
helper = namespace_start[0]
if helper.get("agent", {}).get("namespace") == helper.get("router", {}).get("namespace"):
    fail("agent and router namespace identities are equal")
for role in ("agent", "router"):
    if not isinstance(helper.get(role, {}).get("start_ticks"), int) or helper[role]["start_ticks"] <= 0:
        fail(f"invalid {role} namespace helper start ticks")
if outer.get("all_private") is not True or outer.get("shell_pid_is_one") is not True or outer.get("python_parent_is_one") is not True:
    fail(f"outer user/PID/mount/network namespace ownership missing: {outer!r}")
if supervisor.get("runner_status") not in (0, 1) or supervisor.get("group_joined") is not True or supervisor.get("forced") is not False:
    fail(f"outer runner process group did not join naturally: {supervisor!r}")

pair_gates = []
failed = []
for pair in range(1, 4):
    pair_rows = [row for row, row_pair in zip(rows, expected_pairs) if row_pair == pair and row["measurement_stage"] == "steady"]
    gate = {"pair": pair, "order": list(pair_orders[pair - 1]), "minimum_ratio": 0.90}
    for direction in ("download", "upload"):
        medians = {}
        for mode in ("legacy", "aggregate-v1"):
            samples = [row["useful_transfer_bytes_per_second"] for row in pair_rows if row["transport_mode"] == mode and row["transfer_direction"] == direction]
            if len(samples) != 3:
                fail(f"pair {pair} {mode} {direction} sample count={len(samples)}, want 3")
            medians[mode] = statistics.median(samples)
        ratio = medians["aggregate-v1"] / medians["legacy"]
        passed = ratio >= .90
        gate[direction] = {"legacy_median_bytes_per_second": medians["legacy"], "aggregate_median_bytes_per_second": medians["aggregate-v1"], "ratio": ratio, "pass": passed}
        if not passed:
            failed.append(f"pair {pair} {direction} ratio {ratio:.6f}")
    pair_gates.append(gate)
result_hash = hashlib.sha256(results_path.read_bytes()).hexdigest()
lifecycle_hash = hashlib.sha256(lifecycle_path.read_bytes()).hexdigest()
accepted = supervisor["runner_status"] == 0 and not failed
attestation = {
    "schema_version": 1,
    "accepted": accepted,
    "validation": "three fixed original H2 alternating legacy/aggregate pairs with every-pair gates",
    "original_runner_status": supervisor["runner_status"],
    "exact_complete_timing_cohort": True,
    "row_count": len(rows),
    "all_observed_http_major": 2,
    "natural_endpoint_and_namespace_statuses": True,
    "outer_private_user_pid_mount_net": True,
    "outer_runner_group_joined": True,
    "pair_gates": pair_gates,
    "results_sha256": result_hash,
    "lifecycle_sha256": lifecycle_hash,
}
attestation_path.write_text(json.dumps(attestation, indent=2, sort_keys=True) + "\n")
if not accepted:
    reasons = failed or [f"original runner status {supervisor['runner_status']}"]
    fail("timing acceptance failed after exact complete cohort: " + "; ".join(reasons))

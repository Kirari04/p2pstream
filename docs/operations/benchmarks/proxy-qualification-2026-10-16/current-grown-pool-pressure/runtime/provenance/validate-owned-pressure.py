#!/usr/bin/env python3
"""SOURCE PROPOSAL ONLY: bind the original pressure result to owned inputs/exits."""
import collections
import hashlib
import json
from pathlib import Path
import sys

if len(sys.argv) != 3:
    raise SystemExit("usage: validate-owned-pressure.py OUTPUT_ROOT EXPECTED_MANIFEST_SHA256")
root = Path(sys.argv[1])
attempt = root / "attempt-1"
manifest = json.loads((root / "provenance/build-manifest.json").read_text())
summary = json.loads((attempt / "results.summary.json").read_text())
rows = [json.loads(line) for line in (attempt / "results.jsonl").read_text().splitlines() if line.strip()]
lifecycle = [json.loads(line) for line in (attempt / "results.lifecycle.jsonl").read_text().splitlines() if line.strip()]
inner_status = json.loads((attempt / "results.status.json").read_text())
supervisor = json.loads((attempt / "supervisor-lifecycle.json").read_text())


def require(condition, message):
    if not condition:
        raise SystemExit(message)


def one(stage):
    selected = [row for row in rows if row.get("measurement_stage") == stage]
    require(len(selected) == 1, f"missing unique {stage} row")
    return selected[0]


def strict_zero(value):
    return type(value) is int and value == 0


def exact_counts(value, expected):
    return isinstance(value, dict) and value == expected and all(type(count) is int for count in value.values())


def events(row):
    return (row.get("event_stats_captured") is True and type(row.get("persisted_event_delta")) is int
            and row["persisted_event_delta"] == 1 and strict_zero(row.get("dropped_event_delta"))
            and strict_zero(row.get("event_queue_after_drain")) and row.get("event_drain_error") == "")


def digest(path):
    result = hashlib.sha256()
    with path.open("rb") as source:
        for data in iter(lambda: source.read(1 << 20), b""):
            result.update(data)
    return result.hexdigest()


# The expected digest is captured by the supervisor before launch, not read
# from the editable manifest. Every named input is independently rehashed.
require(digest(root / "provenance/build-manifest.json") == sys.argv[2], "build manifest changed after launch")
required_inputs = {
    "archives/current.tar", "archives/legacy.tar", "bin/current-server.test", "bin/current-agent.test", "bin/legacy-agent.test",
    "sources/current/internal/agent/testdata/proxy_qualification_external_legacy_test.go",
    "provenance/qualify-proxy-agent-growth-pressure-owned.sh", "provenance/validate-owned-pressure.py",
    "provenance/qualify-proxy-pressure-checkpoint.sh", "provenance/go-build-settings.json",
}
require(isinstance(manifest.get("sha256"), dict) and set(manifest["sha256"]) == required_inputs,
        "bound manifest input schema changed")
for name, expected_hash in manifest["sha256"].items():
    require(digest(root / name) == expected_hash, f"bound input hash mismatch: {name}")
require(digest(Path(manifest["go_binary"])) == manifest["go_binary_sha256"], "pinned Go binary hash mismatch")


expected_acceptance = {
    "pressure_reached", "admission_rejected_under_pressure", "functional_recovery", "control_progress_during_pressure",
    "ordinary_admission_closed_during_pressure", "control_reservation_owned", "control_slot_returned_to_baseline",
    "control_did_not_consume_ordinary_pressure_rejection", "canceled_upload_refunds_quiescent", "canceled_upload_reached_origin",
    "all_receiving_pools_exceeded_required_size_before_pressure", "all_receiving_pools_retained_after_recovery", "no_oom",
}
acceptance = summary.get("acceptance")
require(isinstance(acceptance, dict) and set(acceptance) == expected_acceptance
        and all(value is True for value in acceptance.values()), "pressure acceptance schema/boolean failure")
require(strict_zero(inner_status.get("exit_status")) and inner_status.get("emergency_cleanup") is False, "inner runner did not finish naturally")
require(strict_zero(supervisor.get("runner_status")) and supervisor.get("group_joined") is True
        and supervisor.get("forced") is False, "outer supervisor did not join naturally")

expected_labels = {"current-server-aggregate-v1-1-agent", "current-server-aggregate-v1-1-server",
                   "origin", "agent-namespace", "router-namespace", "canceled-upload-load"}
exits = [row for row in lifecycle if row.get("event") == "process-exit"]
require(collections.Counter(row.get("label") for row in exits) == collections.Counter(expected_labels), "missing/duplicate process owner exits")
require(all(row.get("forced") is False and row.get("group_joined") is True
            and strict_zero(row.get("status")) and row.get("invalidates_success") is False for row in exits), "nonnatural process owner exit")
require(not any(row.get("invalidates_success") is True for row in lifecycle), "lifecycle cleanup invalidated attempt")
identities = [row for row in lifecycle if row.get("event") == "namespace-helpers-start"]
require(len(identities) == 1, "missing unique namespace helper identity")
identity = identities[0]
require(len({identity["runner_namespace"], identity["agent"]["namespace"], identity["router"]["namespace"]}) == 3,
        "runner/agent/router network namespaces are not distinct")
for role in ("agent", "router"):
    require(type(identity[role].get("start_ticks")) is int and identity[role]["start_ticks"] > 0,
            f"invalid {role} helper process-instance identity")

expected = [("warmup", direction, repetition, 256 << 20)
            for direction in ("download", "upload") for repetition in range(1, 5)]
expected += [("retained-warmup", direction, 1, 256 << 20) for direction in ("download", "upload")]
expected += [("pressure-recovery", direction, 1, 16 << 20) for direction in ("download", "upload")]
transfers = [row for row in rows if row.get("measurement_stage") in {"warmup", "retained-warmup", "steady", "pressure-recovery"}]
actual = [(row["measurement_stage"], row.get("transfer_direction"), row.get("repetition"), row.get("configured_payload_bytes")) for row in transfers]
require(actual == expected, "original four warmups/retained/recovery order or payloads changed")
for row in transfers:
    require(row.get("transport_mode") == "aggregate-v1" and row.get("protocol") == "h2"
            and row.get("observed_http_major") == 2 and type(row.get("attempted")) is int
            and type(row.get("useful_completed")) is int and row.get("attempted") == row.get("useful_completed") == 1
            and type(row.get("configured_payload_bytes")) is int and strict_zero(row.get("errors"))
            and exact_counts(row.get("status_codes"), {"200": 1}) and exact_counts(row.get("status_classes"), {"2xx": 1}) and events(row), "transfer status/event gate failed")
    require(row.get("response_bytes") == (row["configured_payload_bytes"] if row["transfer_direction"] == "download" else 32), "transfer payload bytes changed")
    require(row.get("configured_rtt_ms") == 80 and row.get("shaped_rate_bits_per_second") == 1000000000, "original shaped path changed")
    provenance = row.get("provenance", {})
    for key in ("server_source_ref", "aggregate_agent_source_ref"):
        require(provenance.get(key) == manifest["current_ref"], f"{key} input mismatch")
    require(provenance.get("legacy_agent_source_ref") == manifest["legacy_ref"], "legacy input mismatch")
    for key, name in (("server_binary_sha256", "bin/current-server.test"), ("aggregate_agent_binary_sha256", "bin/current-agent.test"),
                      ("legacy_agent_binary_sha256", "bin/legacy-agent.test"), ("harness_sha256", "provenance/qualify-proxy-agent-growth-pressure-owned.sh")):
        require(provenance.get(key) == manifest["sha256"][name], f"{key} hash mismatch")

# Pressure/cgroup samples carry a smaller provenance object than transfers;
# every provenance field actually emitted by the canonical runner is bound.
for row in rows:
    provenance = row.get("provenance")
    if not isinstance(provenance, dict):
        continue
    expected = {"server_source_ref": manifest["current_ref"], "aggregate_agent_source_ref": manifest["current_ref"],
                "legacy_agent_source_ref": manifest["legacy_ref"],
                "server_binary_sha256": manifest["sha256"]["bin/current-server.test"],
                "aggregate_agent_binary_sha256": manifest["sha256"]["bin/current-agent.test"],
                "legacy_agent_binary_sha256": manifest["sha256"]["bin/legacy-agent.test"],
                "harness_sha256": manifest["sha256"]["provenance/qualify-proxy-agent-growth-pressure-owned.sh"]}
    require(all(key in expected and value == expected[key] for key, value in provenance.items()),
            "pressure sample provenance mismatch")

critical = one("pressure-critical")
for stage in ("pressure-baseline", "pressure-critical", "pressure-released", "pressure-recovered"):
    sample = one(stage)
    for endpoint in ("server", "agent"):
        cgroup = sample[endpoint + "_cgroup"]
        require(cgroup["memory_max_bytes"] == 4 << 30 and strict_zero(cgroup["memory_swap_max_bytes"])
                and strict_zero(cgroup["memory_events"]["oom"]) and strict_zero(cgroup["memory_events"]["oom_kill"]), "cgroup envelope/OOM gate failed")
    require(sample["server_cgroup"]["cgroup_path"] != sample["agent_cgroup"]["cgroup_path"], "endpoint cgroups are shared")
current, maximum = critical["agent_cgroup"]["memory_current_bytes"], critical["agent_cgroup"]["memory_max_bytes"]
require(current * 100 >= maximum * 90 and maximum - current >= 256 << 20, "critical actual-memory margin gate failed")
require(critical["agent_runtime"]["agent_capacity"]["pressure"] == "critical"
        and critical["agent_runtime"]["agent_capacity"]["pressure_reason"] == "memory", "critical memory state missing")
rejected = one("pressure-rejection")
require(exact_counts(rejected.get("status_codes"), {"503": 1}) and exact_counts(rejected.get("status_classes"), {"5xx": 1})
        and rejected.get("attempted") == 1 and rejected.get("useful_completed") == 0 and events(rejected), "recorded public503 missing")
control = one("pressure-control-roundtrip")
require(control["control_roundtrip"].get("ok") is True and control["control_roundtrip"].get("http_status") == 200,
        "cold reserved-control request failed")
canceled = one("canceled-upload")
require(canceled.get("configured_payload_bytes") == 32 << 20 and canceled.get("observed_http_major") == 2
        and canceled.get("origin_receiving_payload_observed") is True and canceled.get("agent_lane_refunds_quiescent") is True
        and events(canceled), "original cancellation/refund gate failed")
one("pressure-result")
require(summary["receiving_pools"]["required_strictly_greater_than_bytes"] == 80 << 20, "required growth threshold changed")
for stage in ("pre_pressure", "post_recovery"):
    for endpoint in ("server", "agent"):
        pools = summary["receiving_pools"][stage][endpoint + "_bytes"]
        require(len(pools) == 4 and all(type(value) is int and value > 80 << 20 for value in pools), "receiving pools did not all retain growth")
attestation = {"schema_version": 1, "accepted": True, "validation": "original grown-pool pressure with natural owned joins",
               "current_ref": manifest["current_ref"], "legacy_ref": manifest["legacy_ref"], "transfer_rows": len(transfers),
               "natural_owner_exit_count": len(exits), "private_pid_namespace_claimed": False,
               "results_sha256": hashlib.sha256((attempt / "results.jsonl").read_bytes()).hexdigest(),
               "lifecycle_sha256": hashlib.sha256((attempt / "results.lifecycle.jsonl").read_bytes()).hexdigest(),
               "build_manifest_sha256": hashlib.sha256((root / "provenance/build-manifest.json").read_bytes()).hexdigest()}
(attempt / "attestation.json.tmp").write_text(json.dumps(attestation, indent=2, sort_keys=True) + "\n")
(attempt / "attestation.json.tmp").replace(attempt / "attestation.json")

#!/usr/bin/env python3
"""Audit evidence bytes and saved records only; never run workloads or validators."""
from pathlib import Path
import hashlib
import json
import re
import sys

package = Path(__file__).resolve().parent


def sha(data):
    return hashlib.sha256(data).hexdigest()


def load(name):
    return json.loads((package / name).read_text())


def require(condition, rule, path=""):
    if not condition:
        # Never report the contents of a match, especially fixture credentials.
        raise SystemExit(json.dumps({"passed": False, "rule": rule, "path": str(path)}))


exports = load("EXPORT-MAP.json")
root = Path(exports["source_root"])
original_base = root / "tmp/wan-terminal-attribution-20261017"
destinations = set()
for record in exports["copies"]:
    name = record["destination"]
    require(name not in destinations, "unique copy destination", name)
    destinations.add(name)
    path = package / name
    require(path.is_file() and not path.is_symlink(), "regular copy", name)
    data = path.read_bytes()
    require(sha(data) == record["sha256"] and len(data) == record["bytes"], "exact copy hash/bytes", name)
    original = root / record["source"]
    require(original.is_file() and not original.is_symlink() and sha(original.read_bytes()) == record["sha256"], "actual source/copy binding", record["source"])
bindings = load("SOURCE-BINDING.json")
counts = []
for inventory in bindings["source_inventories"]:
    path = root / inventory["source"]
    require(sha(path.read_bytes()) == inventory["sha256"], "original full inventory hash")
    lines = path.read_text().splitlines()
    require(len(lines) == inventory["entries_rehashed"], "input inventory cardinality")
    size = 0
    for line in lines:
        expected, name = line.split("  ", 1)
        data = (original_base / name).read_bytes()
        require(sha(data) == expected, "actual full input hash", name)
        size += len(data)
    require(size == inventory["actual_input_bytes"], "actual input bytes")
    counts.append(len(lines))
require(counts == [459, 460], "tested and compile inventory counts")
for category in ["frozen_runner_inputs", "corrected_plan_input_hashes", "frozen_chronology_inputs"]:
    for record in bindings[category]:
        data = (root / record["source"]).read_bytes()
        require(sha(data) == record["sha256"] and len(data) == record["bytes"], "actual frozen plan/runner input", record["source"])
require(len(bindings["frozen_runner_inputs"]) == 38, "frozen runner source cardinality")
disposition = load("INPUT-DISPOSITION.json")
seen_inputs = set()
for record in disposition["records"]:
    require(record["source"] not in seen_inputs, "unique input disposition", record["source"])
    seen_inputs.add(record["source"])
    data = (root / record["source"]).read_bytes()
    require(sha(data) == record["sha256"] and len(data) == record["bytes"], "actual selected/omitted original hash/bytes", record["source"])

old_plan = load("sustained-plan/COMMANDS.json")
plan = load("sustained-plan-2/COMMANDS.json")
require(sha((package / "sustained-execution/FINAL-AUDIT.json").read_bytes()) == bindings["owner_frozen_final_audit_sha256"], "owner-frozen final audit hash")
completed_checkpoint_entries = (package / "sustained-execution/SHA256SUMS").read_text().splitlines()
require(len(completed_checkpoint_entries) == 271, "complete frozen runtime/analysis checkpoint cardinality")
for line in completed_checkpoint_entries:
    expected_hash, name = line.split("  ", 1)
    require(sha((original_base / name).read_bytes()) == expected_hash, "actual completed checkpoint input hash", name)
chronology = load("analysis/THRESHOLD-CHRONOLOGY.json")
for record in chronology["inputs"]:
    path = Path(record["path"])
    if not path.is_absolute():
        path = original_base / path
    require(sha(path.read_bytes()) == record["sha256"], "actual chronology evidence input hash", record["path"])

old_plan["analysis_contract"][-1] = old_plan["analysis_contract"][-1].replace("of96 transfers with", "of32 transfers (96 total) with")
require(old_plan == plan, "plan correction changes only qualification transfer-count wording")
require(plan["payload_bytes"] == 268435456 and plan["transfers_per_arm"] == 16 and plan["total_transfers_if_both_arms_run"] == 32 and not plan["retry"] and not plan["performance_gate_clearance"], "exact one-pair sustained workload")
require(plan["bounds"]["task_artifact_budget_bytes"] == plan["storage_allowance"]["bytes"] == 268435456 and not plan["storage_allowance"]["application_memory_changed"], "prospective storage-only256MiB allowance")
receipt = load("source-binding/build-receipt.json")
require(sha((package / "source-binding/build-receipt.json").read_bytes()) == plan["build_receipt_sha256"] == bindings["build_receipt_sha256"] and receipt["source_ref"] == plan["source_ref"] == bindings["source_reference"], "same actual compiled receipt/source binding")
for binary in receipt["binaries"].values():
    data = Path(binary["path"]).read_bytes()
    require(sha(data) == binary["sha256"] and len(data) == binary["size"], "actual reused binary hash/size", binary["path"])
toolchain = load("source-binding/TOOLCHAIN.json")
for record in toolchain["files"]:
    require(sha(Path(record["path"]).read_bytes()) == record["sha256"], "actual pinned toolchain/source hash", record["path"])
prior_storage = load("prior-storage/BUDGET-AMENDMENT.json")
require(prior_storage["original_cap_bytes"] == 104857600 and prior_storage["new_cap_bytes"] == 134217728 and not prior_storage["workload_application_memory_timing_acceptance_retry_changes"], "original100/128MiB lineage retained")
final = load("sustained-execution/FINAL-AUDIT.json")
require(final["audit_accepted"] and not final["diagnostic_performance_accepted"] and not final["qualification_cleared"], "accepted audit preserves performance rejection")
require(final["source_hash_checks"] == 933 and final["source_unchanged"] and not final["remaining_owned_processes_or_namespaces"], "original final source/owner audit")
require(final["task_artifact_bytes"] == 143281052 and final["artifact_cap_bytes"] == 268435456 and final["task_artifact_bytes"] < final["artifact_cap_bytes"], "actual sustained storage bound")
require(load("sustained-execution/preflight.json")["exit_status"] == 0 and load("sustained-execution/h1-prerequisites.json")["accepted"] and load("arms/h1-continuation.json")["accepted"], "actual preflight and conditional H2 prerequisites")
helper_count = 0
exhausted_histories = 0
floor_failures = {}
for protocol, status in [("h1", 0), ("h2", 1)]:
    base = "arms/" + protocol + "/"
    execution = load("sustained-execution/" + protocol + ".result.json")
    require(execution["natural_exit"] and execution["exit_status"] == status and not execution["forced"] and not execution["remaining_group_members"], "actual natural arm exit including H2 nonzero", protocol)
    require(execution["argv"] == next(c["argv"] for c in plan["commands"] if c["protocol"] == protocol), "actual corrected-plan argv", protocol)
    for stream in ["stdout", "stderr"]:
        path = package / "sustained-execution" / (protocol + "." + stream + ".log")
        require(sha(path.read_bytes()) == execution[stream + "_sha256"], "actual arm command output hash", protocol)
    att = load(base + "attestation.json")
    require(att["accepted"] == (status == 0) and not att["integrity_issues"] and not att["conditioning_issues"] and att["performance_gate_enabled"], "exact integrity/conditioning/performance partition", protocol)
    require(att["validation_issues"] == att["performance_issues"] and bool(att["performance_issues"]) == (status == 1), "sole H2 failure remains performance", protocol)
    for filename, key in [("results.jsonl", "results_sha256"), ("results.lifecycle.jsonl", "lifecycle_sha256"), ("results.summary.json", "summary_sha256")]:
        require(sha((package / (base + filename)).read_bytes()) == att[key], "attested artifact hash", protocol + "/" + filename)
    rows = [json.loads(line) for line in (package / (base + "results.jsonl")).read_text().splitlines()]
    require(len(rows) == 16 and all(r["configured_payload_bytes"] == 268435456 and r["attempted"] == 1 and r["useful_completed"] == 1 and r["public_protocol"] == protocol for r in rows), "sixteen correct256MiB transfers", protocol)
    failed = [r["diagnostic_sequence"] for r in rows if r["transfer_direction"] == "download" and r["useful_transfer_bytes_per_second"] < 29700000]
    require(failed == ([] if protocol == "h1" else [11, 14]), "exact failed floor rows", protocol)
    floor_failures[protocol] = failed
    if protocol == "h2":
        expected_rates = {11: 27480695.314998038, 14: 27495905.7358898, 15: 34927637.84539039}
        require(all(next(r["useful_transfer_bytes_per_second"] for r in rows if r["diagnostic_sequence"] == seq) == value for seq, value in expected_rates.items()), "preserved exact H2 rates")
    lifecycle = [json.loads(line) for line in (package / (base + "results.lifecycle.jsonl")).read_text().splitlines()]
    exits = [r for r in lifecycle if r["event"] == "process-exit"]
    require(len(exits) == 5 and all(r["status"] == 0 and not r["forced"] and r["group_joined"] and not r["invalidates_success"] for r in exits), "five natural-zero endpoint/helper exits", protocol)
    helper_count += len(exits)
    prospective = load(base + "prospective-terminal-attribution.json")
    independent = load(base + "raw-post-join-supervisor-validation.json")
    saved = load(base + "results.sender-wait-controls/final-raw-write-validation.json")
    require(prospective["attribution_accepted"] and prospective["classifications"] == [] and not prospective["performance_gate_cleared"], "accepted attribution without exercised exception", protocol)
    require(independent["accepted"] and independent["independent_validation"] == independent["saved_validation"] == saved == prospective["legacy_final_verdict"] == att["raw_write_final_closure"] and saved["accepted"] and not saved["issues"], "preserved reviewed106 independent/saved/prospective equality", protocol)
    sidecar = load(base + "results.sender-wait-controls/final-raw-write-terminal.json.attribution.json")
    require(sidecar["intent_complete"] and not sidecar["invalid"] and sidecar["expected_owners"] == len(sidecar["owners"]) == 5, "five complete owner intents", protocol)
    require(sidecar["raw_sha256"] == sha((package / (base + "results.sender-wait-controls/final-raw-write-terminal.json")).read_bytes()), "final raw/prospective hash binding", protocol)
    for owner in sidecar["owners"]:
        state = owner["state"]
        require(state["tcp_bound"] and state["physical_close_calls"] == 1 and not state["physical_close_error"] and not state["underlying_escaped"] and not state["raw_errors"] and not state["raw_shorts"] and state["joined"] > state["close_end"] > state["close_begin"] > state["stop"], "exclusive Close and retirement with zero raw errors/shorts", protocol)
    require(len(att["tcp_state_intervals"]) == 4, "four selected data lanes", protocol)
    for interval in att["tcp_state_intervals"]:
        history = interval["threshold_history"]["history"]
        require(history["exhausted"] and history["failure_reason"] == "attempt_limit" and history["monitored_raw_write_attempts"] == 4096 and not history["transition_observed"] and history["inconclusive"], "history cap keeps transition inconclusive", protocol)
        exhausted_histories += 1
    supervisor = load(base + "supervisor-lifecycle.json")
    cleanup = load(base + "supervisor-cleanup.json")
    require(supervisor["runner_status"] == status and supervisor["group_joined"] and not supervisor["forced"] and supervisor["invalidates_success"] == (status == 1) and supervisor["server_scope"]["unit_joined"] and not cleanup["forced_supervisor_cleanup"] and cleanup["entry_status"] == status, "natural cleanup retains actual failed arm", protocol)
    require(int((package / (base + "runner.status")).read_text()) == status and int((package / (base + "validation.status")).read_text()) == status and load(base + "server-scope/unit-final.json")["joined"], "authoritative runner/validation/scope statuses", protocol)
require(helper_count == 10 and exhausted_histories == 8, "total natural helpers and capped data-lane histories")

# Read sensitive fixture payloads into memory only; matched values are never printed.
known_payloads = []
for record in disposition["records"]:
    path = root / record["source"]
    if path.suffix == ".pem":
        known_payloads.extend(line for line in path.read_bytes().splitlines() if len(line) >= 32 and not line.startswith(b"-----"))
    if path.name.endswith("-server.ready.json"):
        for agent in json.loads(path.read_text()).get("agents", []):
            if isinstance(agent.get("token"), str) and len(agent["token"]) >= 8:
                known_payloads.append(agent["token"].encode())
for command in plan["commands"]:
    argv = command["argv"]
    for flag in ["--cert", "--key"]:
        path = Path(argv[argv.index(flag) + 1])
        known_payloads.extend(line for line in path.read_bytes().splitlines() if len(line) >= 32 and not line.startswith(b"-----"))
password = re.search(r"password\s+`([^`]+)`", Path("/home/user/Documents/p2pstream/AGENTS.md").read_text())
require(password is not None and bool(known_payloads), "known fixture secret scan inputs")
known_payloads.append(password.group(1).encode())
prohibited = {".pem", ".key", ".crt", ".db", ".sqlite", ".sqlite3", ".zip", ".gz", ".xz", ".tar", ".exe", ".bin", ".test", ".o", ".a", ".so", ".go"}
marker = re.compile(br"(?m)^-----BEGIN (?:[A-Z ]*PRIVATE KEY|CERTIFICATE)-----")
files = sorted(p for p in package.rglob("*") if p.is_file())
require(not list(package.rglob("*.go")), "no discoverable Go artifact")
expected = destinations | set(exports["generated"])
actual = {str(p.relative_to(package)) for p in files}
allowed_absent = {"VALIDATION.json", "SHA256SUMS"} if "--finalize" in sys.argv else set()
require(actual <= expected and expected - actual <= allowed_absent, "exact curated package membership")
for path in files:
    name = str(path.relative_to(package)); data = path.read_bytes()
    require(not path.is_symlink() and path.suffix.lower() not in prohibited and ".ready." not in path.name and path.name != "server-app-ready.json", "prohibited format/readiness", name)
    require(b"\0" not in data and marker.search(data) is None, "binary or PEM content", name)
    require(not any(secret in data for secret in known_payloads), "known fixture secret/certificate payload", name)

result = {"passed": True, "audit_scope": "File/hash/content and saved evidence consistency only; no build/test/validator under review/runtime invocation.", "copies_verified_against_actual_sources": len(exports["copies"]), "actual_full_source_inventory_counts": counts, "actual_frozen_runner_inputs_rehashed": 38, "actual_reused_binary_hashes_verified": len(receipt["binaries"]), "actual_toolchain_source_hashes_verified": len(toolchain["files"]), "completed_input_disposition_hashes_verified": len(disposition["records"]), "correct_payload_transfers": 32, "natural_endpoint_helper_exits": helper_count, "arm_natural_exit_statuses": {"h1": 0, "h2": 1}, "floor_failures_preserved": floor_failures, "capped_inconclusive_data_lane_histories": exhausted_histories, "prospective_classifications": [], "exception_exercised": False, "diagnostic_performance_accepted": False, "qualification_cleared": False, "original_audit933_count_preserved": True, "owner_frozen_runtime_analysis_checkpoint_entries_verified": len(completed_checkpoint_entries), "actual_chronology_evidence_inputs_rehashed": len(chronology["inputs"]), "nonprinting_known_fixture_secret_scan_passed": True, "go_files_remaining": 0, "exact_package_membership": True}
if "--finalize" in sys.argv:
    (package / "VALIDATION.json").write_text(json.dumps(result, indent=2) + "\n")
    # Include nested historical manifests. Exclude ONLY this root manifest.
    lines = [sha(p.read_bytes()) + "  " + str(p.relative_to(package)) for p in sorted(package.rglob("*")) if p.is_file() and p != package / "SHA256SUMS"]
    (package / "SHA256SUMS").write_text("\n".join(lines) + "\n")
seen = set()
for line in (package / "SHA256SUMS").read_text().splitlines():
    expected_hash, name = line.split("  ", 1)
    require(name not in seen, "unique package manifest entry", name)
    seen.add(name)
    require(sha((package / name).read_bytes()) == expected_hash, "package manifest hash", name)
require(seen == {str(p.relative_to(package)) for p in package.rglob("*") if p.is_file() and p != package / "SHA256SUMS"}, "complete root manifest including nested manifests")
print(json.dumps({"passed": True, "copies_verified": len(exports["copies"]), "input_inventories": counts, "package_files": len(seen) + 1, "package_bytes": sum(p.stat().st_size for p in package.rglob("*") if p.is_file()), "root_manifest_sha256": sha((package / "SHA256SUMS").read_bytes()), "known_fixture_secret_scan_passed": True, "go_files_remaining": 0}))

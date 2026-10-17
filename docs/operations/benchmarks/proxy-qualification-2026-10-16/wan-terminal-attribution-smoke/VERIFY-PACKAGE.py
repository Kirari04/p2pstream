#!/usr/bin/env python3
"""Read-only evidence audit, or --finalize to write generated audit/manifest only."""
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
        # Never print matched content, including credential/fixture bytes.
        raise SystemExit(json.dumps({"passed": False, "rule": rule, "path": str(path)}))


exports = load("EXPORT-MAP.json")
root = Path(exports["source_root"])
original_base = root / "tmp/wan-terminal-attribution-20261017"
destinations = set()
for record in exports["copies"]:
    name = record["destination"]
    require(name not in destinations, "unique selected destination", name)
    destinations.add(name)
    path = package / name
    require(path.is_file() and not path.is_symlink(), "regular selected copy", name)
    data = path.read_bytes()
    require(sha(data) == record["sha256"] and len(data) == record["bytes"], "copy hash/bytes", name)
    original = root / record["source"]
    require(original.is_file() and not original.is_symlink() and sha(original.read_bytes()) == record["sha256"], "actual source binding", record["source"])

bindings = load("SOURCE-BINDING.json")
inventory_counts = []
for inventory in bindings["source_inventories"]:
    path = root / inventory["source"]
    require(sha(path.read_bytes()) == inventory["sha256"], "full inventory binding", inventory["source"])
    lines = path.read_text().splitlines()
    require(len(lines) == inventory["entries_rehashed"], "input inventory cardinality")
    total_bytes = 0
    for line in lines:
        expected, name = line.split("  ", 1)
        data = (original_base / name).read_bytes()
        require(sha(data) == expected, "actual full input hash", name)
        total_bytes += len(data)
    require(total_bytes == inventory["actual_input_bytes"], "actual input bytes")
    inventory_counts.append(len(lines))
require(inventory_counts == [459, 460], "exact tested and compiled inventories")
for record in bindings["runner_review_bindings"]:
    require(sha((root / record["source"]).read_bytes()) == record["sha256"], "actual frozen runner input", record["source"])
require(len(bindings["runner_review_bindings"]) == 38, "runner source cardinality")
require(sum(r["only_unblock_changes_hash"] for r in bindings["runner_review_bindings"]) == 1, "only receipt unblock changes review tuple")

disposition = load("INPUT-DISPOSITION.json")
seen_inputs = set()
for record in disposition["records"]:
    require(record["source"] not in seen_inputs, "unique input disposition", record["source"])
    seen_inputs.add(record["source"])
    data = (root / record["source"]).read_bytes()
    require(sha(data) == record["sha256"] and len(data) == record["bytes"], "original omitted/selected input hash/bytes", record["source"])

receipt = load("build/receipt.json")
require(sha((package / "build/receipt.json").read_bytes()) == "ad0dbfb3adafab686a4fc84f75effaee57819f2694ae644ada901a192604ad62", "approved actual receipt hash")
require(receipt["accepted"] and not receipt["endpoint_execution"] and not receipt["wan_traffic"] and receipt["source_before_after_unchanged"], "compile-only receipt acceptance")
require(receipt["production_reference"] == "27c8e31f3826ab5326d941b20644c1f56159d99d" and receipt["source_ref"] == bindings["source_reference"], "compiled source reference")
require([r["name"] for r in receipt["executions"]] == ["go-env", "agent-build", "server-build", "agent-metadata", "server-metadata"], "five exact build invocations")
wiring = load("wiring-window/RESULT.json")
require(wiring["accepted"] and wiring["source_before_after_unchanged"] and len(wiring["executions"]) == 4, "wiring acceptance")
for prefix, records in [("build", receipt["executions"]), ("wiring-window", wiring["executions"])]:
    for record in records:
        require(record["exit_status"] == 0 and not record["forced"] and not record["remaining_group_members"], "natural command zero/group join", record["name"])
        for stream in ["stdout", "stderr"]:
            path = package / prefix / (record["name"] + "." + stream + ".log")
            require(sha(path.read_bytes()) == record[stream + "_sha256"], "saved command output binding", str(path.relative_to(package)))
require(load("wiring-window/h1-continuation.json")["case_count"] == 20, "twenty continuation cases")
outcomes = load("wiring-window/arm-outcome.json")
require(outcomes["accepted"] and outcomes["case_count"] == 7 and outcomes["revisit_case_count"] == 4 and outcomes["producer_hash_key_count"] == 106, "outcome/revisit/hash mapping counts")

commands = load("smoke-freeze/COMMANDS.json")
require(sha((package / "final-source/integration/run-wan-below-tls-arm.sh").read_bytes()) == commands["runner_sha256"], "actual final smoke runner hash")
require(commands["bounds"]["task_artifact_budget_bytes"] == 104857600, "original 100MiB storage bound")
amendment = load("smoke-execution/BUDGET-AMENDMENT.json")
require(amendment["original_cap_bytes"] == 104857600 and amendment["new_cap_bytes"] == 134217728 and not amendment["workload_application_memory_timing_acceptance_retry_changes"] and not amendment["h1_rerun"], "prospective storage-only amendment")
require(amendment["original_manifest_sha256"] == sha((package / "smoke-freeze/COMMANDS.json").read_bytes()), "amended original command manifest")
final = load("smoke-execution/FINAL-AUDIT.json")
require(final["accepted"] and final["source_unchanged"] and final["source_hash_checks"] == 944 and not final["remaining_owned_processes_or_namespaces"] and not final["performance_qualification_cleared"] and not final["sustained_traffic_run"], "original final audit scope/join")
require(final["storage_amendment_sha256"] == sha((package / "smoke-execution/BUDGET-AMENDMENT.json").read_bytes()) and final["task_artifact_bytes"] < final["artifact_cap_bytes"] == 134217728, "actual final storage bound")
require(load("smoke-execution/preflight.json")["exit_status"] == 0, "real-clock certificate preflight")
close_notify_count = 0
endpoint_helper_count = 0
for protocol in ["h1", "h2"]:
    base = "arms/" + protocol + "/"
    execution = load("smoke-execution/" + protocol + ".result.json")
    require(execution["natural_zero"] and execution["exit_status"] == 0 and not execution["forced"] and not execution["remaining_group_members"], "natural arm zero/group join", protocol)
    for stream in ["stdout", "stderr"]:
        require(sha((package / "smoke-execution" / (protocol + "." + stream + ".log")).read_bytes()) == execution[stream + "_sha256"], "arm output binding", protocol)
    attestation = load(base + "attestation.json")
    require(attestation["accepted"] and not attestation["validation_issues"] and attestation["row_count"] == 16 and attestation["natural_endpoint_and_namespace_statuses"] and attestation["outer_runner_group_joined"] and not attestation["performance_gate_enabled"], "complete diagnostic arm attestation", protocol)
    rows = [json.loads(line) for line in (package / (base + "results.jsonl")).read_text().splitlines()]
    require(len(rows) == 16 and all(r["configured_payload_bytes"] == 4194304 and r["attempted"] == 1 and r["useful_completed"] == 1 and r["public_protocol"] == protocol for r in rows), "sixteen exact 4MiB transfers", protocol)
    require(sha((package / (base + "results.jsonl")).read_bytes()) == attestation["results_sha256"] and sha((package / (base + "results.lifecycle.jsonl")).read_bytes()) == attestation["lifecycle_sha256"] and sha((package / (base + "results.summary.json")).read_bytes()) == attestation["summary_sha256"], "attested canonical artifacts", protocol)
    lifecycle = [json.loads(line) for line in (package / (base + "results.lifecycle.jsonl")).read_text().splitlines()]
    exits = [r for r in lifecycle if r["event"] == "process-exit"]
    require(len(exits) == 5 and all(r["status"] == 0 and not r["forced"] and r["group_joined"] and not r["invalidates_success"] for r in exits), "five natural endpoint/helper exits", protocol)
    endpoint_helper_count += len(exits)
    prospective = load(base + "prospective-terminal-attribution.json")
    independent = load(base + "raw-post-join-supervisor-validation.json")
    saved = load(base + "results.sender-wait-controls/final-raw-write-validation.json")
    require(prospective["attribution_accepted"] and prospective["classifications"] == [] and not prospective["issues"] and not prospective["performance_gate_cleared"], "prospective accepted without exception", protocol)
    require(independent["accepted"] and independent["independent_validation"] == independent["saved_validation"] == saved == prospective["legacy_final_verdict"] == attestation["raw_write_final_closure"] and saved["accepted"] and not saved["issues"], "independent and saved preserved verdict equality", protocol)
    sidecar = load(base + "results.sender-wait-controls/final-raw-write-terminal.json.attribution.json")
    require(sidecar["intent_complete"] and not sidecar["invalid"] and sidecar["expected_owners"] == len(sidecar["owners"]) == 5, "five complete terminal owners", protocol)
    require(sidecar["raw_sha256"] == sha((package / (base + "results.sender-wait-controls/final-raw-write-terminal.json")).read_bytes()), "raw/prospective sidecar binding", protocol)
    for owner in sidecar["owners"]:
        state = owner["state"]
        require(state["tcp_bound"] and state["physical_close_calls"] == 1 and not state["physical_close_error"] and not state["underlying_escaped"] and not state["raw_errors"] and not state["raw_shorts"] and state["joined"] > state["close_end"] > state["close_begin"] > state["stop"], "exclusive physical Close/retirement", protocol)
        for raw in state["records"]:
            if "crypto/tls.(*Conn).closeNotify" in raw["stack"]["functions"]:
                require(not raw["error"] and not raw["short"] and raw["written"] == raw["attempt"] and raw["plain"]["begin"] == 0 and not raw["plain"]["control"], "successful TLS protocol write clears plaintext", protocol)
                close_notify_count += 1
    supervisor = load(base + "supervisor-lifecycle.json")
    cleanup = load(base + "supervisor-cleanup.json")
    require(supervisor["group_joined"] and not supervisor["forced"] and not supervisor["invalidates_success"] and supervisor["server_scope"]["unit_joined"] and not cleanup["forced_supervisor_cleanup"] and not cleanup["invalidates_success"], "actual supervisor cleanup", protocol)
    require(load(base + "server-scope/unit-final.json")["joined"], "actual scope join", protocol)
require(close_notify_count == 9 and endpoint_helper_count == 10, "terminal protocol and natural exit counts")

# Sensitive input values remain only in memory; never include them in diagnostics.
known_payloads = []
preflight = load("smoke-execution/preflight.json")
for value in preflight["argv"]:
    path = Path(value)
    if value.endswith(".pem") and path.is_file():
        known_payloads.extend(line for line in path.read_bytes().splitlines() if len(line) >= 32 and not line.startswith(b"-----"))
for record in disposition["records"]:
    path = root / record["source"]
    if path.suffix == ".pem":
        known_payloads.extend(line for line in path.read_bytes().splitlines() if len(line) >= 32 and not line.startswith(b"-----"))
    if path.name.endswith("-server.ready.json"):
        for agent in json.loads(path.read_text()).get("agents", []):
            if isinstance(agent.get("token"), str) and len(agent["token"]) >= 8:
                known_payloads.append(agent["token"].encode())
for execution in [load("smoke-execution/h1.result.json"), load("smoke-execution/h2.result.json")]:
    argv = execution["argv"]
    for flag in ["--cert", "--key"]:
        path = Path(argv[argv.index(flag) + 1])
        known_payloads.extend(line for line in path.read_bytes().splitlines() if len(line) >= 32 and not line.startswith(b"-----"))
instructions = Path("/home/user/Documents/p2pstream/AGENTS.md").read_text()
password = re.search(r"password\s+`([^`]+)`", instructions)
require(password is not None and bool(known_payloads), "known secret scan inputs")
known_payloads.append(password.group(1).encode())
prohibited = {".pem", ".key", ".crt", ".db", ".sqlite", ".sqlite3", ".zip", ".gz", ".xz", ".tar", ".exe", ".bin", ".test", ".o", ".a", ".so", ".go"}
marker = re.compile(br"(?m)^-----BEGIN (?:[A-Z ]*PRIVATE KEY|CERTIFICATE)-----")
files = sorted(p for p in package.rglob("*") if p.is_file())
require(not list(package.rglob("*.go")), "no discoverable Go artifact")
expected = destinations | set(exports["generated"])
actual = {str(p.relative_to(package)) for p in files}
allowed_absent = {"VALIDATION.json", "SHA256SUMS"} if "--finalize" in sys.argv else set()
require(actual <= expected and expected - actual <= allowed_absent, "exact curated membership")
for path in files:
    name = str(path.relative_to(package)); data = path.read_bytes()
    require(not path.is_symlink() and path.suffix.lower() not in prohibited and ".ready." not in path.name and path.name != "server-app-ready.json", "prohibited format/readiness", name)
    require(b"\0" not in data and marker.search(data) is None, "binary or PEM material", name)
    require(not any(secret in data for secret in known_payloads), "known fixture credential/certificate payload", name)

result = {"passed": True, "audit_scope": "File/hash/content and saved evidence consistency checks only. No builds/tests/validators under review/runtime invoked.", "copies_verified_against_actual_sources": len(exports["copies"]), "input_inventory_counts_rehashed": inventory_counts, "frozen_runner_inputs_rehashed": 38, "completed_range_input_hashes_verified": len(disposition["records"]), "canonical_transfers_completed": 32, "natural_endpoint_helper_exits": endpoint_helper_count, "successful_tls_close_notify_with_cleared_plaintext": close_notify_count, "prospective_classifications": [], "exception_exercised": False, "original_audit_944_hash_checks_preserved": True, "original_and_amended_storage_bounds_verified": True, "nonprinting_known_fixture_secret_scan_passed": True, "go_files_remaining": 0, "package_membership_exact": True, "performance_qualification_cleared": False}
if "--finalize" in sys.argv:
    (package / "VALIDATION.json").write_text(json.dumps(result, indent=2) + "\n")
    # Exclude ONLY the root manifest: nested original manifests are payload too.
    lines = [sha(p.read_bytes()) + "  " + str(p.relative_to(package)) for p in sorted(package.rglob("*")) if p.is_file() and p != package / "SHA256SUMS"]
    (package / "SHA256SUMS").write_text("\n".join(lines) + "\n")
seen = set()
for line in (package / "SHA256SUMS").read_text().splitlines():
    expected_sha, name = line.split("  ", 1)
    require(name not in seen, "unique package manifest entry", name)
    seen.add(name)
    require(sha((package / name).read_bytes()) == expected_sha, "package manifest hash", name)
require(seen == {str(p.relative_to(package)) for p in package.rglob("*") if p.is_file() and p != package / "SHA256SUMS"}, "complete root manifest including nested manifests")
print(json.dumps({"passed": True, "copies_verified": len(exports["copies"]), "input_inventories": inventory_counts, "package_files": len(seen) + 1, "package_bytes": sum(p.stat().st_size for p in package.rglob("*") if p.is_file()), "root_manifest_sha256": sha((package / "SHA256SUMS").read_bytes()), "known_fixture_secret_scan_passed": True, "go_files_remaining": 0}))

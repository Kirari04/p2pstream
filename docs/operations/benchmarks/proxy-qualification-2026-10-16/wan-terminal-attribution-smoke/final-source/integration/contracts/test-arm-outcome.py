#!/usr/bin/env python3
from __future__ import annotations

import ast
import copy
import hashlib
import importlib.util
import json
import pathlib
import re
import shlex
import subprocess
import sys


if len(sys.argv) != 5:
    raise SystemExit("usage: test-arm-outcome.py ARM VALIDATOR OUTCOME_MODULE OUTPUT")
arm_path, validator_path, module_path, output = map(pathlib.Path, sys.argv[1:])
if output.exists():
    raise SystemExit("fresh output required")
work = output.parent / "arm-outcome-fixture"
if work.exists():
    raise SystemExit("fresh fixture directory required")
work.mkdir(mode=0o700)

spec = importlib.util.spec_from_file_location("arm_outcome", module_path.resolve())
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

validator_source = validator_path.read_text()
tree = ast.parse(validator_source, filename=str(validator_path))
required = next(ast.literal_eval(node.value) for node in ast.walk(tree)
                if isinstance(node, ast.Assign)
                and any(isinstance(target, ast.Name) and target.id == "required_hashes" for target in node.targets))
arm = arm_path.read_text()

# Execute the actual embedded write_hashes producer, including its real argument
# expansion and Python body. Every path argument is a bounded fixture file; $0
# remains the extracted shell producer itself, exactly as in the arm.
function_start = arm.index("write_hashes() {")
function_end = arm.index('\n}\nwrite_hashes "$pre_hashes"', function_start) + 2
write_hashes_source = arm[function_start:function_end]
manifest_path = work / "pre-hashes.json"
producer_script = work / "write-hashes-producer.sh"
path_variables = sorted(set(re.findall(r'"\$([A-Za-z_][A-Za-z0-9_]*)"', write_hashes_source))
                        - {"destination", "server_source_ref", "aggregate_agent_source_ref",
                           "expected_leaf_sha256", "size_mode"})
path_fixtures = {}
for name in path_variables:
    path = work / f"bound-{name}"
    path.write_bytes(f"bounded producer fixture: {name}\n".encode())
    path_fixtures[name] = path
assignments = [f"{name}={shlex.quote(str(path_fixtures[name].resolve()))}" for name in path_variables]
assignments.extend([
    "server_source_ref=fixture-server-source", "aggregate_agent_source_ref=fixture-agent-source",
    f"expected_leaf_sha256={'a' * 64}", "size_mode=small",
])
producer_script.write_text("#!/usr/bin/env bash\nset -Eeuo pipefail\n" + write_hashes_source + "\n" +
                           "\n".join(assignments) + "\n" +
                           f"write_hashes {shlex.quote(str(manifest_path.resolve()))}\n")
producer_script.chmod(0o700)
producer = subprocess.run(
    ["/usr/bin/bash", str(producer_script.resolve())], cwd=work,
    env={"PATH": "/usr/bin:/bin", "LC_ALL": "C", "PYTHONDONTWRITEBYTECODE": "1"},
    stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=15, check=False)
if producer.returncode != 0:
    raise SystemExit(f"actual write_hashes producer failed {producer.returncode}: {producer.stderr!r}")
pre = json.loads(manifest_path.read_text())
post = copy.deepcopy(pre)
manifest_result = module.validate_hash_manifests(pre, post, "small", required)
if manifest_result["issues"] or set(pre.get("sha256", {})) != required:
    raise SystemExit(f"actual write_hashes manifest rejected: {manifest_result!r}")
if len(required) != 106:
    raise SystemExit(f"actual producer key cardinality changed: {len(required)}")
if (pre.get("server_source_ref") != "fixture-server-source"
        or pre.get("aggregate_agent_source_ref") != "fixture-agent-source"
        or pre.get("expected_leaf_der_sha256") != "a" * 64
        or pre.get("size_mode") != "small"):
    raise SystemExit("actual producer scalar argument mapping changed")
producer_sha = hashlib.sha256(producer_script.read_bytes()).hexdigest()
python_start = write_hashes_source.index("<<'PY'\n") + len("<<'PY'\n")
python_end = write_hashes_source.index("\nPY\n}", python_start)
producer_tree = ast.parse(write_hashes_source[python_start:python_end], filename=f"{arm_path}:write_hashes")
shell_argument_names = re.findall(
    r'"\$(0|[A-Za-z_][A-Za-z0-9_]*)"', write_hashes_source[:python_start])
python_argument_names = None
for node in producer_tree.body:
    if isinstance(node, ast.Assign) and len(node.targets) == 1 and isinstance(node.targets[0], ast.Tuple):
        if all(isinstance(element, ast.Name) for element in node.targets[0].elts):
            python_argument_names = [element.id for element in node.targets[0].elts]
            break
if python_argument_names is None or len(python_argument_names) != len(shell_argument_names):
    raise SystemExit("actual shell argv and embedded Python parameter cardinality differ")
python_to_shell = dict(zip(python_argument_names, shell_argument_names))
if len(python_to_shell) != len(python_argument_names):
    raise SystemExit("actual embedded Python parameter tuple repeats a name")
expected_aliases = {
    "supervisor": "0",
    "sender_snapshot_validator_current": "sender_snapshot_validator",
    "approved": "approved_runner",
    "server": "server_binary",
    "agent": "aggregate_agent_binary",
    "server_ref": "server_source_ref",
    "agent_ref": "aggregate_agent_source_ref",
    "fingerprint": "expected_leaf_sha256",
}
observed_aliases = {python_name: shell_name for python_name, shell_name in python_to_shell.items()
                    if python_name != shell_name}
if observed_aliases != expected_aliases:
    raise SystemExit(f"actual shell-to-Python argv aliases changed: {observed_aliases!r}")
key_to_variable = None
for node in ast.walk(producer_tree):
    if isinstance(node, ast.Dict):
        entries = {}
        for key, value in zip(node.keys, node.values):
            if (isinstance(key, ast.Constant) and isinstance(key.value, str)
                    and isinstance(value, ast.Call) and isinstance(value.func, ast.Name)
                    and value.func.id == "sha" and len(value.args) == 1
                    and isinstance(value.args[0], ast.Name)):
                entries[key.value] = value.args[0].id
        if "supervisor" in entries and "server_binary" in entries:
            key_to_variable = entries
            break
if key_to_variable is None or set(key_to_variable) != required:
    raise SystemExit("actual producer key-to-argument mapping is incomplete")
if len(set(key_to_variable.values())) != len(required):
    raise SystemExit("actual producer duplicates an embedded Python variable across hash keys")
shell_hash_arguments = {}
for key, python_variable in key_to_variable.items():
    shell_hash_arguments.setdefault(python_to_shell[python_variable], []).append(key)
expected_shared_shell_arguments = {
    "sender_snapshot_validator": {"sender_snapshot_validator_current", "sender_snapshot_validator"},
}
observed_shared_shell_arguments = {
    shell_name: set(keys) for shell_name, keys in shell_hash_arguments.items() if len(keys) > 1
}
if observed_shared_shell_arguments != expected_shared_shell_arguments:
    raise SystemExit(f"actual producer path aliases changed: {observed_shared_shell_arguments!r}")
expected_hashes = {
    key: (producer_sha if python_to_shell[python_variable] == "0"
          else hashlib.sha256(path_fixtures[python_to_shell[python_variable]].read_bytes()).hexdigest())
    for key, python_variable in key_to_variable.items()
}
if pre["sha256"] != expected_hashes:
    raise SystemExit("actual producer argument/key/hash mapping changed")

if "wan_below_tls_arm_outcome.validate_hash_manifests(" not in validator_source \
        or "wan_below_tls_arm_outcome.classify_summary(" not in validator_source \
        or "wan_below_tls_arm_outcome.classify_runner(" not in validator_source \
        or "wan_below_tls_arm_outcome.attestation_outcome(" not in validator_source:
    raise SystemExit("final arm validator does not call the reviewed outcome functions")


def phases(passed: bool):
    return {stage: {"all_at_or_above_floor": passed}
            for stage in ("warmup", "retained-warmup", "steady")}


def summary(floor: bool, reuse: bool = True):
    return {"classification": "observational-complete" if reuse else "inconclusive-upstream-reuse",
            "acceptance": {"correctness": True, "verified_tls_and_protocol_every_response": True,
                           "transport_trace_complete": True, "expected_upstream_reuse": reuse,
                           "response_copy_attribution_complete": True,
                           "download_floor_every_phase": floor}}


def supervisor(status: object, invalidates: object):
    return {"runner_status": status, "invalidates_success": invalidates}


def evaluate(name, size_mode, phase_rows, summary_row, reuse_integrity, reuse_conditioning,
             supervisor_row, supervisor_joined, base_integrity=None, mutate_pre=None, mutate_post=None):
    case_pre = copy.deepcopy(pre)
    case_pre["size_mode"] = size_mode
    if mutate_pre is not None:
        mutate_pre(case_pre)
    case_post = copy.deepcopy(case_pre)
    if mutate_post is not None:
        mutate_post(case_post)
    manifest = module.validate_hash_manifests(case_pre, case_post, size_mode, required)
    outcome = module.classify_summary(size_mode, phase_rows, summary_row,
                                      reuse_integrity, reuse_conditioning, 29_700_000)
    integrity = list(base_integrity or []) + manifest["issues"] + list(reuse_integrity) + outcome["integrity_issues"]
    conditioning = outcome["conditioning_issues"]
    performance = outcome["performance_issues"]
    runner = module.classify_runner(supervisor_row, supervisor_joined, integrity, conditioning, performance)
    integrity.extend(runner["integrity_issues"])
    attestation = module.attestation_outcome(integrity, conditioning, performance)
    return {"name": name, **attestation, "runner": runner}


cases = [
    (evaluate("valid-small-below-floor", "small", phases(False), summary(True), [], [],
              supervisor(0, False), True), True, True),
    (evaluate("valid-slow-sustained", "sustained", phases(False), summary(False), [], [],
              supervisor(1, True), True), True, False),
    (evaluate("conditioning-idle-expiry", "sustained", phases(True), summary(True, False), [],
              ["fully evidenced idle expiry"], supervisor(1, True), True), True, False),
    (evaluate("malformed-missing-hash", "small", phases(True), summary(True), [], [],
              supervisor(0, False), True,
              mutate_pre=lambda value: value["sha256"].pop("server_binary")), False, False),
    (evaluate("changed-hash-manifest", "small", phases(True), summary(True), [], [],
              supervisor(0, False), True,
              mutate_post=lambda value: value["sha256"].__setitem__("server_binary", "1" * 64)), False, False),
    (evaluate("boolean-runner-status", "small", phases(True), summary(True), [], [],
              supervisor(False, False), True), False, False),
    (evaluate("forced-cleanup", "small", phases(True), summary(True), [], [],
              supervisor(0, True), False, base_integrity=["forced cleanup"]), False, False),
]


# Compile and execute the exact revisit-classification statements from the real
# arm validator. This covers the retained, expired, malformed, and impossible
# negative wall-time boundaries without duplicating their decision logic.
top = tree.body
revisit_start = next(index for index, node in enumerate(top)
                     if isinstance(node, ast.Assign)
                     and any(isinstance(target, ast.Name) and target.id == "first_visits"
                             for target in node.targets))
revisit_end = next(index for index, node in enumerate(top[revisit_start:], revisit_start)
                   if isinstance(node, ast.Expr) and isinstance(node.value, ast.Call)
                   and isinstance(node.value.func, ast.Attribute)
                   and isinstance(node.value.func.value, ast.Name)
                   and node.value.func.value.id == "issues" and node.value.func.attr == "extend"
                   and len(node.value.args) == 1 and isinstance(node.value.args[0], ast.Name)
                   and node.value.args[0].id == "reuse_integrity_issues")
revisit_code = compile(ast.fix_missing_locations(ast.Module(
    body=copy.deepcopy(top[revisit_start:revisit_end + 1]), type_ignores=[])),
    str(validator_path), "exec")


def revisit_traces(kind: str):
    traces = [{} for _ in range(8)]
    first = []
    revisit = []
    for index in range(4):
        prior = 50_000_000_000 + index * 1_000
        origin = f"10.0.0.2:{41000 + index}"
        key = f"tcp|10.0.0.1:{51000 + index}|10.0.0.2:443"
        first.append({"connection_key": key, "reused": False, "was_idle": False,
                      "idle_time_ns": 0, "origin_connection_id": origin,
                      "put_idle_conn": [{"observed_unix_ns": prior, "error": ""}]})
        delta = 1_000
        if kind == "expired":
            delta = 30_000_000_000
        elif kind == "negative":
            delta = -1
        revisit_origin: object = origin if kind != "malformed" else index
        revisit.append({"connection_key": key, "reused": True, "was_idle": True,
                        "idle_time_ns": max(delta, 0), "got_conn_unix_ns": prior + delta,
                        "origin_connection_id": revisit_origin, "put_idle_conn": []})
    return traces + first + revisit


revisit_expectations = {
    "retained": (0, 0, True, True),
    "expired": (0, 4, True, False),
    "malformed": (4, 0, False, False),
    "negative": (4, 0, False, False),
}
revisit_rows = []
for name, (want_integrity, want_conditioning, want_safe, want_accepted) in revisit_expectations.items():
    namespace = {"trace_records": revisit_traces(name), "reuse_integrity_issues": [],
                 "reuse_conditioning_issues": [], "reuse_expectations": [], "issues": []}
    exec(revisit_code, namespace)
    integrity = namespace["reuse_integrity_issues"]
    conditioning = namespace["reuse_conditioning_issues"]
    observed = module.attestation_outcome(integrity, conditioning, [])
    if (len(integrity) != want_integrity or len(conditioning) != want_conditioning
            or observed["continuation_safe"] is not want_safe
            or observed["accepted"] is not want_accepted):
        raise SystemExit(f"{name}: revisit boundary outcome changed: {observed!r}")
    revisit_rows.append({"name": name, "integrity_issues": integrity,
                         "conditioning_issues": conditioning, "attestation_outcome": observed,
                         "expected_integrity_count": want_integrity,
                         "expected_conditioning_count": want_conditioning})

rows = []
for observed, expected_safe, expected_accepted in cases:
    if observed["continuation_safe"] is not expected_safe or observed["accepted"] is not expected_accepted:
        raise SystemExit(f"{observed['name']}: unexpected outcome {observed!r}")
    rows.append({**observed, "expected_continuation_safe": expected_safe,
                 "expected_accepted": expected_accepted})

output.write_text(json.dumps({"schema_version": 1, "accepted": True,
                              "coverage": "actual-write-hashes-plus-extracted-revisit-and-outcome-wiring",
                              "producer_status": producer.returncode,
                              "producer_hash_key_count": len(required),
                              "producer_manifest_sha256": hashlib.sha256(manifest_path.read_bytes()).hexdigest(),
                              "case_count": len(rows), "cases": rows,
                              "revisit_case_count": len(revisit_rows), "revisit_cases": revisit_rows},
                             indent=2, sort_keys=True) + "\n")

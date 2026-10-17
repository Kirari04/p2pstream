#!/usr/bin/env python3
from __future__ import annotations

import copy
import importlib.util
import json
import pathlib
import sys


if len(sys.argv) != 3:
    raise SystemExit("usage: test-h1-continuation.py VALIDATOR OUTPUT")
validator_path = pathlib.Path(sys.argv[1]).resolve()
output = pathlib.Path(sys.argv[2])
if output.exists():
    raise SystemExit("fresh output required")
spec = importlib.util.spec_from_file_location("h1_continuation", validator_path)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

supervisor = {
    "runner_status": 1,
    "invalidates_success": True,
    "group_joined": True,
    "forced": False,
    "server_scope": {"systemd_run_status": 0, "resource_status": 0, "unit_joined": True,
                     "unit_query_status": 0, "unit_active_state": "inactive"},
    "sender_evidence": {"terminal_status": 0, "terminal_validation_status": 0, "terminal_valid": True},
    "raw_write_evidence": {"finalization_status": 0, "final_closure_valid": True},
    "prospective_terminal_attribution": {"validation_status": 0, "accepted": True},
}
outer = {"all_private": True, "shell_pid_is_one": True, "python_parent_is_one": True,
         "cgroup_namespace_unchanged": True, "owned": {"cgroup": "x"}, "parent": {"cgroup": "x"}}
performance = ["warmup download phase failed independent 29700000 B/s floor"]
attestation = {
    "protocol": "h1", "accepted": False, "continuation_safe": True,
    "integrity_issues": [], "conditioning_issues": [],
    "performance_issues": performance, "validation_issues": performance,
    "natural_endpoint_and_namespace_statuses": True, "outer_private_user_pid_mount_net": True,
    "server_only_delegated_cgroup": True, "outer_runner_group_joined": True,
    "sender_terminal_closure_valid": True, "raw_write_final_closure_valid": True,
    "sender_duration_intervals": [{"sequence": sequence, "valid": True, "compound_tuple_join": True}
                                  for sequence in (9, 11, 14, 15)],
}

# The exact reviewed-106 verdict/record are retained, including a rejecting
# old verdict when the narrow new diagnostic classification accepts.
old = {"accepted": True, "issues": []}
old_record = {"agent_natural_join_status": 0, "terminal_file_status": 0}
attestation.update({"prospective_terminal_attribution_valid": True,
    "raw_write_final_closure": old, "raw_write_final_record": old_record,
    "prospective_terminal_attribution": {"attribution_accepted": True, "issues": [],
        "legacy_final_verdict": copy.deepcopy(old), "legacy_final_record": copy.deepcopy(old_record)}})

cases = []
def check(name, expected, observed):
    accepted = observed.get("accepted") if isinstance(observed, dict) else None
    cases.append({"name": name, "expected": expected, "actual": accepted,
                  "issues": observed.get("issues") if isinstance(observed, dict) else None})
    if type(accepted) is not bool or accepted is not expected:
        raise SystemExit(f"{name}: expected {expected}, got {accepted!r}")

check("slow-valid-h1", True, module.evaluate(supervisor, outer, attestation))
malformed = dict(attestation)
malformed["integrity_issues"] = "not-a-list"
check("malformed-attestation", False, module.evaluate(supervisor, outer, malformed))
forced = dict(supervisor)
forced["forced"] = True
check("forced-cleanup", False, module.evaluate(forced, outer, attestation))
unexpected_status = dict(supervisor)
unexpected_status.update({"runner_status": 37, "invalidates_success": True})
check("unexpected-runner-status", False, module.evaluate(unexpected_status, outer, attestation))
boolean_status = json.loads(json.dumps(supervisor))
boolean_status["sender_evidence"]["terminal_status"] = False
check("boolean-status", False, module.evaluate(boolean_status, outer, attestation))
corrupt = json.loads(json.dumps(attestation))
corrupt["sender_duration_intervals"][2]["compound_tuple_join"] = False
check("corrupt-compound-tuple", False, module.evaluate(supervisor, outer, corrupt))
conditioned = json.loads(json.dumps(attestation))
conditioned.update({"performance_issues": [], "conditioning_issues": ["fully evidenced idle expiry"],
                    "validation_issues": ["fully evidenced idle expiry"]})
check("conditioning-only-h1", True, module.evaluate(supervisor, outer, conditioned))
successful = json.loads(json.dumps(attestation))
successful.update({"accepted": True, "conditioning_issues": [], "performance_issues": [], "validation_issues": []})
check("successful-h1", True, module.evaluate({**supervisor, "runner_status": 0, "invalidates_success": False}, outer, successful))

rejected_supervisor = copy.deepcopy(supervisor)
rejected_supervisor["raw_write_evidence"].update(finalization_status=1, final_closure_valid=False)
rejected_attestation = copy.deepcopy(attestation)
rejected_attestation["raw_write_final_closure"] = {"accepted": False, "issues": ["raw error"]}
rejected_attestation["raw_write_final_closure_valid"] = False
rejected_attestation["prospective_terminal_attribution"]["legacy_final_verdict"] = copy.deepcopy(rejected_attestation["raw_write_final_closure"])
check("preserved-rejecting-reviewed106-verdict", True, module.evaluate(rejected_supervisor, outer, rejected_attestation))
for name, target, key, value in [
    ("old-verdict-mismatch", "proof", "legacy_final_verdict", {"accepted": True, "issues": []}),
    ("old-record-mismatch", "proof", "legacy_final_record", {"agent_natural_join_status": 7, "terminal_file_status": 0}),
    ("attribution-rejection", "proof", "attribution_accepted", False),
    ("attribution-issues", "proof", "issues", ["unknown frame"]),
    ("arbitrary-finalization-status", "raw", "finalization_status", 37),
    ("boolean-finalization-status", "raw", "finalization_status", False),
    ("false-old-closure-label", "raw", "final_closure_valid", True),
    ("prospective-sidecar-error", "prospective", "validation_status", 1),
    ("boolean-prospective-status", "prospective", "validation_status", False),
]:
    sup, att = copy.deepcopy(rejected_supervisor), copy.deepcopy(rejected_attestation)
    destination = {"proof": att["prospective_terminal_attribution"], "raw": sup["raw_write_evidence"],
                   "prospective": sup["prospective_terminal_attribution"]}[target]
    destination[key] = value
    check(name, False, module.evaluate(sup, outer, att))
for field in ("agent_natural_join_status", "terminal_file_status"):
    att = copy.deepcopy(rejected_attestation)
    att["raw_write_final_record"][field] = 1
    att["prospective_terminal_attribution"]["legacy_final_record"][field] = 1
    check(field + "-failure", False, module.evaluate(rejected_supervisor, outer, att))

output.write_text(json.dumps({"schema_version": 1, "accepted": True,
                              "case_count": len(cases), "cases": cases},
                             indent=2, sort_keys=True) + "\n")

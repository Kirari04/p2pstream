#!/usr/bin/env python3
from __future__ import annotations

import json
import pathlib
import sys


def evaluate(supervisor: object, outer: object, attestation: object) -> dict:
    issues: list[str] = []
    if not isinstance(supervisor, dict) or not isinstance(outer, dict) or not isinstance(attestation, dict):
        return {"schema_version": 1, "accepted": False, "issues": ["inputs must be JSON objects"]}

    server_scope = supervisor.get("server_scope")
    sender = supervisor.get("sender_evidence")
    raw = supervisor.get("raw_write_evidence")
    prospective = supervisor.get("prospective_terminal_attribution")
    def status_zero(value: object) -> bool:
        return type(value) is int and value == 0

    if not isinstance(server_scope, dict) or not isinstance(sender, dict) or not isinstance(raw, dict) or not isinstance(prospective, dict):
        issues.append("supervisor evidence objects are missing")
    elif not (
        supervisor.get("group_joined") is True
        and supervisor.get("forced") is False
        and status_zero(server_scope.get("systemd_run_status"))
        and status_zero(server_scope.get("resource_status"))
        and server_scope.get("unit_joined") is True
        and status_zero(server_scope.get("unit_query_status"))
        and server_scope.get("unit_active_state") != "failed"
        and status_zero(sender.get("terminal_status"))
        and status_zero(sender.get("terminal_validation_status"))
        and sender.get("terminal_valid") is True
        and status_zero(prospective.get("validation_status"))
        and prospective.get("accepted") is True
    ):
        issues.append("supervisor topology, resource, sender, raw, or natural-join closure failed")

    parent = outer.get("parent")
    owned = outer.get("owned")
    if not isinstance(parent, dict) or not isinstance(owned, dict) or not (
        outer.get("all_private") is True
        and outer.get("shell_pid_is_one") is True
        and outer.get("python_parent_is_one") is True
        and outer.get("cgroup_namespace_unchanged") is True
        and owned.get("cgroup") == parent.get("cgroup")
    ):
        issues.append("outer namespace or cgroup ownership failed")

    integrity = attestation.get("integrity_issues")
    conditioning = attestation.get("conditioning_issues")
    performance = attestation.get("performance_issues")
    validation = attestation.get("validation_issues")
    partition_ok = (isinstance(integrity, list) and all(isinstance(value, str) for value in integrity)
                    and isinstance(conditioning, list) and all(isinstance(value, str) for value in conditioning)
                    and isinstance(performance, list) and all(isinstance(value, str) for value in performance)
                    and validation == integrity + conditioning + performance
                    and attestation.get("continuation_safe") is (not integrity)
                    and attestation.get("accepted") is (not integrity and not conditioning and not performance))
    if not partition_ok:
        issues.append("attested integrity/performance partition is malformed")
    elif integrity:
        issues.append("H2 attestation contains integrity failures")

    runner_status = supervisor.get("runner_status")
    if (type(runner_status) is not int or runner_status not in (0, 1)
            or (runner_status == 0 and partition_ok and performance)
            or (runner_status == 1 and partition_ok and not (performance or conditioning))):
        issues.append("runner status does not match the attested performance-only outcome")
    expected_invalidation = runner_status != 0 if type(runner_status) is int else None
    if type(supervisor.get("invalidates_success")) is not bool or supervisor.get("invalidates_success") is not expected_invalidation:
        issues.append("supervisor invalidation does not match the exact runner status")

    if not (
        attestation.get("protocol") == "h2"
        and attestation.get("natural_endpoint_and_namespace_statuses") is True
        and attestation.get("outer_private_user_pid_mount_net") is True
        and attestation.get("server_only_delegated_cgroup") is True
        and attestation.get("outer_runner_group_joined") is True
        and attestation.get("sender_terminal_closure_valid") is True
        and attestation.get("prospective_terminal_attribution_valid") is True
    ):
        issues.append("attested H2 data ownership, resource, or closure integrity failed")

    # Preserve the reviewed 106-case verdict and original final record exactly.
    # They are data; only the independently attested prospective predicate may
    # explain its narrow raw closure rejection. Arbitrary endpoint errors fail.
    proof = attestation.get("prospective_terminal_attribution")
    old = attestation.get("raw_write_final_closure")
    old_record = attestation.get("raw_write_final_record")
    if not (isinstance(proof, dict) and isinstance(old, dict) and isinstance(old_record, dict)
            and type(old.get("accepted")) is bool and isinstance(raw, dict)
            and proof.get("attribution_accepted") is True and proof.get("issues") == []
            and proof.get("legacy_final_verdict") == old
            and proof.get("legacy_final_record") == old_record
            and status_zero(old_record.get("agent_natural_join_status"))
            and status_zero(old_record.get("terminal_file_status"))
            and raw.get("final_closure_valid") is old["accepted"]
            and attestation.get("raw_write_final_closure_valid") is old["accepted"]
            and type(raw.get("finalization_status")) is int
            and raw["finalization_status"] == (0 if old["accepted"] else 1)):
        issues.append("prospective H2 attribution and preserved reviewed-106 verdict/record disagree")

    intervals = attestation.get("sender_duration_intervals")
    if not (isinstance(intervals, list) and len(intervals) == 4
            and [row.get("sequence") for row in intervals if isinstance(row, dict)] == [9, 11, 14, 15]
            and all(isinstance(row, dict) and row.get("valid") is True
                    and row.get("compound_tuple_join") is True for row in intervals)):
        issues.append("selected sender intervals or compound tuple joins failed")

    return {
        "schema_version": 1,
        "accepted": not issues,
        "performance_passed": not performance if isinstance(performance, list) else False,
        "conditioning_passed": not conditioning if isinstance(conditioning, list) else False,
        "issues": issues,
    }


def main(argv: list[str]) -> int:
    if len(argv) != 5:
        raise SystemExit("usage: validate-h1-continuation.py SUPERVISOR OUTER ATTESTATION OUTPUT")
    source_paths = [pathlib.Path(value) for value in argv[1:4]]
    output = pathlib.Path(argv[4])
    if output.exists():
        raise SystemExit("fresh output required")
    try:
        values = [json.loads(path.read_text()) for path in source_paths]
        result = evaluate(*values)
    except (OSError, json.JSONDecodeError) as error:
        result = {"schema_version": 1, "accepted": False, "issues": [f"input read failed: {error}"]}
    output.write_text(json.dumps(result, indent=2, sort_keys=True) + "\n")
    return 0 if result["accepted"] else 1


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))

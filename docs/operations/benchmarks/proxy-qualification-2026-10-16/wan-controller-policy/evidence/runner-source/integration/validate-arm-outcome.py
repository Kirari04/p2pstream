#!/usr/bin/env python3
from __future__ import annotations

from typing import Any


EXPECTED_ACCEPTANCE = {
    "correctness", "verified_tls_and_protocol_every_response", "transport_trace_complete",
    "expected_upstream_reuse", "response_copy_attribution_complete", "download_floor_every_phase",
}
INTEGRITY_ACCEPTANCE = EXPECTED_ACCEPTANCE - {"expected_upstream_reuse", "download_floor_every_phase"}


def validate_hash_manifests(pre: object, post: object, size_mode: str,
                            required_hashes: set[str]) -> dict[str, Any]:
    issues: list[str] = []
    if not isinstance(pre, dict) or not isinstance(post, dict):
        return {"issues": ["pre/post hash manifests must be objects"], "hashes": {}}
    if pre != post:
        issues.append("binary, source overlay, certificate, or harness hashes changed during the arm")
    if pre.get("size_mode") != size_mode:
        issues.append("preflight size mode differs from validator mode")
    hashes = pre.get("sha256")
    if not isinstance(hashes, dict):
        issues.append("preflight hash manifest is not an object")
        hashes = {}
    elif (set(hashes) != required_hashes
          or any(type(hashes.get(name)) is not str or len(hashes[name]) != 64
                 or any(character not in "0123456789abcdef" for character in hashes[name])
                 for name in required_hashes)):
        issues.append("preflight hash manifest is incomplete")
    return {"issues": issues, "hashes": hashes}


def classify_summary(size_mode: str, phase_classification: dict[str, Any], summary: object,
                     reuse_integrity_issues: list[str], reuse_conditioning_issues: list[str],
                     floor: int) -> dict[str, Any]:
    integrity: list[str] = []
    performance: list[str] = []
    conditioning: list[str] = []
    small = size_mode == "small"
    stages = ("warmup", "retained-warmup", "steady")
    observed_floor_pass = all(isinstance(phase_classification.get(stage), dict)
                              and phase_classification[stage].get("all_at_or_above_floor") is True
                              for stage in stages)
    floor_gate_pass = small or observed_floor_pass
    if not small:
        for stage in stages:
            row = phase_classification.get(stage)
            if not isinstance(row, dict) or row.get("all_at_or_above_floor") is not True:
                performance.append(f"{stage} download phase failed independent {floor} B/s floor")

    if not isinstance(summary, dict):
        integrity.append("diagnostic summary is not an object")
        acceptance = None
        classification = None
    else:
        acceptance = summary.get("acceptance")
        classification = summary.get("classification")
    reuse_issues = reuse_integrity_issues + reuse_conditioning_issues
    expected_classification = "observational-complete" if not reuse_issues else "inconclusive-upstream-reuse"
    if classification != expected_classification:
        integrity.append(f"diagnostic classification differs from independently classified reuse: {classification!r}")
    if not isinstance(acceptance, dict) or set(acceptance) != EXPECTED_ACCEPTANCE:
        integrity.append(f"diagnostic acceptance is malformed or incomplete: {acceptance!r}")
    else:
        if any(acceptance.get(name) is not True for name in INTEGRITY_ACCEPTANCE):
            integrity.append(f"diagnostic integrity acceptance failed: {acceptance!r}")
        observed_reuse = acceptance.get("expected_upstream_reuse")
        expected_reuse = not reuse_issues
        if type(observed_reuse) is not bool or observed_reuse is not expected_reuse:
            integrity.append(f"diagnostic reuse acceptance differs from independently classified visits: {acceptance!r}")
        observed_floor = acceptance.get("download_floor_every_phase")
        if type(observed_floor) is not bool or observed_floor is not floor_gate_pass:
            integrity.append(f"diagnostic floor acceptance differs from independently computed phases: {acceptance!r}")
    conditioning.extend(reuse_conditioning_issues)
    return {
        "integrity_issues": integrity,
        "conditioning_issues": conditioning,
        "performance_issues": performance,
        "observed_floor_pass": observed_floor_pass,
        "floor_gate_pass": floor_gate_pass,
        "expected_classification": expected_classification,
    }


def classify_runner(supervisor: object, supervisor_joined_ok: bool,
                    integrity_issues: list[str], conditioning_issues: list[str],
                    performance_issues: list[str]) -> dict[str, Any]:
    extra: list[str] = []
    if not isinstance(supervisor, dict):
        return {"integrity_issues": ["supervisor record is not an object"],
                "runner_status_ok": False, "diagnostic_outcome_only_status": False}
    runner_status = supervisor.get("runner_status")
    runner_status_ok = type(runner_status) is int and runner_status == 0
    expected_invalidation = not (runner_status_ok and supervisor_joined_ok)
    if type(supervisor.get("invalidates_success")) is not bool or supervisor.get("invalidates_success") is not expected_invalidation:
        extra.append("supervisor invalidates_success does not match recorded runner/resource/sender statuses")
    diagnostic_only = bool(type(runner_status) is int and runner_status == 1
                           and (performance_issues or conditioning_issues)
                           and not integrity_issues)
    if not runner_status_ok and not diagnostic_only:
        extra.append(f"runner exited nonzero after preserving evidence: {runner_status!r}")
    return {"integrity_issues": extra, "runner_status_ok": runner_status_ok,
            "diagnostic_outcome_only_status": diagnostic_only}


def attestation_outcome(integrity_issues: list[str], conditioning_issues: list[str],
                        performance_issues: list[str]) -> dict[str, Any]:
    """Return the exact acceptance partition embedded in the arm attestation."""
    integrity = list(integrity_issues)
    conditioning = list(conditioning_issues)
    performance = list(performance_issues)
    return {
        "accepted": not integrity and not conditioning and not performance,
        "continuation_safe": not integrity,
        "integrity_issues": integrity,
        "conditioning_issues": conditioning,
        "performance_issues": performance,
        "validation_issues": integrity + conditioning + performance,
    }

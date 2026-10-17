#!/usr/bin/env python3
"""Offline regression controls for retained unavailable history captures."""
from __future__ import annotations

import contextlib
import copy
import hashlib
import importlib.util
import io
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
PACKAGE = ROOT.parent
HISTORY = PACKAGE.parent


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load {name}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


validator = load("failed_capture_tcp_validator", ROOT / "validate-tcp-state.py")
snapshot = load("failed_capture_snapshot_adapter", ROOT / "validate-sender-wait-snapshot.py")


def main():
    inputs = json.loads((PACKAGE / "INPUTS.json").read_text())
    run = HISTORY / "integration-source-2/integration/runs/tiny-threshold-history-smoke-2"
    for relative, expected in inputs["tiny2_fixture_hashes"].items():
        path = run / relative
        if path.is_symlink() or not path.is_file():
            raise RuntimeError(f"fixture is not a regular file: {relative}")
        if hashlib.sha256(path.read_bytes()).hexdigest() != expected:
            raise RuntimeError(f"fixture hash differs: {relative}")

    # Carry the exact unchanged 85-case contract through the corrected validator.
    inherited = load("failed_capture_inherited_contract", ROOT / "contracts/test-tcp-state-validator.py")
    output = io.StringIO()
    with contextlib.redirect_stdout(output):
        inherited_status = inherited.main()
    inherited_result = json.loads(output.getvalue())
    if inherited_status != 0 or inherited_result.get("accepted") is not True:
        raise RuntimeError("inherited 85-case contract failed")
    cases = [(row["name"], row["accepted"]) for row in inherited_result["cases"]]

    controls = {arm: run / arm / "results.sender-wait-controls" for arm in ("h1", "h2")}
    sidecars = {arm: json.loads((directory / "final-raw-write-terminal.json").read_text())
                for arm, directory in controls.items()}
    original_sidecars = copy.deepcopy(sidecars)
    h1 = snapshot.validate_final_closure(sidecars["h1"], controls["h1"])
    cases.append(("failed-capture-h1-full-closure-positive", h1["accepted"] is True))
    h2 = snapshot.validate_final_closure(sidecars["h2"], controls["h2"])
    outside_issue = "raw-write final record 4.raw_write_activity.outside contains error or short raw writes"
    cases.append(("failed-capture-h2-full-closure-preserves-outside-error",
                  h2["accepted"] is False and h2["issues"] == [outside_issue]))
    cases.append(("failed-capture-fixtures-not-mutated", sidecars == original_sidecars))

    def boundary(tcp, raw):
        identity = {key: raw[key] for key in ("lane", "generation", "local_address", "remote_address")}
        return validator.validate_boundary(tcp, terminal=True, terminal_sequence="15", owner_identity=identity)

    for lane in (2, 4):
        raw = sidecars["h2"]["records"][lane]["raw_write_activity"]
        tcp = raw["tcp_state"]
        result = boundary(tcp, raw)
        history = tcp["threshold_history"]
        cases.append((f"failed-capture-lane-{lane}-declared-inconclusive-positive",
                      result["accepted"] is True and history["inconclusive"] is True
                      and history["complete"] is False and history["closed"] is True
                      and history["provider_capture_errors"] == 1))

    raw = sidecars["h2"]["records"][2]["raw_write_activity"]
    base = raw["tcp_state"]

    def reject(name, mutate, issue_text):
        tcp = copy.deepcopy(base)
        mutate(tcp)
        result = boundary(tcp, raw)
        cases.append((name, result["accepted"] is False
                      and any(issue_text in issue for issue in result["issues"])))

    def after(tcp):
        return tcp["threshold_history"]["failure"]["after"]

    reject("failed-capture-undeclared-stage-rejected",
           lambda tcp: after(tcp).__setitem__("failure_stage", ""), "unavailable capture lacks declared failure evidence")
    reject("failed-capture-unknown-stage-rejected",
           lambda tcp: after(tcp).__setitem__("failure_stage", "not_a_provider_stage"), "unavailable capture lacks declared failure evidence")
    reject("failed-capture-no-provider-error-rejected",
           lambda tcp: tcp["threshold_history"].__setitem__("provider_capture_errors", 0), "unavailable capture lacks declared failure evidence")
    reject("failed-capture-no-sticky-inconclusive-rejected",
           lambda tcp: tcp["threshold_history"].__setitem__("inconclusive", False), "healthy history retains provider capture errors")
    reject("failed-capture-no-sticky-reason-rejected",
           lambda tcp: tcp["threshold_history"].__setitem__("failure_reason", ""), "inconclusive state lacks its sticky failure")
    reject("failed-capture-schema-boolean-rejected",
           lambda tcp: after(tcp).__setitem__("schema_version", True), "schema_version is not exact integer one")
    reject("failed-capture-captured-boolean-shape-rejected",
           lambda tcp: after(tcp).__setitem__("captured", "false"), "captured is not an exact boolean")
    reject("failed-capture-forged-healthy-capture-rejected",
           lambda tcp: after(tcp).__setitem__("captured", True), "tcp_info_attempted is not true")
    reject("failed-capture-forged-cc-identity-rejected",
           lambda tcp: after(tcp).__setitem__("congestion_control", "bbr"), "unavailable capture lacks declared failure evidence")
    reject("failed-capture-unattempted-success-rejected",
           lambda tcp: after(tcp).__setitem__("congestion_control_succeeded", True), "unavailable capture lacks declared failure evidence")
    reject("failed-capture-nonzero-tcp-payload-rejected",
           lambda tcp: after(tcp).__setitem__("send_congestion_window", 10), "unavailable capture lacks declared failure evidence")
    reject("failed-capture-false-availability-rejected",
           lambda tcp: after(tcp)["availability"].__setitem__("state", True), "unavailable capture lacks declared failure evidence")
    reject("failed-capture-provider-layout-mismatch-rejected",
           lambda tcp: after(tcp).__setitem__("required_field_end_offset", 1), "unavailable capture lacks declared failure evidence")
    reject("failed-capture-provider-count-conservation-rejected",
           lambda tcp: tcp["threshold_history"].__setitem__("provider_capture_errors", 99999), "provider error/duration counters do not conserve calls")
    reject("failed-capture-genuine-before-cc-change-rejected",
           lambda tcp: tcp["threshold_history"]["failure"]["before"].__setitem__("congestion_control", "bbr"), "failure.before congestion control differs from baseline")
    reject("failed-capture-genuine-last-equal-cc-change-rejected",
           lambda tcp: tcp["threshold_history"]["last_equal"]["after"].__setitem__("congestion_control", "bbr"), "last_equal.after congestion control differs from baseline")

    expected = json.loads((PACKAGE / "EXPECTED-CONTRACT-CASES.json").read_text())
    names = [name for name, _ in cases]
    result = {"schema_version": 1, "accepted": all(ok for _, ok in cases)
              and names == expected["case_names"] and len(cases) == expected["case_count"],
              "case_count": len(cases), "cases": [{"name": name, "accepted": ok} for name, ok in cases]}
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0 if result["accepted"] else 1


if __name__ == "__main__":
    raise SystemExit(main())

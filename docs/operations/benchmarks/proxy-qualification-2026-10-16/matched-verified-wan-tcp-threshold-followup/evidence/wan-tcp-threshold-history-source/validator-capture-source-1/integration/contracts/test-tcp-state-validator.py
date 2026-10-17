#!/usr/bin/env python3
"""Focused source contract for the additive TCP-state validator.

This file is source-only until the producer schema and runner successor are
independently reviewed and frozen.
"""
from __future__ import annotations

import copy
import hashlib
import importlib.util
import json
import pathlib

ROOT = pathlib.Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("tcp_state_validator", ROOT / "validate-tcp-state.py")
if SPEC is None or SPEC.loader is None:
    raise RuntimeError("load TCP-state validator")
validator = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(validator)


def info(base: int, *, congestion: str = "cubic", threshold: int = (1 << 31) - 1) -> dict:
    value = {key: 0 for key in validator.INFO_FIELDS}
    value.update({
        "schema_version": 1,
        "captured": True,
        "capture_started_unix_ns": 1_000_000 + base,
        "capture_finished_unix_ns": 1_000_005 + base,
        "capture_duration_ns": 5,
        "capture_monotonic_valid": True,
        "capture_started_monotonic_ns": base,
        "capture_finished_monotonic_ns": base + 5,
        "tcp_info_attempted": True,
        "tcp_info_succeeded": True,
        "tcp_info_optlen": 280,
        "tcp_info_struct_size": 280,
        "required_field_end_offset": 248,
        "congestion_control_attempted": True,
        "congestion_control_succeeded": True,
        "congestion_control_optlen": len(congestion) + 1,
        "congestion_control": congestion,
        "failure_stage": "",
        "failure_errno": 0,
        "availability": {key: True for key in validator.AVAILABILITY_FIELDS},
        "state": 1,
        "send_slow_start_threshold": threshold,
    })
    return value


def sample(sample_id: int, kind: str, classification: str, observed: int,
           ordinal: int, cumulative: int, capture: int, *, credit: int = 0,
           after_ordinal: int | None = None, after_cumulative: int | None = None) -> dict:
    final_ordinal = ordinal if after_ordinal is None else after_ordinal
    final_cumulative = cumulative if after_cumulative is None else after_cumulative
    return {
        "present": True, "complete": True, "capture_error": False,
        "sample_id": sample_id, "kind": kind, "classification": classification,
        "credit": credit, "observed_unix_ns": 1_000_000 + observed,
        "observed_monotonic_valid": True, "observed_monotonic_ns": observed,
        "cursor_before_ordinal": ordinal, "cursor_before_cumulative": cumulative,
        "cursor_after_valid": True,
        "cursor_after_ordinal": final_ordinal, "cursor_after_cumulative": final_cumulative,
        "cursor_changed": (ordinal, cumulative) != (final_ordinal, final_cumulative),
        "tcp_info": info(capture),
    }


def absent_info() -> dict:
    value = {key: 0 for key in validator.INFO_FIELDS}
    value.update({
        "captured": False,
        "capture_monotonic_valid": False,
        "tcp_info_attempted": False,
        "tcp_info_succeeded": False,
        "congestion_control_attempted": False,
        "congestion_control_succeeded": False,
        "congestion_control": "",
        "failure_stage": "",
        "availability": {key: False for key in validator.AVAILABILITY_FIELDS},
    })
    return value


def absent_history_record() -> dict:
    value = {key: 0 for key in validator.HISTORY_RECORD_FIELDS}
    value.update({
        "present": False, "write_error": False, "short_write": False,
        "envelope_attributed": False, "raw_write_monotonic_valid": False,
        "bracket_valid": False, "transition_observed_across_write": False,
        "local_address": "", "remote_address": "", "transition_location": "",
        "before": absent_info(), "after": absent_info(),
    })
    return value


def history_record(*, ordinal: int = 1, before_threshold: int = 240,
                   after_threshold: int = 240, location: str = "between_bind_and_first_write",
                   attributed: bool = False) -> dict:
    return {
        "present": True, "raw_write_ordinal": ordinal, "owner_lane": 1,
        "owner_generation": 1, "local_address": "127.0.0.1:21001",
        "remote_address": "127.0.0.1:31001", "attempted_bytes": 100,
        "written_bytes": 100, "write_error": False, "short_write": False,
        "envelope_attributed": attributed, "envelope_token": 12 if attributed else 0,
        "stream_id": 7 if attributed else 0,
        "envelope_payload_bytes": 88 if attributed else 0,
        "envelope_wire_bytes": 100 if attributed else 0,
        "raw_write_started_unix_ns": 1_000_026,
        "raw_write_finished_unix_ns": 1_000_027,
        "raw_write_monotonic_valid": True, "raw_write_started_monotonic_ns": 26,
        "raw_write_finished_monotonic_ns": 27, "bracket_valid": True,
        "transition_location": location,
        "transition_observed_across_write": location == "across_write_observation",
        "before": info(20, threshold=before_threshold),
        "after": info(30, threshold=after_threshold),
    }


def threshold_history(pre: dict) -> dict:
    empty = absent_history_record()
    first = history_record()
    return {
        "schema_version": 1, "enabled": True, "required": True,
        "monitoring": False, "complete": False, "transition_observed": True,
        "selected_pre_compared": True, "selected_pre_sequence": "9",
        "selected_pre_arm_ordinal": 1, "selected_pre_initial": False,
        "selected_pre_matched": True, "inconclusive": False,
        "failure_reason": "", "explanation_status": "selected_pre_matched",
        "explanation_available": True, "explanation_inconclusive": False,
        "explanation_reason": "", "pre_bind_unobserved": False, "exhausted": False,
        "active_sample": False, "closed": False,
        "baseline_threshold": (1 << 31) - 1, "first_changed_threshold": 240,
        "selected_pre_threshold": 240, "selected_pre_tcp_info": copy.deepcopy(pre),
        "raw_write_ordinal": 1, "monitored_raw_write_attempts": 1,
        "provider_capture_calls": 2, "provider_capture_errors": 0,
        "provider_capture_duration": {"count": 2, "total_nanoseconds": 10, "max_nanoseconds": 5},
        "raw_provider_calls_shared_with_tcp_interval": 0,
        "raw_provider_duration_shared_with_tcp_interval": {
            "count": 0, "total_nanoseconds": 0, "max_nanoseconds": 0},
        "baseline": info(0), "last_equal": copy.deepcopy(empty),
        "first_different": first, "failure": copy.deepcopy(empty),
    }


def activity() -> dict:
    entry = sample(1, "entry", "pending", 100, 0, 0, 101, credit=0,
                   after_ordinal=1, after_cumulative=65_536)
    retry = sample(2, "retry", "applied_range", 200, 1, 65_536, 201, credit=65_536)
    wait = {
        "record_id": 1, "stream_id": 7, "wait_token": 9,
        "intervening_allocation": False, "pending_raw": False, "claimed": False,
        "raw_pair_record_id": 2, "active": False, "ambiguous": False,
        "entry": entry, "retry": retry,
    }
    pair = {
        "record_id": 2, "wait_record_id": 1, "stream_id": 7, "wait_token": 9,
        "envelope_token": 11, "committed": True, "active": False, "ambiguous": False,
        "before_capture_error": False, "after_capture_error": False, "bracket_valid": True,
        "raw_write_started_unix_ns": 1_000_307, "raw_write_finished_unix_ns": 1_000_310,
        "raw_write_monotonic_valid": True, "raw_write_started_monotonic_ns": 307,
        "raw_write_finished_monotonic_ns": 310, "additional_contained_raw_writes": 0,
        "before": info(301), "after": info(311),
    }
    pre = info(100, threshold=240)
    post = info(400, threshold=240)
    return {
        "schema_version": 1, "enabled": True, "bound": True, "armed": False,
        "sequence": "9", "complete": True, "reset_performed": True, "invalid": False,
        "ambiguous": False, "overflow": False, "tcp_slow_start_after_idle": 1,
        "active_samples": 0, "pending_raw_claims": 0, "active_raw_pairs": 0,
        "dropped_wait_records": 0, "dropped_raw_pair_records": 0,
        "capture_calls": 6, "capture_errors": 0,
        "capture_duration": {"count": 6, "total_nanoseconds": 30, "max_nanoseconds": 5},
        "bind_tcp_info": info(0), "last_live_tcp_info": post,
        "pre_tcp_info": pre, "post_tcp_info": post,
        "wait_records": [wait], "raw_pairs": [pair],
        "threshold_history": threshold_history(pre),
    }


def accepted(value: dict, *, waits: list[dict] | None = None) -> bool:
    selected = waits if waits is not None else [{
        "token": 9, "stream_id": 7, "classification": "applied_range",
        "entry_ordinal": 0, "entry_cumulative": 0, "entry_byte_credit": 0,
        "entry_observed_unix_ns": 1_000_099,
        "retry_ordinal": 1, "retry_cumulative": 65_536, "retry_byte_credit": 65_536,
        "retry_observed_unix_ns": 1_000_199,
    }]
    return validator.validate_live(
        value, sequence=9, selected_stream_id=7, selected_waits=selected,
        owner_identity={"lane": 1, "generation": 1,
                        "local_address": "127.0.0.1:21001",
                        "remote_address": "127.0.0.1:31001"})["accepted"]


def snapshot_adapter_cases() -> list[tuple[str, bool]]:
    """Exercise the complete adapter against hash-bound retained producer envelopes.

    The failed tiny run remains rejected evidence. Its immutable raw responses are
    regression fixtures only; accepting them here makes no performance claim.
    """
    snapshot_spec = importlib.util.spec_from_file_location(
        "snapshot_adapter_contract", ROOT / "validate-sender-wait-snapshot.py")
    if snapshot_spec is None or snapshot_spec.loader is None:
        raise RuntimeError("load sender snapshot adapter")
    snapshot = importlib.util.module_from_spec(snapshot_spec)
    snapshot_spec.loader.exec_module(snapshot)
    history_root = ROOT.parent.parent
    fixture_root = (history_root / "integration-source-1/integration/runs/"
                    "tiny-threshold-history-smoke-1/h1/results.sender-wait-controls")
    fixture_hashes = {
        "sequence-9-pre-sender.json": "4bdd619ecd39bb3bb8fd2034f2a51d4f000f39f52faead7f74c38657129262ac",
        "sequence-9-post-sender.json": "8e8b5b5331bc1b243cfa37f53ae2d688ce88f440d54808679dac6f386b149f8f",
        "sequence-11-pre-sender.json": "020493ede9fc38ba0207e9521c0ec4940b1e10a015d2f9bd3d3051f7e61cf19b",
        "sequence-11-post-sender.json": "c03a8ee9a8bb20df4cfc3e7504ab45e166547fb60808bc81a1573152a210beed",
        "sequence-14-pre-sender.json": "5e09b30134de25366f70ea8f489402171b8d5f8478c17e72b0b71aa3b1ec74ef",
        "sequence-14-post-sender.json": "8e9704f949057370cc5fb0c4c3aec3165c865ebb3be1d90d23d15ef18da8ffe8",
        "sequence-15-pre-sender.json": "ad10ee75fa248a154fc72225f406cefb5e651d2e74c3694161608c1f927bdbf9",
        "sequence-15-post-sender.json": "abfa62a0abbffee77fb3ec93281f9d502a15b4454577a13875b38f6aea13a218",
    }
    fixtures = {}
    for name, expected in fixture_hashes.items():
        path = fixture_root / name
        if path.is_symlink() or not path.is_file():
            raise RuntimeError(f"missing regular snapshot fixture: {name}")
        raw = path.read_bytes()
        if hashlib.sha256(raw).hexdigest() != expected:
            raise RuntimeError(f"snapshot fixture hash mismatch: {name}")
        fixtures[name] = json.loads(raw)
    cases = []
    for sequence in (9, 11, 14, 15):
        pre = fixtures[f"sequence-{sequence}-pre-sender.json"]
        post = fixtures[f"sequence-{sequence}-post-sender.json"]
        cases.append((f"snapshot-pre-generation-match-{sequence}",
                      snapshot.validate_pre(pre, sequence)["accepted"]))
        cases.append((f"snapshot-pair-generation-match-{sequence}",
                      snapshot.validate_pair(pre, post, sequence)["accepted"]))

    pre = fixtures["sequence-9-pre-sender.json"]
    post = fixtures["sequence-9-post-sender.json"]
    selected_lane = snapshot.validate_pair(pre, post, 9)["selected_lane"]
    if type(selected_lane) is not int or selected_lane == 0:
        raise RuntimeError("fixture does not select a DATA lane")

    def reject_mutation(name, mutate, issue_text):
        changed = copy.deepcopy(post)
        row = changed["response"]["send_counters"][selected_lane]
        mutate(row)
        result = snapshot.validate_pair(pre, changed, 9)
        cases.append((name, result["accepted"] is False
                      and any(issue_text in issue for issue in result["issues"])))

    reject_mutation("snapshot-owner-generation-mismatch",
                    lambda row: row["raw_write_activity"]["tcp_state"]["threshold_history"]["last_equal"].__setitem__("owner_generation", 999),
                    "last_equal.owner_generation differs from the enclosing raw owner")
    reject_mutation("snapshot-owner-generation-omitted",
                    lambda row: row["raw_write_activity"].pop("generation"),
                    "owner generation is not positive")
    reject_mutation("snapshot-owner-generation-boolean",
                    lambda row: row["raw_write_activity"].__setitem__("generation", True),
                    "owner generation is not positive")
    reject_mutation("snapshot-raw-owner-malformed",
                    lambda row: row.__setitem__("raw_write_activity", None),
                    "owner generation is not positive")
    reject_mutation("snapshot-selected-data-required-false",
                    lambda row: row["raw_write_activity"]["tcp_state"]["threshold_history"].__setitem__("required", False),
                    ".required differs from the owner lane role")

    control_raw = post["response"]["send_counters"][0]["raw_write_activity"]
    control_identity = {key: control_raw[key] for key in
                        ("lane", "generation", "local_address", "remote_address")}
    control_tcp = control_raw["tcp_state"]
    cases.append(("generic-control-lane-zero-required-false",
                  validator.validate_live(control_tcp, owner_identity=control_identity)["accepted"]))
    wrong_control = copy.deepcopy(control_tcp)
    wrong_control["threshold_history"]["required"] = True
    wrong = validator.validate_live(wrong_control, owner_identity=control_identity)
    cases.append(("generic-control-lane-zero-required-true-rejected",
                  wrong["accepted"] is False and any(
                      ".required differs from the owner lane role" in issue for issue in wrong["issues"])))

    # Omit owner identity only in these direct entrypoint controls so the selected
    # context assertion is isolated from the independent exact owner-lane rule.
    selected_tcp = copy.deepcopy(post["response"]["send_counters"][selected_lane]["raw_write_activity"]["tcp_state"])
    selected_tcp["threshold_history"]["required"] = False
    for label, context in (("sequence", {"sequence": 9}),
                           ("stream", {"selected_stream_id": post["response"]["send_counters"][selected_lane]["timing"]["observed_stream_id"]})):
        result = validator.validate_live(selected_tcp, **context)
        cases.append((f"selected-{label}-requires-data-history",
                      result["accepted"] is False and any(
                          "required is not true for the selected DATA lane" in issue for issue in result["issues"])))
    return cases


def main() -> int:
    base = activity()
    cases: list[tuple[str, bool]] = [("valid", accepted(base))]

    not_reproduced = copy.deepcopy(base)
    initial = (1 << 31) - 1
    not_reproduced["pre_tcp_info"] = info(100, threshold=initial)
    history = not_reproduced["threshold_history"]
    history.update({
        "monitoring": True, "transition_observed": False,
        "selected_pre_initial": True, "selected_pre_matched": False,
        "explanation_status": "not_reproduced", "explanation_available": False,
        "first_changed_threshold": 0, "selected_pre_threshold": initial,
        "selected_pre_tcp_info": copy.deepcopy(not_reproduced["pre_tcp_info"]),
        "raw_write_ordinal": 0, "monitored_raw_write_attempts": 0,
        "provider_capture_calls": 0,
        "provider_capture_duration": {"count": 0, "total_nanoseconds": 0, "max_nanoseconds": 0},
        "first_different": absent_history_record(),
    })
    cases.append(("history-not-reproduced", accepted(not_reproduced)))

    unavailable = copy.deepcopy(base)
    history = unavailable["threshold_history"]
    history.update({
        "monitoring": False, "transition_observed": False,
        "selected_pre_matched": False, "inconclusive": True,
        "failure_reason": "selected_pre_transition_unobserved",
        "explanation_status": "inconclusive", "explanation_available": False,
        "first_changed_threshold": 0, "raw_write_ordinal": 0,
        "monitored_raw_write_attempts": 0, "provider_capture_calls": 0,
        "provider_capture_duration": {"count": 0, "total_nanoseconds": 0, "max_nanoseconds": 0},
        "first_different": absent_history_record(),
    })
    cases.append(("history-finite-pre-transition-unobserved", accepted(unavailable)))

    mismatch = copy.deepcopy(base)
    mismatch["pre_tcp_info"] = info(100, threshold=241)
    history = mismatch["threshold_history"]
    history.update({
        "selected_pre_matched": False, "selected_pre_threshold": 241,
        "selected_pre_tcp_info": copy.deepcopy(mismatch["pre_tcp_info"]),
        "explanation_status": "selected_pre_mismatch", "explanation_available": False,
        "explanation_inconclusive": True, "explanation_reason": "selected_pre_mismatch",
    })
    cases.append(("history-later-transition-mismatch", accepted(mismatch)))

    attributed = copy.deepcopy(base)
    attributed["threshold_history"]["first_different"] = history_record(attributed=True)
    cases.append(("history-selected-data-attribution", accepted(attributed)))
    earlier_stream = copy.deepcopy(attributed)
    earlier_stream["threshold_history"]["first_different"]["stream_id"] = 8
    cases.append(("history-earlier-data-stream-retained", accepted(earlier_stream)))

    attempt_limit = copy.deepcopy(base)
    history = attempt_limit["threshold_history"]
    limit_record = history_record(
        ordinal=4096, before_threshold=(1 << 31) - 1,
        after_threshold=(1 << 31) - 1, location="")
    history.update({
        "monitoring": False, "transition_observed": False,
        "selected_pre_matched": False, "inconclusive": True,
        "failure_reason": "attempt_limit", "explanation_status": "inconclusive",
        "explanation_available": False, "first_changed_threshold": 0,
        "raw_write_ordinal": 4096, "monitored_raw_write_attempts": 4096,
        "provider_capture_calls": 8192, "provider_capture_errors": 0,
        "provider_capture_duration": {
            "count": 8192, "total_nanoseconds": 40960, "max_nanoseconds": 5},
        "exhausted": True, "last_equal": copy.deepcopy(limit_record),
        "first_different": absent_history_record(), "failure": copy.deepcopy(limit_record),
    })
    cases.append(("history-exact-attempt-limit", accepted(attempt_limit)))
    over_attempt_limit = copy.deepcopy(attempt_limit)
    over_attempt_limit["threshold_history"]["raw_write_ordinal"] = 4097
    over_attempt_limit["threshold_history"]["monitored_raw_write_attempts"] = 4097
    over_attempt_limit["threshold_history"]["provider_capture_calls"] = 8194
    over_attempt_limit["threshold_history"]["provider_capture_duration"] = {
        "count": 8194, "total_nanoseconds": 40970, "max_nanoseconds": 5}
    cases.append(("history-over-attempt-limit", not accepted(over_attempt_limit)))
    missing_exhausted = copy.deepcopy(attempt_limit)
    missing_exhausted["threshold_history"]["exhausted"] = False
    cases.append(("history-attempt-limit-status", not accepted(missing_exhausted)))

    invalid_bracket = copy.deepcopy(base)
    invalid_bracket["threshold_history"]["first_different"].update({
        "raw_write_monotonic_valid": False,
        "raw_write_started_monotonic_ns": 0,
        "raw_write_finished_monotonic_ns": 0,
    })
    cases.append(("history-bracket-requires-raw-monotonic", not accepted(invalid_bracket)))

    mismatch_without_transition = copy.deepcopy(mismatch)
    mismatch_without_transition["threshold_history"].update({
        "transition_observed": False, "first_changed_threshold": 0,
        "first_different": absent_history_record(), "monitoring": True,
    })
    cases.append(("history-mismatch-requires-transition", not accepted(mismatch_without_transition)))
    mismatch_without_comparison = copy.deepcopy(mismatch)
    mismatch_without_comparison["threshold_history"].update({
        "selected_pre_compared": False, "selected_pre_sequence": "",
        "selected_pre_arm_ordinal": 0, "selected_pre_threshold": 0,
        "selected_pre_tcp_info": absent_info(),
    })
    cases.append(("history-mismatch-requires-comparison", not accepted(mismatch_without_comparison)))
    mismatch_initial = copy.deepcopy(mismatch)
    mismatch_initial["threshold_history"]["selected_pre_initial"] = True
    cases.append(("history-mismatch-rejects-initial-pre", not accepted(mismatch_initial)))

    stopped_healthy = copy.deepcopy(not_reproduced)
    stopped_healthy["threshold_history"]["monitoring"] = False
    cases.append(("history-live-healthy-monitoring", not accepted(stopped_healthy)))
    healthy_error = copy.deepcopy(base)
    healthy_error["threshold_history"]["provider_capture_errors"] = 1
    cases.append(("history-healthy-provider-errors", not accepted(healthy_error)))
    ordinal_beyond_attempts = copy.deepcopy(base)
    ordinal_beyond_attempts["threshold_history"]["monitored_raw_write_attempts"] = 0
    ordinal_beyond_attempts["threshold_history"]["provider_capture_calls"] = 0
    ordinal_beyond_attempts["threshold_history"]["provider_capture_duration"] = {
        "count": 0, "total_nanoseconds": 0, "max_nanoseconds": 0}
    cases.append(("history-record-ordinal-beyond-attempts", not accepted(ordinal_beyond_attempts)))
    wrong_required = copy.deepcopy(base)
    wrong_required["threshold_history"]["required"] = False
    cases.append(("history-required-owner-lane", not accepted(wrong_required)))
    unresolved_comparison = copy.deepcopy(base)
    unresolved_comparison["threshold_history"].update({
        "selected_pre_matched": False, "explanation_available": False,
        "explanation_status": "transition_observed",
    })
    cases.append(("history-compared-transition-resolves-explanation", not accepted(unresolved_comparison)))
    reverse_prebind = copy.deepcopy(unavailable)
    reverse_prebind["threshold_history"]["failure_reason"] = "pre_bind_unobserved"
    cases.append(("history-prebind-reason-requires-marker", not accepted(reverse_prebind)))
    unresolved_no_transition = copy.deepcopy(not_reproduced)
    unresolved_no_transition["threshold_history"].update({
        "selected_pre_initial": False, "explanation_status": "monitoring",
    })
    cases.append(("history-no-transition-comparison-is-initial", not accepted(unresolved_no_transition)))
    counter_overflow = copy.deepcopy(base)
    overflow_history = counter_overflow["threshold_history"]
    overflow_record = history_record()
    overflow_history.update({
        "monitoring": False, "transition_observed": False,
        "selected_pre_matched": False, "inconclusive": True,
        "failure_reason": "counter_overflow", "explanation_status": "inconclusive",
        "explanation_available": False, "first_changed_threshold": 0,
        "exhausted": True, "last_equal": absent_history_record(),
        "first_different": absent_history_record(), "failure": overflow_record,
    })
    cases.append(("history-counter-overflow-exhausted", accepted(counter_overflow)))
    finite_prebind = copy.deepcopy(unavailable)
    finite_prebind["bind_tcp_info"] = info(0, threshold=500)
    finite_history = finite_prebind["threshold_history"]
    finite_history.update({
        "baseline": copy.deepcopy(finite_prebind["bind_tcp_info"]),
        "baseline_threshold": 500, "pre_bind_unobserved": True,
        "failure_reason": "pre_bind_unobserved",
    })
    cases.append(("history-finite-prebind-gap", accepted(finite_prebind)))
    fabricated_prebind = copy.deepcopy(unavailable)
    fabricated_prebind["threshold_history"].update({
        "pre_bind_unobserved": True, "failure_reason": "pre_bind_unobserved",
    })
    cases.append(("history-initial-bind-rejects-prebind-gap", not accepted(fabricated_prebind)))
    missing_prebind = copy.deepcopy(finite_prebind)
    missing_prebind["threshold_history"].update({
        "pre_bind_unobserved": False,
        "failure_reason": "selected_pre_transition_unobserved",
    })
    cases.append(("history-finite-bind-requires-prebind-gap", not accepted(missing_prebind)))
    located_invalid = copy.deepcopy(unavailable)
    located_record = history_record()
    located_record.update({
        "bracket_valid": False, "transition_location": "between_bind_and_first_write",
        "transition_observed_across_write": False,
    })
    located_invalid["threshold_history"].update({
        "failure_reason": "capture_or_bracket_invalid", "raw_write_ordinal": 1,
        "monitored_raw_write_attempts": 1, "provider_capture_calls": 2,
        "provider_capture_duration": {"count": 2, "total_nanoseconds": 10, "max_nanoseconds": 5},
        "failure": located_record,
    })
    cases.append(("history-invalid-bracket-rejects-location", not accepted(located_invalid)))

    progression_pre = copy.deepcopy(base)
    progression_pre["threshold_history"].update({
        "monitoring": True, "transition_observed": False,
        "selected_pre_compared": False, "selected_pre_sequence": "",
        "selected_pre_arm_ordinal": 0, "selected_pre_matched": False,
        "explanation_status": "monitoring", "explanation_available": False,
        "first_changed_threshold": 0, "selected_pre_threshold": 0,
        "selected_pre_tcp_info": absent_info(), "raw_write_ordinal": 0,
        "monitored_raw_write_attempts": 0, "provider_capture_calls": 0,
        "provider_capture_duration": {"count": 0, "total_nanoseconds": 0, "max_nanoseconds": 0},
        "first_different": absent_history_record(),
    })
    progression = validator.validate_threshold_history_progression(
        progression_pre, base, sequence=9, path="history progression")
    cases.append(("history-persistent-progression", progression["accepted"]))
    wrong_progression = copy.deepcopy(base)
    wrong_progression["threshold_history"]["selected_pre_arm_ordinal"] = 2
    cases.append(("history-arm-ordinal-jump", not validator.validate_threshold_history_progression(
        progression_pre, wrong_progression, sequence=9, path="history progression")["accepted"]))
    overwritten = copy.deepcopy(base)
    retained_pre = copy.deepcopy(base)
    overwritten["threshold_history"]["selected_pre_arm_ordinal"] = 2
    overwritten["threshold_history"]["first_different"]["raw_write_ordinal"] = 2
    cases.append(("history-first-transition-overwrite", not validator.validate_threshold_history_progression(
        retained_pre, overwritten, sequence=9, path="history progression")["accepted"]))
    continued_attempts = copy.deepcopy(base)
    continued_attempts["threshold_history"]["selected_pre_arm_ordinal"] = 2
    continued_attempts["threshold_history"]["raw_write_ordinal"] = 2
    continued_attempts["threshold_history"]["monitored_raw_write_attempts"] = 2
    continued_attempts["threshold_history"]["provider_capture_calls"] = 4
    continued_attempts["threshold_history"]["provider_capture_duration"] = {
        "count": 4, "total_nanoseconds": 20, "max_nanoseconds": 5}
    cases.append(("history-retained-transition-freezes-provider-work", not validator.validate_threshold_history_progression(
        retained_pre, continued_attempts, sequence=9, path="history progression")["accepted"]))
    continued_shared = copy.deepcopy(base)
    continued_shared["threshold_history"]["selected_pre_arm_ordinal"] = 2
    continued_shared["threshold_history"]["raw_provider_calls_shared_with_tcp_interval"] = 1
    continued_shared["threshold_history"]["raw_provider_duration_shared_with_tcp_interval"] = {
        "count": 1, "total_nanoseconds": 5, "max_nanoseconds": 5}
    cases.append(("history-retained-transition-freezes-shared-work", not validator.validate_threshold_history_progression(
        retained_pre, continued_shared, sequence=9, path="history progression")["accepted"]))
    sticky_post = copy.deepcopy(attempt_limit)
    sticky_post["threshold_history"]["selected_pre_arm_ordinal"] = 2
    sticky_post["threshold_history"]["raw_write_ordinal"] = 4097
    sticky_post["threshold_history"]["provider_capture_errors"] = 1
    cases.append(("history-sticky-failure-freezes-provider-work", not validator.validate_threshold_history_progression(
        attempt_limit, sticky_post, sequence=9, path="history progression")["accepted"]))
    equal_pre = copy.deepcopy(progression_pre)
    equal_pre_history = equal_pre["threshold_history"]
    equal_pre_history.update({
        "raw_write_ordinal": 1, "monitored_raw_write_attempts": 1,
        "provider_capture_calls": 2,
        "provider_capture_duration": {"count": 2, "total_nanoseconds": 10, "max_nanoseconds": 5},
        "last_equal": history_record(
            ordinal=1, before_threshold=(1 << 31) - 1,
            after_threshold=(1 << 31) - 1, location=""),
    })
    transition_post = copy.deepcopy(base)
    transition_post["threshold_history"].update({
        "raw_write_ordinal": 2, "monitored_raw_write_attempts": 2,
        "provider_capture_calls": 4,
        "provider_capture_duration": {"count": 4, "total_nanoseconds": 20, "max_nanoseconds": 5},
        "first_different": history_record(ordinal=2),
        "last_equal": absent_history_record(),
    })
    cases.append(("history-transition-preserves-last-equal", not validator.validate_threshold_history_progression(
        equal_pre, transition_post, sequence=9, path="history progression")["accepted"]))
    sticky_mutation = copy.deepcopy(attempt_limit)
    sticky_mutation["threshold_history"].update({
        "selected_pre_arm_ordinal": 2, "transition_observed": True,
        "first_changed_threshold": 240, "first_different": history_record(),
    })
    cases.append(("history-sticky-failure-freezes-history", not validator.validate_threshold_history_progression(
        attempt_limit, sticky_mutation, sequence=9, path="history progression")["accepted"]))

    boundary = copy.deepcopy(base)
    boundary.update({
        "sequence": "", "capture_calls": 0,
        "capture_duration": {"count": 0, "total_nanoseconds": 0, "max_nanoseconds": 0},
        "pre_tcp_info": absent_info(), "post_tcp_info": absent_info(),
        "wait_records": [], "raw_pairs": [],
    })
    cases.append(("exact-zero-boundary", validator.validate_boundary(boundary)["accepted"]))

    terminal = copy.deepcopy(boundary)
    terminal["threshold_history"].update({
        "closed": True, "complete": True, "selected_pre_sequence": "15",
    })
    cases.append(("terminal-retains-sequence-15", validator.validate_boundary(
        terminal, terminal=True, owner_identity={
            "lane": 1, "generation": 1, "local_address": "127.0.0.1:21001",
            "remote_address": "127.0.0.1:31001"})["accepted"]))
    terminal_owner_mismatch = copy.deepcopy(terminal)
    terminal_owner_mismatch["threshold_history"]["first_different"]["owner_generation"] = 2
    cases.append(("terminal-history-owner-mismatch", not validator.validate_boundary(
        terminal_owner_mismatch, terminal=True, owner_identity={
            "lane": 1, "generation": 1, "local_address": "127.0.0.1:21001",
            "remote_address": "127.0.0.1:31001"})["accepted"]))

    interleaved = copy.deepcopy(base)
    second = {
        "record_id": 3, "stream_id": 8, "wait_token": 10,
        "intervening_allocation": False, "pending_raw": False, "claimed": False,
        "raw_pair_record_id": 0, "active": False, "ambiguous": False,
        "entry": sample(2, "entry", "pending", 120, 0, 0, 121),
        "retry": sample(3, "retry", "stale", 180, 0, 0, 181),
    }
    interleaved["wait_records"][0]["entry"]["sample_id"] = 1
    interleaved["wait_records"][0]["retry"]["sample_id"] = 4
    interleaved["wait_records"].append(second)
    interleaved["capture_calls"] = 8
    interleaved["capture_duration"] = {"count": 8, "total_nanoseconds": 40, "max_nanoseconds": 5}
    cases.append(("interleaved-stream-sample-ids", validator.validate_activity(interleaved)["accepted"]))

    def rejected(name: str, mutate) -> None:
        value = copy.deepcopy(base)
        mutate(value)
        cases.append((name, not accepted(value)))

    rejected("boolean-schema", lambda value: value.__setitem__("schema_version", True))
    rejected("invalid-sticky", lambda value: value.__setitem__("invalid", True))
    rejected("overflow-sticky", lambda value: value.__setitem__("overflow", True))
    rejected("record-cap", lambda value: value.__setitem__("wait_records", value["wait_records"] * 4097))
    rejected("capture-count", lambda value: value.__setitem__("capture_calls", 5))
    rejected("unsupported-field", lambda value: value["pre_tcp_info"]["availability"].__setitem__("total_rto_time", False))
    rejected("cursor-change-marker", lambda value: value["wait_records"][0]["entry"].__setitem__("cursor_changed", False))
    rejected("cursor-after-invalid", lambda value: value["wait_records"][0]["entry"].__setitem__("cursor_after_valid", False))
    rejected("sample-monotonic", lambda value: value["wait_records"][0]["retry"].__setitem__("observed_monotonic_ns", 50))
    rejected("pair-bracket", lambda value: value["raw_pairs"][0].__setitem__("raw_write_started_monotonic_ns", 300))
    rejected("pair-reverse-link", lambda value: value["raw_pairs"][0].__setitem__("wait_record_id", 2))
    rejected("congestion-change", lambda value: value["post_tcp_info"].__setitem__("congestion_control", "bbr"))
    rejected("entry-congestion-change", lambda value: value["wait_records"][0]["entry"]["tcp_info"].__setitem__("congestion_control", "bbr"))
    rejected("retry-congestion-change", lambda value: value["wait_records"][0]["retry"]["tcp_info"].__setitem__("congestion_control", "bbr"))
    rejected("raw-before-congestion-change", lambda value: value["raw_pairs"][0]["before"].__setitem__("congestion_control", "bbr"))
    rejected("raw-after-congestion-change", lambda value: value["raw_pairs"][0]["after"].__setitem__("congestion_control", "bbr"))
    rejected("last-live-mismatch", lambda value: value.__setitem__("last_live_tcp_info", info(500)))
    rejected("applied-retry-zero-credit", lambda value: value["wait_records"][0]["retry"].__setitem__("credit", 0))
    zero_byte_wait = [{
        "token": 9, "stream_id": 7, "classification": "applied_range",
        "entry_ordinal": 0, "entry_cumulative": 0, "entry_byte_credit": 0,
        "entry_observed_unix_ns": 1_000_099,
        "retry_ordinal": 1, "retry_cumulative": 65_536, "retry_byte_credit": 0,
        "retry_observed_unix_ns": 1_000_199,
    }]
    cases.append(("byte-credit-applied-zero-credit", not accepted(base, waits=zero_byte_wait)))
    rejected("sysctl-domain", lambda value: value.__setitem__("tcp_slow_start_after_idle", 2))
    rejected("history-boolean-schema", lambda value: value["threshold_history"].__setitem__("schema_version", True))
    rejected("history-selected-sequence", lambda value: value["threshold_history"].__setitem__("selected_pre_sequence", "11"))
    rejected("history-selected-pre-snapshot", lambda value: value["threshold_history"]["selected_pre_tcp_info"].__setitem__("send_slow_start_threshold", 241))
    rejected("history-transition-chronology", lambda value: value["threshold_history"]["selected_pre_tcp_info"].__setitem__("capture_started_monotonic_ns", 24))
    rejected("history-provider-count", lambda value: value["threshold_history"].__setitem__("provider_capture_calls", 1))
    rejected("history-shared-provider-count", lambda value: value["threshold_history"].__setitem__("raw_provider_calls_shared_with_tcp_interval", 3))
    rejected("history-owner-generation", lambda value: value["threshold_history"]["first_different"].__setitem__("owner_generation", 2))
    malformed_record = copy.deepcopy(base)
    malformed_record["threshold_history"]["first_different"] = None
    cases.append(("history-malformed-record-shape", not accepted(malformed_record)))
    rejected("history-last-equal-order", lambda value: value["threshold_history"].update({
        "last_equal": history_record(ordinal=1, before_threshold=(1 << 31) - 1,
                                     after_threshold=(1 << 31) - 1, location=""),
        "first_different": history_record(ordinal=1),
    }))
    wrong_wait = accepted(base, waits=[{"token": 10, "stream_id": 7, "classification": "applied_range"}])
    cases.append(("byte-credit-wait-join", not wrong_wait))
    wrong_stream = validator.validate_live(base, sequence=9, selected_stream_id=8)["accepted"]
    cases.append(("selected-stream-join", not wrong_stream))

    cases.extend(snapshot_adapter_cases())
    result = {
        "schema_version": 1,
        "case_count": len(cases),
        "cases": [{"name": name, "accepted": ok} for name, ok in cases],
        "accepted": all(ok for _, ok in cases),
    }
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0 if result["accepted"] else 1


if __name__ == "__main__":
    raise SystemExit(main())

#!/usr/bin/env python3
"""Fail-closed validator for authenticated schema-2 sender snapshots."""

import importlib.util
import json
from pathlib import Path
from typing import Any

raw_validator_path = Path(__file__).with_name("validate-raw-write-activity.py")
raw_spec = importlib.util.spec_from_file_location("raw_write_activity_validator", raw_validator_path)
raw_write_activity_validator = importlib.util.module_from_spec(raw_spec)
raw_spec.loader.exec_module(raw_write_activity_validator)

BULK_LANES = (1, 2, 3, 4)
ALL_LANES = (0, 1, 2, 3, 4)
FIXED_SEQUENCES = (9, 11, 14, 15)
COUNTER_SCALARS = (
    "reserved_data_frames", "reserved_payload_bytes", "reserved_allocation_charged_bytes",
    "byte_credit_zero_observations", "stream_allocation_zero_observations",
    "session_allocation_zero_observations",
)
TIMING_BOOLS = (
    "complete", "reset_performed", "ambiguous", "overflow", "stream_observed",
    "multiple_streams", "pending_credit_decision", "send_loop_terminated",
)
TIMING_INTS = (
    "observed_stream_id", "first_wait_token", "last_wait_token",
    "active_credit_waits", "active_reserved_frames", "active_reserved_payload_bytes",
    "active_reserved_allocation_charged_bytes", "active_caller_frames",
    "active_caller_payload_bytes", "active_enqueue_frames", "active_enqueue_payload_bytes",
    "active_queued_frames", "active_queued_payload_bytes", "claimed_before_publication_frames",
    "claimed_before_publication_bytes", "enqueue_publication_races", "active_physical_frames",
    "active_physical_payload_bytes", "data_enqueued_frames", "data_enqueued_payload_bytes",
    "data_completed_frames", "data_completed_payload_bytes", "data_gather_error_frames",
    "data_gather_error_payload_bytes", "data_abandoned_frames", "data_abandoned_payload_bytes",
    "physical_attempt_frames", "physical_attempt_payload_bytes", "physical_attempt_wire_bytes",
    "physical_written_wire_bytes", "physical_success_frames", "physical_success_payload_bytes",
    "physical_success_wire_bytes", "physical_error_frames", "physical_error_payload_bytes",
    "physical_error_attempt_wire_bytes", "physical_error_written_wire_bytes",
    "physical_short_frames", "physical_short_payload_bytes", "physical_short_attempt_wire_bytes",
    "physical_short_written_wire_bytes",
)
ACTIVE_INTS = (
    "active_credit_waits", "active_reserved_frames", "active_reserved_payload_bytes",
    "active_reserved_allocation_charged_bytes", "active_caller_frames",
    "active_caller_payload_bytes", "active_enqueue_frames", "active_enqueue_payload_bytes",
    "active_queued_frames", "active_queued_payload_bytes", "claimed_before_publication_frames",
    "claimed_before_publication_bytes", "active_physical_frames", "active_physical_payload_bytes",
)
BAD_INTS = (
    "data_gather_error_frames", "data_gather_error_payload_bytes", "data_abandoned_frames",
    "data_abandoned_payload_bytes", "physical_error_frames", "physical_error_payload_bytes",
    "physical_error_attempt_wire_bytes", "physical_error_written_wire_bytes",
    "physical_short_frames", "physical_short_payload_bytes", "physical_short_attempt_wire_bytes",
    "physical_short_written_wire_bytes",
)
DURATION_TOTALS = (
    "credit_wait_total", "data_enqueue_select_total", "data_completion_select_total",
    "data_call_total", "physical_write_total",
)
OUTCOME_GROUPS = {
    "credit_wait_by_cause": ("byte_zero", "stream_allocation_zero", "session_allocation_zero", "stream_and_session_allocation_zero"),
    "credit_wait_by_outcome": ("byte_notify", "aggregate_notify", "shutdown", "timeout"),
    "data_enqueue_select_by_outcome": ("accepted", "shutdown", "timeout"),
    "data_completion_select_by_outcome": ("success", "error", "shutdown", "timeout"),
    "data_call_by_outcome": ("success", "error", "shutdown", "timeout"),
    "physical_write_by_outcome": ("success", "error", "short_write"),
}
ZERO_BUCKET = {"count": 0, "total_nanoseconds": 0, "max_nanoseconds": 0}
UINT32_MAX = (1 << 32) - 1
UINT64_MAX = (1 << 64) - 1
MAX_RESPONSE_BYTES = 4 * 1024 * 1024


def exact_int(value: Any) -> bool:
    return type(value) is int and 0 <= value <= UINT64_MAX


def exact_bool(value: Any) -> bool:
    return type(value) is bool


def bucket(value: Any, path: str, issues: list[str]) -> dict[str, int]:
    if type(value) is not dict:
        issues.append(f"{path} is not an object")
        value = {}
    result: dict[str, int] = {}
    for key in ("count", "total_nanoseconds", "max_nanoseconds"):
        current = value.get(key)
        if not exact_int(current):
            issues.append(f"{path}.{key} is not a nonnegative exact integer")
            current = 0
        result[key] = current
    if set(value) != {"count", "total_nanoseconds", "max_nanoseconds"}:
        issues.append(f"{path} fields do not match the fixed bucket schema")
    if result["max_nanoseconds"] > result["total_nanoseconds"]:
        issues.append(f"{path} maximum exceeds total")
    if result["count"] > 0 and result["total_nanoseconds"] > result["count"] * result["max_nanoseconds"]:
        issues.append(f"{path} total exceeds count times maximum")
    if result["count"] == 0 and (result["total_nanoseconds"] != 0 or result["max_nanoseconds"] != 0):
        issues.append(f"{path} has duration without calls")
    return result


def validate_timing(value: Any, path: str, issues: list[str]) -> dict[str, Any]:
    if type(value) is not dict:
        issues.append(f"{path} is not an object")
        value = {}
    bools: dict[str, bool] = {}
    ints: dict[str, int] = {}
    durations: dict[str, dict[str, int]] = {}
    groups: dict[str, dict[str, dict[str, int]]] = {}
    for key in TIMING_BOOLS:
        current = value.get(key)
        if not exact_bool(current):
            issues.append(f"{path}.{key} is not an exact boolean")
            current = False
        bools[key] = current
    for key in TIMING_INTS:
        current = value.get(key)
        if not exact_int(current):
            issues.append(f"{path}.{key} is not a nonnegative exact integer")
            current = 0
        ints[key] = current
    for key in DURATION_TOTALS:
        durations[key] = bucket(value.get(key), f"{path}.{key}", issues)
    for group, members in OUTCOME_GROUPS.items():
        container = value.get(group)
        if type(container) is not dict:
            issues.append(f"{path}.{group} is not an object")
            container = {}
        if set(container) != set(members):
            issues.append(f"{path}.{group} fields do not match the fixed outcome schema")
        groups[group] = {member: bucket(container.get(member), f"{path}.{group}.{member}", issues) for member in members}
    expected = set(TIMING_BOOLS) | set(TIMING_INTS) | set(DURATION_TOTALS) | set(OUTCOME_GROUPS)
    if set(value) != expected:
        issues.append(f"{path} fields do not match the fixed timing schema")
    conservation = (
        ("credit_wait_total", "credit_wait_by_cause"),
        ("credit_wait_total", "credit_wait_by_outcome"),
        ("data_enqueue_select_total", "data_enqueue_select_by_outcome"),
        ("data_completion_select_total", "data_completion_select_by_outcome"),
        ("data_call_total", "data_call_by_outcome"),
        ("physical_write_total", "physical_write_by_outcome"),
    )
    for total_name, group_name in conservation:
        total = durations[total_name]
        members = groups[group_name].values()
        if (total["count"], total["total_nanoseconds"]) != (
            sum(item["count"] for item in members), sum(item["total_nanoseconds"] for item in members)
        ):
            issues.append(f"{path}.{total_name} does not conserve through {group_name}")
        if total["max_nanoseconds"] != max((item["max_nanoseconds"] for item in groups[group_name].values()), default=0):
            issues.append(f"{path}.{total_name} maximum does not conserve through {group_name}")
    return {"bools": bools, "ints": ints, "durations": durations, "groups": groups}


def response(envelope: Any, sequence: int, action: str, issues: list[str]) -> dict[str, Any]:
    path = f"{action} envelope"
    if type(envelope) is not dict:
        issues.append(f"{path} is not an object")
        return {}
    expected_envelope_fields = {
        "schema_version", "role", "action", "sequence", "request_started_unix_ns",
        "request_finished_unix_ns", "request_elapsed_ns", "http_status", "response_bytes", "response", "error", "authorization",
    }
    if set(envelope) != expected_envelope_fields:
        issues.append(f"{path} fields do not match the bounded response envelope schema")
    for key in ("schema_version", "sequence", "http_status", "response_bytes", "request_started_unix_ns", "request_finished_unix_ns", "request_elapsed_ns"):
        if not exact_int(envelope.get(key)):
            issues.append(f"{path} {key} invalid")
    if envelope.get("schema_version") != 1 or envelope.get("sequence") != sequence:
        issues.append(f"{path} identity mismatch")
    if envelope.get("role") != "agent-drain" or envelope.get("action") != action:
        issues.append(f"{path} role or action mismatch")
    if envelope.get("authorization") != "bearer-present":
        issues.append(f"{path} authorization provenance mismatch")
    if envelope.get("http_status") != 200 or envelope.get("error") != "":
        issues.append(f"{path} request failed")
    started = envelope.get("request_started_unix_ns")
    finished = envelope.get("request_finished_unix_ns")
    elapsed = envelope.get("request_elapsed_ns")
    response_bytes = envelope.get("response_bytes")
    if not exact_int(started) or started <= 0:
        issues.append(f"{path} request start timestamp is not positive")
    if not exact_int(response_bytes) or not (0 < response_bytes <= MAX_RESPONSE_BYTES):
        issues.append(f"{path} response byte count is outside the fixed 4 MiB bound")
    if not exact_int(finished) or finished < started:
        issues.append(f"{path} request finish timestamp precedes its start")
    value = envelope.get("response")
    if type(value) is not dict:
        issues.append(f"{path} response is not an object")
        return {}
    if set(value) != {"schema_version", "captured_unix_nano", "capacity", "dropped_events", "events", "send_counters", "byte_credit_traces"}:
        issues.append(f"{path} response fields do not match schema 2")
    if value.get("schema_version") != 2 or value.get("capacity") != 4096:
        issues.append(f"{path} schema or fixed capacity mismatch")
    if not exact_int(value.get("captured_unix_nano")) or value.get("captured_unix_nano", 0) <= 0:
        issues.append(f"{path} capture timestamp invalid")
    captured = value.get("captured_unix_nano")
    if exact_int(started) and exact_int(finished) and exact_int(captured) and not (started <= captured <= finished):
        issues.append(f"{path} capture timestamp is outside the request interval")
    events = value.get("events")
    if type(events) is not list or len(events) > 4096:
        issues.append(f"{path} receive-growth events missing or over bound")
    if not exact_int(value.get("dropped_events")):
        issues.append(f"{path} dropped-event count invalid")
    byte_credit_traces = value.get("byte_credit_traces")
    if type(byte_credit_traces) is not list or len(byte_credit_traces) != 5:
        issues.append(f"{path} byte-credit trace session cardinality is not five")
    copied = dict(value)
    copied["_envelope_meta"] = {"request_started_unix_ns": started,
                                "request_finished_unix_ns": finished,
                                "request_elapsed_ns": elapsed,
                                "captured_unix_nano": captured}
    return copied


def counters(value: dict[str, Any], action: str, issues: list[str]) -> dict[int, dict[str, Any]]:
    rows = value.get("send_counters")
    if type(rows) is not list or len(rows) != 5:
        issues.append(f"{action} sender counter cardinality is not five")
        return {}
    result: dict[int, dict[str, Any]] = {}
    for index, row in enumerate(rows):
        path = f"{action} counter {index}"
        if type(row) is not dict:
            issues.append(f"{path} is not an object")
            continue
        expected_row_fields = {
            "lane", "session_identity", "local_address", "remote_address", "enabled", "timing",
            "raw_write_activity", *COUNTER_SCALARS,
        }
        if set(row) != expected_row_fields:
            issues.append(f"{path} fields do not match the fixed sender-counter schema")
        lane = row.get("lane")
        if type(lane) is not int or lane not in ALL_LANES or lane in result:
            issues.append(f"{path} lane invalid or repeated: {lane!r}")
            continue
        if not exact_bool(row.get("enabled")):
            issues.append(f"{action} lane {lane} enabled is not an exact boolean")
        for key in ("session_identity", "local_address", "remote_address"):
            if type(row.get(key)) is not str or not row[key]:
                issues.append(f"{action} lane {lane} {key} missing")
        for key in COUNTER_SCALARS:
            if not exact_int(row.get(key)):
                issues.append(f"{action} lane {lane} {key} invalid")
        timing_issues: list[str] = []
        normalized = validate_timing(row.get("timing"), f"{action} lane {lane}.timing", timing_issues)
        issues.extend(timing_issues)
        raw_activity = row.get("raw_write_activity")
        raw_generation = raw_activity.get("generation") if type(raw_activity) is dict else None
        raw_identity = {"lane": lane, "generation": raw_generation,
                        "local_address": row.get("local_address"), "remote_address": row.get("remote_address")}
        raw_result = raw_write_activity_validator.validate_live(
            row.get("raw_write_activity"), raw_identity, action)
        issues.extend(raw_result["issues"])
        validated_row = dict(row)
        validated_row["_validated_timing"] = normalized
        validated_row["_validated_raw_write_activity"] = raw_result
        result[lane] = validated_row
    if tuple(sorted(result)) != ALL_LANES:
        issues.append(f"{action} physical lane set mismatch")
    return result


def require_zero_activity(row: dict[str, Any], path: str, issues: list[str]) -> None:
    timing = row.get("_validated_timing", {"bools": {}, "ints": {}, "durations": {}, "groups": {}})
    for key in COUNTER_SCALARS:
        if row.get(key) != 0:
            issues.append(f"{path}.{key} is nonzero")
    for key, value in timing["ints"].items():
        if value != 0:
            issues.append(f"{path}.timing.{key} is nonzero")
    for key, value in timing["durations"].items():
        if value != ZERO_BUCKET:
            issues.append(f"{path}.timing.{key} is nonzero")
    for group, members in timing["groups"].items():
        for key, value in members.items():
            if value != ZERO_BUCKET:
                issues.append(f"{path}.timing.{group}.{key} is nonzero")
    for key in ("ambiguous", "overflow", "stream_observed", "multiple_streams", "pending_credit_decision", "send_loop_terminated"):
        if timing["bools"].get(key) is not False:
            issues.append(f"{path}.timing.{key} is not false")


def require_bulk_boundary(row: dict[str, Any], path: str, issues: list[str]) -> None:
    timing = row.get("_validated_timing", {"bools": {}, "ints": {}})
    if row.get("enabled") is not True:
        issues.append(f"{path} observer disabled")
    if timing["bools"].get("complete") is not True or timing["bools"].get("reset_performed") is not True:
        issues.append(f"{path} did not complete and reset")
    if timing["bools"].get("pending_credit_decision") is not False:
        issues.append(f"{path} has a pending credit decision")
    if timing["bools"].get("send_loop_terminated") is not False:
        issues.append(f"{path} send loop terminated")
    for key in ACTIVE_INTS:
        if timing["ints"].get(key) != 0:
            issues.append(f"{path} active ownership {key} is nonzero")


def validate_pre(envelope: Any, sequence: int) -> dict[str, Any]:
    issues: list[str] = []
    value = response(envelope, sequence, "pre", issues)
    rows = counters(value, "pre", issues)
    if 0 in rows:
        if rows[0].get("enabled") is not False:
            issues.append("pre control-lane sender observer unexpectedly enabled")
        require_zero_activity(rows[0], "pre control lane", issues)
        issues.extend(raw_write_activity_validator.validate_no_data_activity(
            rows[0].get("raw_write_activity"), "pre control lane raw-write activity"))
    for lane in BULK_LANES:
        if lane in rows:
            require_bulk_boundary(rows[lane], f"pre lane {lane}", issues)
    return {"schema_version": 1, "mode": "pre", "sequence": sequence,
            "measurement_start_valid": not issues, "issues": issues, "accepted": not issues}


def validate_terminal(envelope: Any, sequence: int) -> dict[str, Any]:
    issues: list[str] = []
    value = response(envelope, sequence, "terminal", issues)
    rows = counters(value, "terminal", issues)
    if 0 in rows:
        if rows[0].get("enabled") is not False:
            issues.append("terminal control-lane sender observer unexpectedly enabled")
        require_zero_activity(rows[0], "terminal control lane", issues)
        issues.extend(raw_write_activity_validator.validate_no_data_activity(
            rows[0].get("raw_write_activity"), "terminal control lane raw-write activity"))
    for lane in BULK_LANES:
        if lane in rows:
            require_bulk_boundary(rows[lane], f"terminal lane {lane}", issues)
    return {"schema_version": 1, "mode": "terminal", "sequence": sequence,
            "closure_valid": not issues, "issues": issues, "accepted": not issues}


def validate_pair(pre_envelope: Any, post_envelope: Any, sequence: int) -> dict[str, Any]:
    issues: list[str] = []
    pre_value = response(pre_envelope, sequence, "pre", issues)
    post_value = response(post_envelope, sequence, "post", issues)
    pre_meta = pre_value.get("_envelope_meta", {})
    post_meta = post_value.get("_envelope_meta", {})
    if (exact_int(pre_meta.get("request_finished_unix_ns"))
            and exact_int(post_meta.get("request_started_unix_ns"))
            and post_meta["request_started_unix_ns"] < pre_meta["request_finished_unix_ns"]):
        issues.append("post query began before the pre query completed")
    pre = counters(pre_value, "pre", issues)
    post = counters(post_value, "post", issues)
    for lane in ALL_LANES:
        if lane not in pre or lane not in post:
            continue
        before, after = pre[lane], post[lane]
        for key in ("lane", "session_identity", "local_address", "remote_address"):
            if before.get(key) != after.get(key):
                issues.append(f"lane {lane} pre/post {key} changed")
        issues.extend(raw_write_activity_validator.validate_identity_stable(
            before.get("raw_write_activity"), after.get("raw_write_activity"), f"lane {lane} raw-write pre/post"))
        if lane == 0:
            if before.get("enabled") is not False or after.get("enabled") is not False:
                issues.append("control-lane sender observer unexpectedly enabled")
            require_zero_activity(before, "pre control lane", issues)
            require_zero_activity(after, "post control lane", issues)
            issues.extend(raw_write_activity_validator.validate_no_data_activity(
                before.get("raw_write_activity"), "pre control lane raw-write activity"))
            issues.extend(raw_write_activity_validator.validate_no_data_activity(
                after.get("raw_write_activity"), "post control lane raw-write activity"))
            continue
        require_bulk_boundary(before, f"pre lane {lane}", issues)
        require_bulk_boundary(after, f"post lane {lane}", issues)
    selected = [lane for lane in BULK_LANES if post.get(lane, {}).get("_validated_timing", {}).get("bools", {}).get("stream_observed") is True]
    if len(selected) != 1:
        issues.append(f"post observed stream lane cardinality is {len(selected)}, want one")
    selected_lane = selected[0] if len(selected) == 1 else None
    for lane in BULK_LANES:
        row = post.get(lane)
        if row is None:
            continue
        timing = row["_validated_timing"]
        for key in ("ambiguous", "overflow", "multiple_streams", "pending_credit_decision", "send_loop_terminated"):
            if timing["bools"].get(key) is not False:
                issues.append(f"post lane {lane} {key} is not false")
        if lane != selected_lane:
            require_zero_activity(row, f"post nonselected lane {lane}", issues)
            issues.extend(raw_write_activity_validator.validate_no_data_activity(
                row.get("raw_write_activity"), f"post nonselected lane {lane} raw-write activity"))
    summary: dict[str, Any] = {"schema_version": 1, "mode": "pair", "sequence": sequence,
                               "selected_lane": selected_lane, "issues": issues}
    if selected_lane is not None and selected_lane in post:
        row = post[selected_lane]
        timing = row["_validated_timing"]
        ints, durations, groups = timing["ints"], timing["durations"], timing["groups"]
        stream_id = ints["observed_stream_id"]
        if not (0 < stream_id <= UINT32_MAX):
            issues.append("selected lane observed stream ID is outside the positive uint32 range")
        frames = row.get("reserved_data_frames", 0)
        payload = row.get("reserved_payload_bytes", 0)
        charged = row.get("reserved_allocation_charged_bytes", 0)
        frame_fields = ("data_enqueued_frames", "data_completed_frames", "physical_attempt_frames", "physical_success_frames")
        payload_fields = ("data_enqueued_payload_bytes", "data_completed_payload_bytes", "physical_attempt_payload_bytes", "physical_success_payload_bytes")
        if not exact_int(frames) or frames <= 0 or any(ints[key] != frames for key in frame_fields):
            issues.append("selected lane DATA frame conservation failed")
        if not exact_int(payload) or payload <= 0 or any(ints[key] != payload for key in payload_fields):
            issues.append("selected lane DATA payload conservation failed")
        wire = ints["physical_attempt_wire_bytes"]
        if wire != ints["physical_written_wire_bytes"] or wire != ints["physical_success_wire_bytes"] or wire != payload + frames * 12:
            issues.append("selected lane physical wire conservation failed")
        if not exact_int(charged) or charged < payload or charged % 4096 != 0 or charged > payload + frames * 4095:
            issues.append("selected lane allocation charge bounds failed")
        if any(ints[key] != 0 for key in BAD_INTS):
            issues.append("selected lane error, abandoned, or short-write counter nonzero")
        success_counts = (
            groups["data_enqueue_select_by_outcome"]["accepted"]["count"],
            groups["data_completion_select_by_outcome"]["success"]["count"],
            groups["data_call_by_outcome"]["success"]["count"],
            groups["physical_write_by_outcome"]["success"]["count"],
        )
        if any(value != frames for value in success_counts):
            issues.append("selected lane successful duration call counts do not equal DATA frames")
        for group, unwanted in (
            ("data_enqueue_select_by_outcome", ("shutdown", "timeout")),
            ("data_completion_select_by_outcome", ("error", "shutdown", "timeout")),
            ("data_call_by_outcome", ("error", "shutdown", "timeout")),
            ("physical_write_by_outcome", ("error", "short_write")),
        ):
            for outcome in unwanted:
                if groups[group][outcome] != ZERO_BUCKET:
                    issues.append(f"selected lane rejected outcome {group}.{outcome} is nonzero")
        if durations["data_call_total"]["total_nanoseconds"] != durations["data_enqueue_select_total"]["total_nanoseconds"] + durations["data_completion_select_total"]["total_nanoseconds"]:
            issues.append("selected lane DATA-call duration does not telescope")
        if durations["physical_write_total"]["total_nanoseconds"] > durations["data_call_total"]["total_nanoseconds"]:
            issues.append("selected lane physical duration exceeds DATA-call duration")
        waits = durations["credit_wait_total"]["count"]
        first, last = ints["first_wait_token"], ints["last_wait_token"]
        if waits == 0:
            if first != 0 or last != 0:
                issues.append("selected lane has tokens without waits")
        elif first <= 0 or last < first or last - first + 1 != waits:
            issues.append("selected lane wait-token range does not conserve")
        causes = groups["credit_wait_by_cause"]
        if row.get("byte_credit_zero_observations") != causes["byte_zero"]["count"]:
            issues.append("selected lane byte-zero observation count mismatch")
        if row.get("stream_allocation_zero_observations") != causes["stream_allocation_zero"]["count"] + causes["stream_and_session_allocation_zero"]["count"]:
            issues.append("selected lane stream-allocation-zero count mismatch")
        if row.get("session_allocation_zero_observations") != causes["session_allocation_zero"]["count"] + causes["stream_and_session_allocation_zero"]["count"]:
            issues.append("selected lane session-allocation-zero count mismatch")
        races = ints["enqueue_publication_races"]
        if races > frames:
            issues.append("selected lane publication-race count exceeds DATA frames")
        raw_activity = row.get("raw_write_activity")
        raw_generation = raw_activity.get("generation") if type(raw_activity) is dict else None
        raw_identity = {"lane": selected_lane, "generation": raw_generation,
                        "local_address": row.get("local_address"), "remote_address": row.get("remote_address")}
        raw_physical = {"stream_id": stream_id, "frames": frames, "payload_bytes": payload,
                        "wire_bytes": wire, "physical_write_total": durations["physical_write_total"]}
        raw_result = raw_write_activity_validator.validate_live(
            row.get("raw_write_activity"), raw_identity, "post selected", raw_physical)
        issues.extend(raw_result["issues"])
        summary["selected"] = {"lane": selected_lane, "stream_id": stream_id, "frames": frames,
                               "payload_bytes": payload, "wire_bytes": wire,
                               "allocation_charged_bytes": charged, "durations": durations,
                               "enqueue_publication_races": races,
                               "raw_write_activity": raw_result}
    summary["accepted"] = not issues
    return summary


def validate_final_closure(final_sidecar: Any, control_directory: Path) -> dict[str, Any]:
    issues: list[str] = []
    history_rows: list[dict[str, Any]] = []
    inputs = [(sequence, action, control_directory / f"sequence-{sequence}-{action}-sender.json")
              for sequence in FIXED_SEQUENCES for action in ("pre", "post")]
    inputs.append((15, "terminal", control_directory / "terminal-sender.json"))
    for sequence, action, path in inputs:
        try:
            envelope = json.loads(path.read_text())
            value = response(envelope, sequence, action, issues)
            history_rows.extend(counters(value, action, issues).values())
        except (OSError, json.JSONDecodeError, TypeError, ValueError) as error:
            issues.append(f"raw-write closure history {path.name} is unreadable: {error!r}")
    result = raw_write_activity_validator.validate_final_closure(final_sidecar, history_rows)
    issues.extend(result["issues"])
    return {"schema_version": 1, "mode": "final-closure", "issues": issues, "accepted": not issues}


def main() -> None:
    import argparse
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True, choices=("pre", "pair", "terminal", "final-closure"))
    parser.add_argument("--pre", type=Path)
    parser.add_argument("--post", type=Path)
    parser.add_argument("--terminal", type=Path)
    parser.add_argument("--final-closure", type=Path)
    parser.add_argument("--control-directory", type=Path)
    parser.add_argument("--sequence", type=int)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    try:
        if args.mode != "final-closure" and args.sequence not in FIXED_SEQUENCES:
            raise ValueError("sequence is outside fixed diagnostic set")
        if args.mode == "pre":
            if (args.pre is None or args.post is not None or args.terminal is not None
                    or args.final_closure is not None or args.control_directory is not None):
                raise ValueError("pre mode requires only --pre")
            result = validate_pre(json.loads(args.pre.read_text()), args.sequence)
        elif args.mode == "pair":
            if (args.pre is None or args.post is None or args.terminal is not None
                    or args.final_closure is not None or args.control_directory is not None):
                raise ValueError("pair mode requires only --pre and --post")
            result = validate_pair(json.loads(args.pre.read_text()), json.loads(args.post.read_text()), args.sequence)
        elif args.mode == "terminal":
            if args.terminal is None or args.pre is not None or args.post is not None or args.final_closure is not None or args.control_directory is not None:
                raise ValueError("terminal mode requires only --terminal")
            result = validate_terminal(json.loads(args.terminal.read_text()), args.sequence)
        else:
            if args.final_closure is None or args.control_directory is None or any((args.pre, args.post, args.terminal, args.sequence)):
                raise ValueError("final-closure mode requires only --final-closure and --control-directory")
            result = validate_final_closure(json.loads(args.final_closure.read_text()), args.control_directory)
    except Exception as error:
        result = {"schema_version": 1, "mode": args.mode, "sequence": args.sequence,
                  "issues": [f"malformed sender snapshot input: {error!r}"], "accepted": False}
    with args.output.open("x", encoding="utf-8") as stream:
        json.dump(result, stream, indent=2, sort_keys=True)
        stream.write("\n")
    raise SystemExit(0 if result["accepted"] else 1)


if __name__ == "__main__":
    main()

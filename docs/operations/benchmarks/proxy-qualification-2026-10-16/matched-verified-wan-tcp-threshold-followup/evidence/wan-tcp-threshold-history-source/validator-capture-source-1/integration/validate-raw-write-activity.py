#!/usr/bin/env python3
"""Fail-closed validation for qualification raw-write activity snapshots."""

from __future__ import annotations

import importlib.util
import pathlib
from typing import Any

_tcp_validator_path = pathlib.Path(__file__).with_name("validate-tcp-state.py")
_tcp_spec = importlib.util.spec_from_file_location("tcp_state_validator", _tcp_validator_path)
if _tcp_spec is None or _tcp_spec.loader is None:
    raise RuntimeError("TCP-state validator import failed")
tcp_state_validator = importlib.util.module_from_spec(_tcp_spec)
_tcp_spec.loader.exec_module(tcp_state_validator)

UINT32_MAX = (1 << 32) - 1
UINT64_MAX = (1 << 64) - 1
INT64_MAX = (1 << 63) - 1
LANES = (0, 1, 2, 3, 4)
BOOL_FIELDS = {
    "enabled", "complete", "reset_performed", "bound", "close_observed",
    "session_joined", "ambiguous", "overflow", "stream_observed", "multiple_streams",
}
INT_FIELDS = {
    "schema_version", "lane", "generation", "observed_stream_id", "active_envelopes",
    "active_raw_writes", "maximum_active_raw_writes", "envelope_count",
    "envelope_payload_bytes", "envelope_wire_bytes", "envelope_zero_raw_calls",
    "boundary_active_raw_writes", "boundary_outside_completions", "envelope_overlaps",
    "owner_generation_changes",
}
BUCKET_FIELDS = {"count", "total_nanoseconds", "max_nanoseconds"}
CLASS_SCALARS = {
    "calls", "attempt_bytes", "written_bytes", "success_calls", "error_calls", "short_calls",
    "success_attempt_bytes", "success_written_bytes", "error_attempt_bytes",
    "error_written_bytes", "short_attempt_bytes", "short_written_bytes",
}
CLASS_BUCKETS = {"duration", "success_duration", "error_duration", "short_duration"}
ACTIVITY_FIELDS = BOOL_FIELDS | INT_FIELDS | {
    "local_address", "remote_address", "envelope_duration", "contained_raw_duration",
    "residual_duration", "contained", "outside", "straddling", "tcp_state",
}
ZERO_BUCKET = {"count": 0, "total_nanoseconds": 0, "max_nanoseconds": 0}
YAMUX_PLACEHOLDER_ADDRESSES = {"yamux:local", "yamux:remote"}


def exact_uint(value: Any, maximum: int = UINT64_MAX) -> bool:
    return type(value) is int and 0 <= value <= maximum


def exact_bool(value: Any) -> bool:
    return type(value) is bool


def bucket(value: Any, path: str, issues: list[str]) -> dict[str, int]:
    if type(value) is not dict:
        issues.append(f"{path} is not an object")
        value = {}
    if set(value) != BUCKET_FIELDS:
        issues.append(f"{path} fields do not match the fixed duration schema")
    result: dict[str, int] = {}
    for key in BUCKET_FIELDS:
        current = value.get(key)
        if not exact_uint(current):
            issues.append(f"{path}.{key} is not a uint64")
            current = 0
        result[key] = current
    if result["max_nanoseconds"] > result["total_nanoseconds"]:
        issues.append(f"{path} maximum exceeds total")
    if result["max_nanoseconds"] > INT64_MAX:
        issues.append(f"{path} maximum exceeds one Go duration")
    if result["count"] == 0 and (result["total_nanoseconds"] or result["max_nanoseconds"]):
        issues.append(f"{path} has duration without calls")
    if result["count"] and result["total_nanoseconds"] > result["count"] * result["max_nanoseconds"]:
        issues.append(f"{path} total exceeds count times maximum")
    return result


def raw_class(value: Any, path: str, issues: list[str]) -> dict[str, Any]:
    if type(value) is not dict:
        issues.append(f"{path} is not an object")
        value = {}
    if set(value) != CLASS_SCALARS | CLASS_BUCKETS:
        issues.append(f"{path} fields do not match the fixed raw-write class schema")
    scalars: dict[str, int] = {}
    for key in CLASS_SCALARS:
        current = value.get(key)
        if not exact_uint(current):
            issues.append(f"{path}.{key} is not a uint64")
            current = 0
        scalars[key] = current
    buckets = {key: bucket(value.get(key), f"{path}.{key}", issues) for key in CLASS_BUCKETS}
    calls = scalars["success_calls"] + scalars["error_calls"] + scalars["short_calls"]
    attempts = scalars["success_attempt_bytes"] + scalars["error_attempt_bytes"] + scalars["short_attempt_bytes"]
    written = scalars["success_written_bytes"] + scalars["error_written_bytes"] + scalars["short_written_bytes"]
    if scalars["calls"] != calls:
        issues.append(f"{path} call outcomes do not conserve")
    if scalars["attempt_bytes"] != attempts or scalars["written_bytes"] != written:
        issues.append(f"{path} byte outcomes do not conserve")
    if scalars["written_bytes"] > scalars["attempt_bytes"]:
        issues.append(f"{path} written bytes exceed attempted bytes")
    if scalars["success_attempt_bytes"] != scalars["success_written_bytes"]:
        issues.append(f"{path} successful byte totals differ")
    for outcome in ("success", "error", "short"):
        calls = scalars[f"{outcome}_calls"]
        attempted = scalars[f"{outcome}_attempt_bytes"]
        written = scalars[f"{outcome}_written_bytes"]
        if calls == 0 and (attempted or written):
            issues.append(f"{path} {outcome} bytes exist without calls")
        if written > attempted:
            issues.append(f"{path} {outcome} written bytes exceed attempted bytes")
    if scalars["short_calls"] and scalars["short_written_bytes"] >= scalars["short_attempt_bytes"]:
        issues.append(f"{path} short writes do not write fewer bytes")
    if buckets["duration"]["count"] != scalars["calls"]:
        issues.append(f"{path} duration count differs from calls")
    outcome_buckets = [buckets["success_duration"], buckets["error_duration"], buckets["short_duration"]]
    if (buckets["duration"]["count"] != sum(item["count"] for item in outcome_buckets)
            or buckets["duration"]["total_nanoseconds"] != sum(item["total_nanoseconds"] for item in outcome_buckets)
            or buckets["duration"]["max_nanoseconds"] != max((item["max_nanoseconds"] for item in outcome_buckets), default=0)):
        issues.append(f"{path} duration outcomes do not conserve")
    for outcome in ("success", "error", "short"):
        if buckets[f"{outcome}_duration"]["count"] != scalars[f"{outcome}_calls"]:
            issues.append(f"{path} {outcome} duration count differs from calls")
    return {"scalars": scalars, "buckets": buckets}


def validate_activity(value: Any, path: str = "raw_write_activity") -> dict[str, Any]:
    issues: list[str] = []
    if type(value) is not dict:
        issues.append(f"{path} is not an object")
        value = {}
    if set(value) != ACTIVITY_FIELDS:
        issues.append(f"{path} fields do not match schema 1")
    bools: dict[str, bool] = {}
    ints: dict[str, int] = {}
    for key in BOOL_FIELDS:
        current = value.get(key)
        if not exact_bool(current):
            issues.append(f"{path}.{key} is not an exact boolean")
            current = False
        bools[key] = current
    for key in INT_FIELDS:
        maximum = UINT32_MAX if key == "observed_stream_id" else UINT64_MAX
        current = value.get(key)
        if not exact_uint(current, maximum):
            issues.append(f"{path}.{key} is outside its unsigned bound")
            current = 0
        ints[key] = current
    if ints["schema_version"] != 1:
        issues.append(f"{path} schema version is not one")
    if type(value.get("lane")) is not int or value.get("lane") not in LANES:
        issues.append(f"{path}.lane is outside the fixed lane set")
    for address_name in ("local_address", "remote_address"):
        address = value.get(address_name)
        if type(address) is not str or not address or address != address.strip():
            issues.append(f"{path}.{address_name} is empty or noncanonical")
        if type(address) is str and address.strip() in YAMUX_PLACEHOLDER_ADDRESSES:
            issues.append(f"{path}.{address_name} is a Yamux placeholder")
    durations = {name: bucket(value.get(name), f"{path}.{name}", issues) for name in
                 ("envelope_duration", "contained_raw_duration", "residual_duration")}
    classes = {name: raw_class(value.get(name), f"{path}.{name}", issues) for name in
               ("contained", "outside", "straddling")}
    if ints["active_envelopes"] > 1 or ints["maximum_active_raw_writes"] > 1:
        issues.append(f"{path} active/maximum concurrency exceeds one")
    if ints["active_raw_writes"] > ints["maximum_active_raw_writes"]:
        issues.append(f"{path} active raw writes exceed the interval maximum")
    if bools["complete"] != bools["reset_performed"]:
        issues.append(f"{path} complete/reset flags differ")
    if bools["complete"] and (ints["active_envelopes"] or ints["active_raw_writes"]):
        issues.append(f"{path} complete snapshot retains active ownership")
    if bools["session_joined"] and not bools["close_observed"]:
        issues.append(f"{path} session joined before close was observed")
    if ints["envelope_zero_raw_calls"] > ints["envelope_count"]:
        issues.append(f"{path} zero-raw envelope count exceeds envelope count")
    if ints["envelope_wire_bytes"] < ints["envelope_payload_bytes"]:
        issues.append(f"{path} envelope wire bytes are below payload bytes")
    if ints["envelope_count"] == 0 and (ints["envelope_payload_bytes"] or ints["envelope_wire_bytes"]):
        issues.append(f"{path} envelope bytes exist without envelopes")
    if ints["envelope_count"] > 0 and (ints["envelope_payload_bytes"] == 0 or ints["envelope_wire_bytes"] == 0):
        issues.append(f"{path} envelopes exist without positive payload and wire bytes")
    if bools["stream_observed"] != (ints["envelope_count"] > 0):
        issues.append(f"{path} stream observation differs from envelope presence")
    if not bools["stream_observed"] and ints["observed_stream_id"] != 0:
        issues.append(f"{path} has a stream ID without an observed stream")
    if bools["stream_observed"] and ints["observed_stream_id"] == 0:
        issues.append(f"{path} observed stream ID is not positive")
    if bools["multiple_streams"] and (not bools["stream_observed"] or ints["envelope_count"] < 2):
        issues.append(f"{path} multiple-stream marker lacks multiple envelopes")
    if durations["envelope_duration"]["count"] != ints["envelope_count"]:
        issues.append(f"{path} envelope duration count differs from envelope count")
    if (durations["contained_raw_duration"]["total_nanoseconds"]
            != classes["contained"]["buckets"]["duration"]["total_nanoseconds"]):
        issues.append(f"{path} contained duration total differs from contained class")
    if durations["envelope_duration"]["count"] != durations["contained_raw_duration"]["count"] or durations["envelope_duration"]["count"] != durations["residual_duration"]["count"]:
        issues.append(f"{path} envelope/contained/residual bucket counts differ")
    if durations["envelope_duration"]["total_nanoseconds"] != durations["contained_raw_duration"]["total_nanoseconds"] + durations["residual_duration"]["total_nanoseconds"]:
        issues.append(f"{path} residual duration does not subtract exactly")
    if durations["contained_raw_duration"]["total_nanoseconds"] > durations["envelope_duration"]["total_nanoseconds"]:
        issues.append(f"{path} contained duration exceeds envelope duration")
    if (durations["contained_raw_duration"]["max_nanoseconds"] > durations["envelope_duration"]["max_nanoseconds"]
            or durations["residual_duration"]["max_nanoseconds"] > durations["envelope_duration"]["max_nanoseconds"]):
        issues.append(f"{path} contained or residual maximum exceeds the envelope maximum")
    contained_calls = classes["contained"]["scalars"]["calls"]
    envelopes_with_raw = ints["envelope_count"] - ints["envelope_zero_raw_calls"]
    if contained_calls < envelopes_with_raw:
        issues.append(f"{path} contained calls cannot cover the nonempty envelopes")
    if (contained_calls == 0) != (ints["envelope_zero_raw_calls"] == ints["envelope_count"]):
        issues.append(f"{path} zero-raw envelope count disagrees with contained calls")
    raw_calls = sum(classes[name]["scalars"]["calls"] for name in ("contained", "outside", "straddling"))
    expected_maximum = 1 if raw_calls else 0
    if ints["maximum_active_raw_writes"] != expected_maximum:
        issues.append(f"{path} maximum raw-write activity disagrees with recorded calls")
    tcp_state = tcp_state_validator.validate_activity(value.get("tcp_state"), f"{path}.tcp_state")
    issues.extend(tcp_state["issues"])
    return {"accepted": not issues, "issues": issues, "bools": bools, "ints": ints,
            "durations": durations, "classes": classes, "tcp_state": tcp_state}


def validate_live(value: Any, identity: dict[str, Any], phase: str,
                  physical: dict[str, Any] | None = None) -> dict[str, Any]:
    result = validate_activity(value, f"{phase}.raw_write_activity")
    issues = result["issues"]
    bools, ints = result["bools"], result["ints"]
    normalized_value = value if type(value) is dict else {}
    for key in ("enabled", "complete", "reset_performed", "bound"):
        if bools.get(key) is not True:
            issues.append(f"{phase} raw-write {key} is not true")
    for key in ("close_observed", "session_joined", "ambiguous", "overflow"):
        if bools.get(key) is not False:
            issues.append(f"{phase} raw-write {key} is not false")
    for key in ("active_envelopes", "active_raw_writes", "boundary_active_raw_writes",
                "boundary_outside_completions", "envelope_overlaps", "owner_generation_changes"):
        if ints.get(key) != 0:
            issues.append(f"{phase} raw-write {key} is nonzero")
    if ints.get("generation", 0) <= 0:
        issues.append(f"{phase} raw-write generation is not positive")
    for key in ("lane", "local_address", "remote_address"):
        if normalized_value.get(key) != identity.get(key):
            issues.append(f"{phase} raw-write {key} differs from sender identity")
    for name, current in result["classes"].items():
        if current["scalars"]["error_calls"] or current["scalars"]["short_calls"]:
            issues.append(f"{phase} raw-write {name} contains error or short writes")
    if any(result["classes"]["straddling"]["scalars"].values()):
        issues.append(f"{phase} raw-write straddling class is nonzero")
    if physical is not None:
        if bools.get("multiple_streams") is not False:
            issues.append(f"{phase} raw-write multiple-stream marker is not false")
        if bools.get("stream_observed") is not True or ints.get("observed_stream_id") != physical.get("stream_id"):
            issues.append(f"{phase} raw-write stream identity differs from selected sender stream")
        joins = {
            "envelope_count": physical.get("frames"),
            "envelope_payload_bytes": physical.get("payload_bytes"),
            "envelope_wire_bytes": physical.get("wire_bytes"),
        }
        for key, expected in joins.items():
            if ints.get(key) != expected:
                issues.append(f"{phase} raw-write {key} differs from physical DATA ledger")
        if result["durations"]["envelope_duration"] != physical.get("physical_write_total"):
            issues.append(f"{phase} raw-write envelope duration differs from physical write duration")
        if ints.get("envelope_zero_raw_calls") != 0 or result["classes"]["contained"]["scalars"]["calls"] <= 0:
            issues.append(f"{phase} selected DATA envelope lacks contained raw writes")
        contained_scalars = result["classes"]["contained"]["scalars"]
        if contained_scalars["attempt_bytes"] <= 0 or contained_scalars["written_bytes"] <= 0:
            issues.append(f"{phase} selected DATA envelope has no contained raw bytes")
    if phase in {"post", "post selected"}:
        tcp_live = tcp_state_validator.validate_live(
            normalized_value.get("tcp_state"), owner_identity=identity,
            path=f"{phase}.raw_write_activity.tcp_state")
    else:
        tcp_live = tcp_state_validator.validate_boundary(
            normalized_value.get("tcp_state"), owner_identity=identity,
            path=f"{phase}.raw_write_activity.tcp_state")
    issues.extend(tcp_live["issues"])
    result["tcp_state"] = tcp_live
    result["accepted"] = not issues
    return result


def validate_identity_stable(before: Any, after: Any, path: str = "raw-write pre/post") -> list[str]:
    issues: list[str] = []
    if type(before) is not dict or type(after) is not dict:
        return [f"{path} snapshots are not objects"]
    for key in ("lane", "generation", "local_address", "remote_address"):
        if before.get(key) != after.get(key):
            issues.append(f"{path} {key} changed")
    return issues


def validate_no_data_activity(value: Any, path: str) -> list[str]:
    result = validate_activity(value, path)
    issues = list(result["issues"])
    if result["bools"].get("stream_observed") is not False or result["ints"].get("observed_stream_id") != 0:
        issues.append(f"{path} unexpectedly observed a DATA stream")
    for key in ("envelope_count", "envelope_payload_bytes", "envelope_wire_bytes", "envelope_zero_raw_calls"):
        if result["ints"].get(key) != 0:
            issues.append(f"{path}.{key} is nonzero")
    if result["durations"]["envelope_duration"] != ZERO_BUCKET or result["durations"]["contained_raw_duration"] != ZERO_BUCKET or result["durations"]["residual_duration"] != ZERO_BUCKET:
        issues.append(f"{path} DATA envelope durations are nonzero")
    if any(result["classes"]["contained"]["scalars"].values()):
        issues.append(f"{path} contained raw-write class is nonzero")
    if any(result["classes"]["straddling"]["scalars"].values()):
        issues.append(f"{path} straddling raw-write class is nonzero")
    normalized = value if type(value) is dict else {}
    tcp_value = normalized.get("tcp_state")
    tcp_sequence = tcp_value.get("sequence") if type(tcp_value) is dict else ""
    tcp_result = tcp_state_validator.validate_no_data(
        tcp_value, sequence=tcp_sequence, path=f"{path}.tcp_state")
    issues.extend(tcp_result["issues"])
    return issues


def validate_interval_outcomes(value: Any, path: str) -> list[str]:
    result = validate_activity(value, path)
    issues = list(result["issues"])
    for name, current in result["classes"].items():
        scalars = current["scalars"]
        if scalars["error_calls"] or scalars["short_calls"]:
            issues.append(f"{path}.{name} contains error or short raw writes")
    return issues


def validate_final_closure(sidecar: Any, history_rows: list[dict[str, Any]]) -> dict[str, Any]:
    issues: list[str] = []
    if type(sidecar) is not dict:
        return {"accepted": False, "issues": ["raw-write final closure is not an object"]}
    expected_top = {
        "schema_version", "expected_sessions", "complete", "invalid",
        "tcp_slow_start_after_idle_setup", "tcp_slow_start_after_idle_terminal",
        "tcp_slow_start_after_idle_read_error", "records",
    }
    if set(sidecar) != expected_top:
        issues.append("raw-write final closure fields do not match schema 1")
    if type(sidecar.get("schema_version")) is not int or sidecar.get("schema_version") != 1:
        issues.append("raw-write final closure schema version is not exact integer one")
    if type(sidecar.get("expected_sessions")) is not int or sidecar.get("expected_sessions") != 5:
        issues.append("raw-write final closure schema/session count mismatch")
    if sidecar.get("complete") is not True or sidecar.get("invalid") is not False:
        issues.append("raw-write final closure is incomplete or invalid")
    setup_slow_start = sidecar.get("tcp_slow_start_after_idle_setup")
    terminal_slow_start = sidecar.get("tcp_slow_start_after_idle_terminal")
    if (type(setup_slow_start) is not int or setup_slow_start not in (0, 1)
            or type(terminal_slow_start) is not int or terminal_slow_start not in (0, 1)
            or setup_slow_start != terminal_slow_start
            or sidecar.get("tcp_slow_start_after_idle_read_error") is not False):
        issues.append("raw-write final TCP slow-start-after-idle evidence is invalid or changed")
    if type(history_rows) is not list or len(history_rows) != 45:
        issues.append("raw-write history cardinality is not nine snapshots across five lanes")
        history_rows = history_rows if type(history_rows) is list else []
    identities: dict[int, tuple[Any, ...]] = {}
    lane_counts = {lane: 0 for lane in LANES}
    for index, row in enumerate(history_rows):
        if type(row) is not dict:
            issues.append(f"history row {index} is not an object")
            continue
        raw = row.get("raw_write_activity")
        parsed = validate_activity(raw, f"history row {index}.raw_write_activity")
        issues.extend(parsed["issues"])
        if any(parsed["classes"]["straddling"]["scalars"].values()):
            issues.append(f"history row {index} straddling raw-write class is nonzero")
        lane = row.get("lane")
        session_identity = row.get("session_identity")
        if (type(session_identity) is not str or not session_identity
                or session_identity != session_identity.strip()):
            issues.append(f"history row {index} session identity is empty or noncanonical")
        if parsed["ints"].get("generation", 0) <= 0:
            issues.append(f"history row {index} generation is not positive")
        if type(raw) is dict and raw.get("lane") != lane:
            issues.append(f"history row {index} raw lane differs from row lane")
        identity = (row.get("session_identity"), raw.get("generation") if type(raw) is dict else None,
                    raw.get("local_address") if type(raw) is dict else None,
                    raw.get("remote_address") if type(raw) is dict else None)
        if lane in identities and identities[lane] != identity:
            issues.append(f"history lane {lane!r} identity changed")
        elif type(lane) is int and lane in LANES:
            identities[lane] = identity
            lane_counts[lane] += 1
        else:
            issues.append(f"history row {index} lane is invalid")
    if set(identities) != set(LANES):
        issues.append("raw-write history does not bind all five lanes")
    if any(lane_counts[lane] != 9 for lane in LANES):
        issues.append("raw-write history does not contain nine records for every lane")
    records = sidecar.get("records")
    if type(records) is not list or len(records) != 5:
        issues.append("raw-write final closure record cardinality is not five")
        records = []
    if [record.get("lane") if type(record) is dict else None for record in records] != list(LANES):
        issues.append("raw-write final closure records are not ordered lane 0 through 4")
    seen: set[int] = set()
    for index, record in enumerate(records):
        path = f"raw-write final record {index}"
        if type(record) is not dict:
            issues.append(f"{path} is not an object")
            continue
        if set(record) != {"lane", "aggregate", "session_identity", "raw_write_activity"}:
            issues.append(f"{path} fields do not match schema 1")
        if (type(record.get("session_identity")) is not str or not record.get("session_identity")
                or record.get("session_identity") != record.get("session_identity").strip()):
            issues.append(f"{path} session identity is empty or noncanonical")
        lane = record.get("lane")
        if type(lane) is not int or lane not in LANES or lane in seen:
            issues.append(f"{path} lane is invalid or duplicated")
            continue
        seen.add(lane)
        if record.get("aggregate") is not True:
            issues.append(f"{path} aggregate negotiation flag is not true")
        raw = record.get("raw_write_activity")
        parsed = validate_activity(raw, f"{path}.raw_write_activity")
        issues.extend(parsed["issues"])
        normalized_raw = raw if type(raw) is dict else {}
        tcp_final = tcp_state_validator.validate_boundary(
            normalized_raw.get("tcp_state"), path=f"{path}.raw_write_activity.tcp_state",
            terminal=True, terminal_sequence="15", owner_identity={
                "lane": normalized_raw.get("lane"),
                "generation": normalized_raw.get("generation"),
                "local_address": normalized_raw.get("local_address"),
                "remote_address": normalized_raw.get("remote_address"),
            })
        issues.extend(tcp_final["issues"])
        tcp_value = normalized_raw.get("tcp_state", {}) if type(normalized_raw) is dict else {}
        if type(tcp_value) is not dict or tcp_value.get("tcp_slow_start_after_idle") != setup_slow_start:
            issues.append(f"{path} TCP slow-start-after-idle differs from terminal sidecar")
        for key in ("enabled", "complete", "reset_performed", "bound", "close_observed", "session_joined"):
            if parsed["bools"].get(key) is not True:
                issues.append(f"{path} {key} is not true")
        for key in ("ambiguous", "overflow", "multiple_streams"):
            if parsed["bools"].get(key) is not False:
                issues.append(f"{path} {key} is not false")
        if parsed["ints"].get("generation", 0) <= 0:
            issues.append(f"{path} generation is not positive")
        for key in ("active_envelopes", "active_raw_writes", "boundary_active_raw_writes",
                    "boundary_outside_completions", "envelope_overlaps", "owner_generation_changes"):
            if parsed["ints"].get(key) != 0:
                issues.append(f"{path} {key} is nonzero")
        issues.extend(validate_interval_outcomes(raw, f"{path}.raw_write_activity"))
        issues.extend(validate_no_data_activity(raw, f"{path}.raw_write_activity"))
        expected = identities.get(lane)
        got = (record.get("session_identity"), raw.get("generation") if type(raw) is dict else None,
               raw.get("local_address") if type(raw) is dict else None,
               raw.get("remote_address") if type(raw) is dict else None)
        if expected is not None and got != expected:
            issues.append(f"{path} identity differs from live history")
        if type(raw) is dict and raw.get("lane") != lane:
            issues.append(f"{path} raw lane differs from record lane")
    return {"schema_version": 1, "mode": "final-closure", "record_count": len(records),
            "issues": issues, "accepted": not issues}

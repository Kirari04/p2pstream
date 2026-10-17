#!/usr/bin/env python3
"""Fail-closed validation for qualification TCP-state activity.

This draft follows the reviewed WIP producer schema. Final source binding is
deliberately deferred until the producer checkpoint is immutable.
"""
from __future__ import annotations

from typing import Any

UINT8_MAX = (1 << 8) - 1
UINT16_MAX = (1 << 16) - 1
UINT32_MAX = (1 << 32) - 1
UINT64_MAX = (1 << 64) - 1
INT64_MAX = (1 << 63) - 1
RECORD_LIMIT = 4096

AVAILABILITY_FIELDS = {
    "state", "ca_state", "retransmits", "rto", "send_mss", "unacked", "lost",
    "retrans", "last_data_sent", "rtt", "rtt_variance", "send_slow_start_threshold",
    "send_congestion_window", "total_retrans", "pacing_rate", "bytes_acked",
    "not_sent_bytes", "delivery_rate", "bytes_sent", "bytes_retransmitted",
    "send_window", "total_rto", "total_rto_recoveries", "total_rto_time",
}
INFO_FIELDS = {
    "schema_version", "captured", "capture_started_unix_ns", "capture_finished_unix_ns",
    "capture_duration_ns", "capture_monotonic_valid", "capture_started_monotonic_ns",
    "capture_finished_monotonic_ns", "tcp_info_attempted", "tcp_info_succeeded",
    "tcp_info_optlen", "tcp_info_struct_size", "required_field_end_offset",
    "congestion_control_attempted", "congestion_control_succeeded",
    "congestion_control_optlen", "congestion_control", "failure_stage", "failure_errno",
    "availability", "state",
    "ca_state", "retransmits", "rto_us", "send_mss", "unacked", "lost", "retrans",
    "last_data_sent_ms", "rtt_us", "rtt_variance_us", "send_slow_start_threshold",
    "send_congestion_window", "total_retrans", "pacing_rate_bytes_per_second",
    "bytes_acked", "not_sent_bytes", "delivery_rate_bytes_per_second", "bytes_sent",
    "bytes_retransmitted", "send_window_bytes", "total_rto", "total_rto_recoveries",
    "total_rto_time_ms",
}
INFO_BOOL_FIELDS = {
    "captured", "capture_monotonic_valid", "tcp_info_attempted", "tcp_info_succeeded",
    "congestion_control_attempted", "congestion_control_succeeded",
}
INFO_UINT8_FIELDS = {"state", "ca_state", "retransmits"}
INFO_UINT16_FIELDS = {"total_rto", "total_rto_recoveries"}
INFO_UINT32_FIELDS = {
    "tcp_info_optlen", "tcp_info_struct_size", "required_field_end_offset",
    "congestion_control_optlen", "rto_us", "send_mss", "unacked", "lost", "retrans",
    "last_data_sent_ms", "rtt_us", "rtt_variance_us", "send_slow_start_threshold",
    "send_congestion_window", "total_retrans", "not_sent_bytes", "send_window_bytes",
    "total_rto_time_ms",
}
INFO_UINT64_FIELDS = {
    "capture_duration_ns", "capture_started_monotonic_ns", "capture_finished_monotonic_ns",
    "pacing_rate_bytes_per_second", "bytes_acked", "delivery_rate_bytes_per_second",
    "bytes_sent", "bytes_retransmitted",
}
SAMPLE_FIELDS = {
    "present", "complete", "capture_error", "sample_id", "kind", "classification",
    "credit", "observed_unix_ns", "observed_monotonic_valid", "observed_monotonic_ns",
    "cursor_before_ordinal", "cursor_before_cumulative", "cursor_after_valid", "cursor_after_ordinal",
    "cursor_after_cumulative", "cursor_changed", "tcp_info",
}
WAIT_FIELDS = {
    "record_id", "stream_id", "wait_token", "intervening_allocation", "pending_raw",
    "claimed", "raw_pair_record_id", "active", "ambiguous", "entry", "retry",
}
PAIR_FIELDS = {
    "record_id", "wait_record_id", "stream_id", "wait_token", "envelope_token",
    "committed", "active", "ambiguous", "before_capture_error", "after_capture_error",
    "bracket_valid", "raw_write_started_unix_ns", "raw_write_finished_unix_ns",
    "raw_write_monotonic_valid", "raw_write_started_monotonic_ns",
    "raw_write_finished_monotonic_ns", "additional_contained_raw_writes", "before", "after",
}
ACTIVITY_FIELDS = {
    "schema_version", "enabled", "bound", "armed", "sequence", "complete",
    "reset_performed", "invalid", "ambiguous", "overflow", "tcp_slow_start_after_idle",
    "active_samples", "pending_raw_claims", "active_raw_pairs", "dropped_wait_records",
    "dropped_raw_pair_records", "capture_calls", "capture_errors", "capture_duration",
    "bind_tcp_info", "last_live_tcp_info", "pre_tcp_info", "post_tcp_info", "wait_records", "raw_pairs",
    "threshold_history",
}
ACTIVITY_BOOL_FIELDS = {
    "enabled", "bound", "armed", "complete", "reset_performed", "invalid", "ambiguous", "overflow",
}
ACTIVITY_UINT_FIELDS = {
    "active_samples", "pending_raw_claims", "active_raw_pairs", "dropped_wait_records",
    "dropped_raw_pair_records", "capture_calls", "capture_errors",
}
BUCKET_FIELDS = {"count", "total_nanoseconds", "max_nanoseconds"}
WAIT_CLASSIFICATIONS = {"applied_range", "pre_entry_buffered", "stale", "timeout", "shutdown"}

HISTORY_RECORD_FIELDS = {
    "present", "raw_write_ordinal", "owner_lane", "owner_generation", "local_address",
    "remote_address", "attempted_bytes", "written_bytes", "write_error", "short_write",
    "envelope_attributed", "envelope_token", "stream_id", "envelope_payload_bytes",
    "envelope_wire_bytes", "raw_write_started_unix_ns", "raw_write_finished_unix_ns",
    "raw_write_monotonic_valid", "raw_write_started_monotonic_ns",
    "raw_write_finished_monotonic_ns", "bracket_valid", "transition_location",
    "transition_observed_across_write", "before", "after",
}
HISTORY_FIELDS = {
    "schema_version", "enabled", "required", "monitoring", "complete",
    "transition_observed", "selected_pre_compared", "selected_pre_sequence",
    "selected_pre_arm_ordinal", "selected_pre_initial", "selected_pre_matched",
    "inconclusive", "failure_reason", "explanation_status", "explanation_available",
    "explanation_inconclusive", "explanation_reason", "pre_bind_unobserved", "exhausted",
    "active_sample", "closed", "baseline_threshold", "first_changed_threshold",
    "selected_pre_threshold", "selected_pre_tcp_info", "raw_write_ordinal",
    "monitored_raw_write_attempts", "provider_capture_calls", "provider_capture_errors",
    "provider_capture_duration", "raw_provider_calls_shared_with_tcp_interval",
    "raw_provider_duration_shared_with_tcp_interval", "baseline", "last_equal",
    "first_different", "failure",
}
HISTORY_BOOL_FIELDS = {
    "enabled", "required", "monitoring", "complete", "transition_observed",
    "selected_pre_compared", "selected_pre_initial", "selected_pre_matched", "inconclusive",
    "explanation_available", "explanation_inconclusive", "pre_bind_unobserved", "exhausted",
    "active_sample", "closed",
}
HISTORY_UINT_FIELDS = {
    "selected_pre_arm_ordinal", "raw_write_ordinal", "monitored_raw_write_attempts",
    "provider_capture_calls", "provider_capture_errors",
    "raw_provider_calls_shared_with_tcp_interval",
}
HISTORY_STATUS_DOMAIN = {
    "monitoring", "inconclusive", "selected_pre_matched", "transition_observed_with_initial_pre",
    "not_reproduced", "selected_pre_mismatch", "selected_pre_transition_not_strictly_prior",
    "transition_observed", "not_required_no_transition",
}
HISTORY_FAILURE_DOMAIN = {
    "", "bind_capture_invalid", "pre_bind_unobserved", "raw_ordinal_overflow",
    "concurrent_raw_write", "attempt_limit", "counter_overflow", "owner_mismatch",
    "close_race", "incomplete_write", "capture_or_bracket_invalid",
    "congestion_control_mismatch", "selected_pre_arm_ordinal_overflow",
    "selected_pre_capture_invalid", "selected_pre_transition_unobserved",
    "close_before_selected_pre", "close_before_observation",
}
HISTORY_EXPLANATION_REASON_DOMAIN = {
    "", "selected_pre_mismatch", "selected_pre_transition_not_strictly_prior",
}
HISTORY_TRANSITION_LOCATION_DOMAIN = {
    "", "between_bind_and_first_write", "between_writes", "across_write_observation",
}


def exact_bool(value: Any) -> bool:
    return type(value) is bool


def exact_uint(value: Any, maximum: int = UINT64_MAX, *, positive: bool = False) -> bool:
    return type(value) is int and (0 < value if positive else 0 <= value) and value <= maximum


def exact_int64(value: Any, *, positive: bool = False) -> bool:
    return type(value) is int and (-INT64_MAX - 1) <= value <= INT64_MAX and (not positive or value > 0)


def duration_bucket(value: Any, path: str, issues: list[str]) -> dict[str, int]:
    if type(value) is not dict:
        issues.append(f"{path} is not an object")
        value = {}
    if set(value) != BUCKET_FIELDS:
        issues.append(f"{path} fields changed")
    result: dict[str, int] = {}
    for key in BUCKET_FIELDS:
        current = value.get(key)
        if not exact_uint(current):
            issues.append(f"{path}.{key} is not a uint64")
            current = 0
        result[key] = current
    if result["max_nanoseconds"] > result["total_nanoseconds"]:
        issues.append(f"{path} maximum exceeds total")
    if result["count"] == 0 and (result["total_nanoseconds"] or result["max_nanoseconds"]):
        issues.append(f"{path} has duration without calls")
    if result["count"] and result["total_nanoseconds"] > result["count"] * result["max_nanoseconds"]:
        issues.append(f"{path} total exceeds count times maximum")
    return result


def absent_tcp_info(value: Any) -> bool:
    if type(value) is not dict or set(value) != INFO_FIELDS:
        return False
    if value.get("schema_version") != 0 or type(value.get("schema_version")) is not int:
        return False
    if any(value.get(key) is not False for key in INFO_BOOL_FIELDS):
        return False
    numeric = ({"capture_started_unix_ns", "capture_finished_unix_ns", "failure_errno"}
               | INFO_UINT8_FIELDS | INFO_UINT16_FIELDS | INFO_UINT32_FIELDS | INFO_UINT64_FIELDS)
    if any(type(value.get(key)) is not int or value.get(key) != 0 for key in numeric):
        return False
    if value.get("congestion_control") != "" or value.get("failure_stage") != "":
        return False
    availability = value.get("availability")
    return (type(availability) is dict and set(availability) == AVAILABILITY_FIELDS
            and all(availability.get(key) is False for key in AVAILABILITY_FIELDS))


def tcp_info(value: Any, path: str, issues: list[str], *, require_healthy: bool,
             allow_absent: bool = False) -> dict[str, Any]:
    if allow_absent and absent_tcp_info(value):
        return value
    if type(value) is not dict:
        issues.append(f"{path} is not an object")
        value = {}
    if set(value) != INFO_FIELDS:
        issues.append(f"{path} fields changed")
    if type(value.get("schema_version")) is not int or value.get("schema_version") != 1:
        issues.append(f"{path}.schema_version is not exact integer one")
    for key in INFO_BOOL_FIELDS:
        if not exact_bool(value.get(key)):
            issues.append(f"{path}.{key} is not an exact boolean")
    for key, maximum in ((key, UINT8_MAX) for key in INFO_UINT8_FIELDS):
        if not exact_uint(value.get(key), maximum):
            issues.append(f"{path}.{key} exceeds uint8")
    for key, maximum in ((key, UINT16_MAX) for key in INFO_UINT16_FIELDS):
        if not exact_uint(value.get(key), maximum):
            issues.append(f"{path}.{key} exceeds uint16")
    for key, maximum in ((key, UINT32_MAX) for key in INFO_UINT32_FIELDS):
        if not exact_uint(value.get(key), maximum):
            issues.append(f"{path}.{key} exceeds uint32")
    for key in INFO_UINT64_FIELDS:
        if not exact_uint(value.get(key)):
            issues.append(f"{path}.{key} is not a uint64")
    for key in ("capture_started_unix_ns", "capture_finished_unix_ns"):
        if not exact_int64(value.get(key)):
            issues.append(f"{path}.{key} is not an int64")
    availability = value.get("availability")
    if type(availability) is not dict or set(availability) != AVAILABILITY_FIELDS:
        issues.append(f"{path}.availability fields changed")
        availability = {}
    for key in AVAILABILITY_FIELDS:
        if not exact_bool(availability.get(key)):
            issues.append(f"{path}.availability.{key} is not an exact boolean")
    congestion = value.get("congestion_control")
    if type(congestion) is not str or congestion != congestion.strip():
        issues.append(f"{path}.congestion_control is noncanonical")
    failure_stage = value.get("failure_stage")
    if type(failure_stage) is not str or failure_stage != failure_stage.strip():
        issues.append(f"{path}.failure_stage is noncanonical")
    if not exact_int64(value.get("failure_errno")):
        issues.append(f"{path}.failure_errno is not an int64")
    started, finished = value.get("capture_started_unix_ns"), value.get("capture_finished_unix_ns")
    if exact_int64(started) and exact_int64(finished) and finished < started:
        issues.append(f"{path} wall capture timestamps regress")
    mono_start = value.get("capture_started_monotonic_ns")
    mono_finish = value.get("capture_finished_monotonic_ns")
    if value.get("capture_monotonic_valid") is True:
        if not (exact_uint(mono_start) and exact_uint(mono_finish)
                and mono_finish >= mono_start):
            issues.append(f"{path} monotonic capture coordinates are invalid")
        elif value.get("capture_duration_ns") != mono_finish - mono_start:
            issues.append(f"{path} duration differs from owner-relative monotonic coordinates")
    elif mono_start or mono_finish:
        issues.append(f"{path} has monotonic coordinates without a valid marker")
    if require_healthy:
        required_true = (
            "captured", "capture_monotonic_valid", "tcp_info_attempted", "tcp_info_succeeded",
            "congestion_control_attempted", "congestion_control_succeeded",
        )
        for key in required_true:
            if value.get(key) is not True:
                issues.append(f"{path}.{key} is not true")
        if not all(availability.get(key) is True for key in AVAILABILITY_FIELDS):
            issues.append(f"{path} does not make every required TCP_INFO field available")
        if not exact_uint(value.get("tcp_info_optlen"), UINT32_MAX, positive=True):
            issues.append(f"{path}.tcp_info_optlen is not positive")
        if not exact_uint(value.get("tcp_info_struct_size"), UINT32_MAX, positive=True):
            issues.append(f"{path}.tcp_info_struct_size is not positive")
        if not exact_uint(value.get("required_field_end_offset"), UINT32_MAX, positive=True):
            issues.append(f"{path}.required_field_end_offset is not positive")
        if (exact_uint(value.get("tcp_info_optlen"), UINT32_MAX)
                and exact_uint(value.get("required_field_end_offset"), UINT32_MAX)
                and value.get("tcp_info_optlen") < value.get("required_field_end_offset")):
            issues.append(f"{path}.tcp_info_optlen is below the required field end")
        if not congestion:
            issues.append(f"{path}.congestion_control is empty")
        if value.get("state") != 1:
            issues.append(f"{path}.state is not TCP_ESTABLISHED")
        if (exact_uint(value.get("tcp_info_optlen"), UINT32_MAX)
                and exact_uint(value.get("tcp_info_struct_size"), UINT32_MAX)
                and value.get("tcp_info_optlen") > value.get("tcp_info_struct_size")):
            issues.append(f"{path}.tcp_info_optlen exceeds the bound structure")
        if failure_stage or value.get("failure_errno") != 0:
            issues.append(f"{path} retains failure metadata in a healthy capture")
        if not (exact_int64(started, positive=True) and exact_int64(finished, positive=True)):
            issues.append(f"{path} healthy wall capture timestamps are not positive")
    return value


def sample(value: Any, path: str, issues: list[str], *, kind: str) -> dict[str, Any]:
    if type(value) is not dict:
        issues.append(f"{path} is not an object")
        value = {}
    if set(value) != SAMPLE_FIELDS:
        issues.append(f"{path} fields changed")
    for key in ("present", "complete", "capture_error", "observed_monotonic_valid",
                "cursor_after_valid", "cursor_changed"):
        if not exact_bool(value.get(key)):
            issues.append(f"{path}.{key} is not an exact boolean")
    for key in ("sample_id", "cursor_before_ordinal", "cursor_before_cumulative",
                "cursor_after_ordinal", "cursor_after_cumulative", "observed_monotonic_ns"):
        if not exact_uint(value.get(key)):
            issues.append(f"{path}.{key} is not a uint64")
    if not exact_uint(value.get("credit"), UINT32_MAX):
        issues.append(f"{path}.credit exceeds uint32")
    if not exact_int64(value.get("observed_unix_ns")):
        issues.append(f"{path}.observed_unix_ns is not an int64")
    if value.get("kind") != kind:
        issues.append(f"{path}.kind is not {kind!r}")
    expected_classifications = {"pending"} if kind == "entry" else WAIT_CLASSIFICATIONS
    if value.get("classification") not in expected_classifications:
        issues.append(f"{path}.classification is outside the fixed domain")
    for key in ("present", "complete", "observed_monotonic_valid", "cursor_after_valid"):
        if value.get(key) is not True:
            issues.append(f"{path}.{key} is not true")
    if value.get("capture_error") is not False:
        issues.append(f"{path}.capture_error is not false")
    if not exact_uint(value.get("sample_id"), positive=True):
        issues.append(f"{path}.sample_id is not positive")
    if not exact_int64(value.get("observed_unix_ns"), positive=True):
        issues.append(f"{path}.observed_unix_ns is not positive")
    if not exact_uint(value.get("observed_monotonic_ns"), positive=True):
        issues.append(f"{path}.observed_monotonic_ns is not positive")
    before = (value.get("cursor_before_ordinal"), value.get("cursor_before_cumulative"))
    after = (value.get("cursor_after_ordinal"), value.get("cursor_after_cumulative"))
    if (all(exact_uint(item) for item in before + after)
            and (after[0] < before[0] or after[1] < before[1])):
        issues.append(f"{path} cursor coordinates regress during the synchronous sample")
    changed = before != after
    if exact_bool(value.get("cursor_changed")) and value.get("cursor_changed") is not changed:
        issues.append(f"{path}.cursor_changed disagrees with the observed cursor bracket")
    info = tcp_info(value.get("tcp_info"), f"{path}.tcp_info", issues, require_healthy=True)
    if (exact_int64(value.get("observed_unix_ns"))
            and exact_int64(info.get("capture_started_unix_ns"))
            and value.get("observed_unix_ns") > info.get("capture_started_unix_ns")):
        issues.append(f"{path} local capture starts before caller observation")
    if value.get("observed_monotonic_valid") is True and info.get("capture_monotonic_valid") is True:
        observed = value.get("observed_monotonic_ns")
        if (exact_uint(observed) and exact_uint(info.get("capture_started_monotonic_ns"))
                and exact_uint(info.get("capture_finished_monotonic_ns"))
                and not (observed <= info.get("capture_started_monotonic_ns")
                         <= info.get("capture_finished_monotonic_ns"))):
            issues.append(f"{path} owner-relative capture is not after caller observation")
    return value


def wait_record(value: Any, path: str, issues: list[str]) -> dict[str, Any]:
    if type(value) is not dict:
        issues.append(f"{path} is not an object")
        value = {}
    if set(value) != WAIT_FIELDS:
        issues.append(f"{path} fields changed")
    for key in ("record_id", "wait_token", "raw_pair_record_id"):
        if not exact_uint(value.get(key)):
            issues.append(f"{path}.{key} is not a uint64")
    if not exact_uint(value.get("stream_id"), UINT32_MAX, positive=True):
        issues.append(f"{path}.stream_id is not a positive uint32")
    for key in ("intervening_allocation", "pending_raw", "claimed", "active", "ambiguous"):
        if not exact_bool(value.get(key)):
            issues.append(f"{path}.{key} is not an exact boolean")
    entry = sample(value.get("entry"), f"{path}.entry", issues, kind="entry")
    retry = sample(value.get("retry"), f"{path}.retry", issues, kind="retry")
    if value.get("active") is not False or value.get("ambiguous") is not False:
        issues.append(f"{path} is active or ambiguous")
    if value.get("pending_raw") is not False:
        issues.append(f"{path} retains a pending raw claim after drain")
    if entry.get("sample_id") == retry.get("sample_id"):
        issues.append(f"{path} reuses one sample ID for entry and retry")
    if (entry.get("observed_monotonic_valid") is True and retry.get("observed_monotonic_valid") is True
            and retry.get("observed_monotonic_ns", 0) < entry.get("observed_monotonic_ns", 0)):
        issues.append(f"{path} retry precedes entry in owner-relative time")
    applied = retry.get("classification") == "applied_range"
    if value.get("claimed") is not False:
        issues.append(f"{path} retains a claimed envelope after committed drain")
    if applied != exact_uint(value.get("raw_pair_record_id"), positive=True):
        issues.append(f"{path} raw-pair identity differs from applied-range classification")
    if applied and not exact_uint(retry.get("credit"), UINT32_MAX, positive=True):
        issues.append(f"{path} applied-range retry does not retain positive byte credit")
    return value


def raw_pair(value: Any, path: str, issues: list[str]) -> dict[str, Any]:
    if type(value) is not dict:
        issues.append(f"{path} is not an object")
        value = {}
    if set(value) != PAIR_FIELDS:
        issues.append(f"{path} fields changed")
    for key in ("record_id", "wait_record_id", "wait_token", "envelope_token",
                "additional_contained_raw_writes", "raw_write_started_monotonic_ns",
                "raw_write_finished_monotonic_ns"):
        if not exact_uint(value.get(key)):
            issues.append(f"{path}.{key} is not a uint64")
    if not exact_uint(value.get("stream_id"), UINT32_MAX, positive=True):
        issues.append(f"{path}.stream_id is not a positive uint32")
    for key in ("committed", "active", "ambiguous", "before_capture_error", "after_capture_error",
                "bracket_valid", "raw_write_monotonic_valid"):
        if not exact_bool(value.get(key)):
            issues.append(f"{path}.{key} is not an exact boolean")
    for key in ("raw_write_started_unix_ns", "raw_write_finished_unix_ns"):
        if not exact_int64(value.get(key), positive=True):
            issues.append(f"{path}.{key} is not a positive int64")
    for key in ("record_id", "wait_record_id", "wait_token", "envelope_token"):
        if not exact_uint(value.get(key), positive=True):
            issues.append(f"{path}.{key} is not positive")
    for key in ("committed", "bracket_valid", "raw_write_monotonic_valid"):
        if value.get(key) is not True:
            issues.append(f"{path}.{key} is not true")
    for key in ("active", "ambiguous", "before_capture_error", "after_capture_error"):
        if value.get(key) is not False:
            issues.append(f"{path}.{key} is not false")
    wall_start, wall_finish = value.get("raw_write_started_unix_ns"), value.get("raw_write_finished_unix_ns")
    if exact_int64(wall_start) and exact_int64(wall_finish) and wall_finish < wall_start:
        issues.append(f"{path} raw-write wall coordinates regress")
    mono_start, mono_finish = value.get("raw_write_started_monotonic_ns"), value.get("raw_write_finished_monotonic_ns")
    if not (exact_uint(mono_start, positive=True) and exact_uint(mono_finish, positive=True)
            and mono_finish >= mono_start):
        issues.append(f"{path} raw-write owner-relative coordinates regress")
    before = tcp_info(value.get("before"), f"{path}.before", issues, require_healthy=True)
    after = tcp_info(value.get("after"), f"{path}.after", issues, require_healthy=True)
    if before.get("congestion_control") != after.get("congestion_control"):
        issues.append(f"{path} congestion-control identity changed within raw write")
    if (before.get("capture_monotonic_valid") is True and after.get("capture_monotonic_valid") is True
            and exact_uint(mono_start) and exact_uint(mono_finish)):
        if not (before.get("capture_finished_monotonic_ns", UINT64_MAX) <= mono_start
                <= mono_finish <= after.get("capture_started_monotonic_ns", 0)):
            issues.append(f"{path} TCP captures do not bracket the raw write in owner-relative time")
    if (exact_int64(before.get("capture_finished_unix_ns")) and exact_int64(wall_start)
            and exact_int64(wall_finish) and exact_int64(after.get("capture_started_unix_ns"))
            and not (before.get("capture_finished_unix_ns") <= wall_start
                     <= wall_finish <= after.get("capture_started_unix_ns"))):
        issues.append(f"{path} TCP captures do not contain the raw write in local wall order")
    return value


def absent_history_record(value: Any) -> bool:
    if type(value) is not dict or set(value) != HISTORY_RECORD_FIELDS:
        return False
    false_fields = {
        "present", "write_error", "short_write", "envelope_attributed",
        "raw_write_monotonic_valid", "bracket_valid", "transition_observed_across_write",
    }
    if any(value.get(key) is not False for key in false_fields):
        return False
    integer_fields = HISTORY_RECORD_FIELDS - false_fields - {
        "local_address", "remote_address", "transition_location", "before", "after",
    }
    if any(type(value.get(key)) is not int or value.get(key) != 0 for key in integer_fields):
        return False
    return (value.get("local_address") == "" and value.get("remote_address") == ""
            and value.get("transition_location") == ""
            and absent_tcp_info(value.get("before")) and absent_tcp_info(value.get("after")))


def history_record(value: Any, path: str, issues: list[str]) -> dict[str, Any]:
    if type(value) is not dict:
        issues.append(f"{path} is not an object")
        value = {}
    if set(value) != HISTORY_RECORD_FIELDS:
        issues.append(f"{path} fields changed")
    if value.get("present") is False:
        if not absent_history_record(value):
            issues.append(f"{path} absent record is not the exact zero form")
        return value
    if value.get("present") is not True:
        issues.append(f"{path}.present is not an exact boolean")
    for key in ("write_error", "short_write", "envelope_attributed", "raw_write_monotonic_valid",
                "bracket_valid", "transition_observed_across_write"):
        if not exact_bool(value.get(key)):
            issues.append(f"{path}.{key} is not an exact boolean")
    for key in ("raw_write_ordinal", "owner_generation", "attempted_bytes", "written_bytes",
                "envelope_token", "stream_id", "envelope_payload_bytes", "envelope_wire_bytes",
                "raw_write_started_monotonic_ns", "raw_write_finished_monotonic_ns"):
        if not exact_uint(value.get(key)):
            issues.append(f"{path}.{key} is not a uint64")
    if type(value.get("owner_lane")) is not int or value.get("owner_lane") not in range(5):
        issues.append(f"{path}.owner_lane is outside the exact five-lane domain")
    for key in ("raw_write_started_unix_ns", "raw_write_finished_unix_ns"):
        if not exact_int64(value.get(key), positive=True):
            issues.append(f"{path}.{key} is not a positive int64")
    if not exact_uint(value.get("raw_write_ordinal"), positive=True):
        issues.append(f"{path}.raw_write_ordinal is not positive")
    if not exact_uint(value.get("owner_generation"), positive=True):
        issues.append(f"{path}.owner_generation is not positive")
    for key in ("local_address", "remote_address"):
        current = value.get(key)
        if (type(current) is not str or not current or current != current.strip()
                or current in {"yamux:local", "yamux:remote"}):
            issues.append(f"{path}.{key} is empty, noncanonical, or a Yamux placeholder")
    attempted, written = value.get("attempted_bytes"), value.get("written_bytes")
    if (not exact_uint(attempted, positive=True) or not exact_uint(written)
            or (exact_uint(attempted) and exact_uint(written) and written > attempted)):
        issues.append(f"{path} write byte outcome is invalid")
    if value.get("write_error") is False and value.get("short_write") is not (written != attempted):
        issues.append(f"{path}.short_write disagrees with the write outcome")
    if value.get("write_error") is True and value.get("short_write") is not False:
        issues.append(f"{path} error and short-write markers overlap")
    if value.get("envelope_attributed") is True:
        for key in ("envelope_token", "stream_id", "envelope_payload_bytes", "envelope_wire_bytes"):
            if not exact_uint(value.get(key), positive=True):
                issues.append(f"{path}.{key} is not positive for an attributed DATA envelope")
        if (exact_uint(value.get("envelope_payload_bytes"), positive=True)
                and exact_uint(value.get("envelope_wire_bytes"), positive=True)
                and value.get("envelope_wire_bytes") != value.get("envelope_payload_bytes") + 12):
            issues.append(f"{path} attributed Yamux wire bytes are not payload plus 12")
    elif any(value.get(key) != 0 for key in
             ("envelope_token", "stream_id", "envelope_payload_bytes", "envelope_wire_bytes")):
        issues.append(f"{path} unattributed TLS/control record retains DATA identity")
    wall_start, wall_finish = value.get("raw_write_started_unix_ns"), value.get("raw_write_finished_unix_ns")
    if exact_int64(wall_start) and exact_int64(wall_finish) and wall_finish < wall_start:
        issues.append(f"{path} raw-write wall coordinates regress")
    mono_start = value.get("raw_write_started_monotonic_ns")
    mono_finish = value.get("raw_write_finished_monotonic_ns")
    if value.get("raw_write_monotonic_valid") is True:
        if not (exact_uint(mono_start) and exact_uint(mono_finish) and mono_finish >= mono_start):
            issues.append(f"{path} raw-write owner-relative coordinates regress")
    elif mono_start or mono_finish:
        issues.append(f"{path} has raw-write monotonic coordinates without a valid marker")
    location = value.get("transition_location")
    if location not in HISTORY_TRANSITION_LOCATION_DOMAIN:
        issues.append(f"{path}.transition_location is outside the fixed domain")
    if value.get("transition_observed_across_write") is not (location == "across_write_observation"):
        issues.append(f"{path} across-write transition marker disagrees with its location")
    before = tcp_info(value.get("before"), f"{path}.before", issues, require_healthy=False)
    after = tcp_info(value.get("after"), f"{path}.after", issues, require_healthy=False)
    if value.get("bracket_valid") is True:
        tcp_info(value.get("before"), f"{path}.before", issues, require_healthy=True)
        tcp_info(value.get("after"), f"{path}.after", issues, require_healthy=True)
        if before.get("congestion_control") != after.get("congestion_control"):
            issues.append(f"{path} congestion-control identity changed within the bracket")
        if (before.get("capture_monotonic_valid") is not True
                or after.get("capture_monotonic_valid") is not True
                or value.get("raw_write_monotonic_valid") is not True
                or not exact_uint(mono_start) or not exact_uint(mono_finish)
                or not (before.get("capture_finished_monotonic_ns", UINT64_MAX) <= mono_start
                        <= mono_finish <= after.get("capture_started_monotonic_ns", 0))):
            issues.append(f"{path} TCP captures do not bracket the raw write")
    elif location != "" or value.get("transition_observed_across_write") is not False:
        issues.append(f"{path} invalid bracket retains a transition location")
    return value


def unavailable_raw_control_capture(value: Any, baseline: dict[str, Any]) -> bool:
    """Recognize a declared provider failure before either getsockopt ran."""
    if type(value) is not dict:
        return False
    zero_fields = (INFO_UINT8_FIELDS | INFO_UINT16_FIELDS | INFO_UINT32_FIELDS | INFO_UINT64_FIELDS) - {
        "capture_duration_ns", "capture_started_monotonic_ns", "capture_finished_monotonic_ns",
        "tcp_info_struct_size", "required_field_end_offset",
    }
    availability = value.get("availability")
    return (value.get("captured") is False and value.get("failure_stage") == "raw_control"
            and value.get("capture_monotonic_valid") is True
            and exact_int64(value.get("capture_started_unix_ns"), positive=True)
            and exact_int64(value.get("capture_finished_unix_ns"), positive=True)
            and exact_int64(value.get("failure_errno")) and value.get("failure_errno") >= 0
            and all(value.get(key) is False for key in (
                "tcp_info_attempted", "tcp_info_succeeded",
                "congestion_control_attempted", "congestion_control_succeeded"))
            and value.get("congestion_control") == ""
            and all(type(value.get(key)) is int and value[key] == 0 for key in zero_fields)
            and all(value.get(key) == baseline.get(key) for key in (
                "tcp_info_struct_size", "required_field_end_offset"))
            and type(availability) is dict and set(availability) == AVAILABILITY_FIELDS
            and all(availability[key] is False for key in AVAILABILITY_FIELDS))


def validate_threshold_history(value: Any, *, bind_tcp_info: Any, path: str) -> dict[str, Any]:
    issues: list[str] = []
    if type(value) is not dict:
        issues.append(f"{path} is not an object")
        value = {}
    if set(value) != HISTORY_FIELDS:
        issues.append(f"{path} fields changed")
    if type(value.get("schema_version")) is not int or value.get("schema_version") != 1:
        issues.append(f"{path}.schema_version is not exact integer one")
    for key in HISTORY_BOOL_FIELDS:
        if not exact_bool(value.get(key)):
            issues.append(f"{path}.{key} is not an exact boolean")
    for key in HISTORY_UINT_FIELDS:
        if not exact_uint(value.get(key)):
            issues.append(f"{path}.{key} is not a uint64")
    for key in ("baseline_threshold", "first_changed_threshold", "selected_pre_threshold"):
        if not exact_uint(value.get(key), UINT32_MAX):
            issues.append(f"{path}.{key} exceeds uint32")
    for key, domain in (("failure_reason", HISTORY_FAILURE_DOMAIN),
                        ("explanation_reason", HISTORY_EXPLANATION_REASON_DOMAIN),
                        ("explanation_status", HISTORY_STATUS_DOMAIN)):
        current = value.get(key)
        if type(current) is not str or current not in domain:
            issues.append(f"{path}.{key} is outside the fixed domain")
    sequence = value.get("selected_pre_sequence")
    if type(sequence) is not str or sequence != sequence.strip():
        issues.append(f"{path}.selected_pre_sequence is noncanonical")
    provider_duration = duration_bucket(
        value.get("provider_capture_duration"), f"{path}.provider_capture_duration", issues)
    shared_duration = duration_bucket(
        value.get("raw_provider_duration_shared_with_tcp_interval"),
        f"{path}.raw_provider_duration_shared_with_tcp_interval", issues)
    baseline = tcp_info(value.get("baseline"), f"{path}.baseline", issues, require_healthy=True)
    if baseline != bind_tcp_info:
        issues.append(f"{path}.baseline differs from the enclosing TCP bind capture")
    baseline_threshold = value.get("baseline_threshold")
    if baseline.get("send_slow_start_threshold") != baseline_threshold:
        issues.append(f"{path}.baseline_threshold differs from the baseline TCP_INFO")
    if value.get("pre_bind_unobserved") is not (baseline_threshold != (1 << 31) - 1):
        issues.append(f"{path}.pre_bind_unobserved differs from finite bind threshold state")
    selected_pre = value.get("selected_pre_tcp_info")
    if value.get("selected_pre_compared") is True:
        selected_pre = tcp_info(selected_pre, f"{path}.selected_pre_tcp_info", issues, require_healthy=True)
        if not sequence or not exact_uint(value.get("selected_pre_arm_ordinal"), positive=True):
            issues.append(f"{path} selected-pre comparison lacks sequence or positive arm ordinal")
        if selected_pre.get("send_slow_start_threshold") != value.get("selected_pre_threshold"):
            issues.append(f"{path}.selected_pre_threshold differs from selected-pre TCP_INFO")
    else:
        if not absent_tcp_info(selected_pre):
            issues.append(f"{path}.selected_pre_tcp_info is not absent before comparison")
        if sequence or value.get("selected_pre_arm_ordinal") or value.get("selected_pre_threshold"):
            issues.append(f"{path} retains selected-pre identity before comparison")
    last_equal = history_record(value.get("last_equal"), f"{path}.last_equal", issues)
    first_different = history_record(value.get("first_different"), f"{path}.first_different", issues)
    failure = history_record(value.get("failure"), f"{path}.failure", issues)
    baseline_cc = baseline.get("congestion_control")
    for label, row in (("last_equal", last_equal), ("first_different", first_different), ("failure", failure)):
        if row.get("present") is True:
            for endpoint in ("before", "after"):
                info = row.get(endpoint, {})
                if type(info) is dict and info.get("schema_version") == 1:
                    capture_path = f"{path}.{label}.{endpoint}"
                    if info.get("captured") is True:
                        prior_capture_issues = len(issues)
                        tcp_info(info, capture_path, issues, require_healthy=True)
                        if len(issues) == prior_capture_issues and info.get("congestion_control") != baseline_cc:
                            issues.append(f"{capture_path} congestion control differs from baseline")
                    elif (label != "failure" or value.get("inconclusive") is not True
                          or not exact_uint(value.get("provider_capture_errors"), positive=True)
                          or not unavailable_raw_control_capture(info, baseline)):
                        issues.append(f"{capture_path} unavailable capture lacks declared failure evidence")
            if row.get("raw_write_ordinal", 0) > value.get("raw_write_ordinal", 0):
                issues.append(f"{path}.{label} ordinal exceeds the raw-write cursor")
            if row.get("raw_write_ordinal", 0) > value.get("monitored_raw_write_attempts", 0):
                issues.append(f"{path}.{label} ordinal exceeds monitored raw-write attempts")
    attempts = value.get("monitored_raw_write_attempts")
    calls = value.get("provider_capture_calls")
    errors = value.get("provider_capture_errors")
    shared_calls = value.get("raw_provider_calls_shared_with_tcp_interval")
    if exact_uint(attempts) and exact_uint(calls):
        expected_calls = 2 * attempts
        if ((value.get("inconclusive") is False and calls != expected_calls)
                or (value.get("inconclusive") is True and calls > expected_calls)):
            issues.append(f"{path} provider calls do not conserve monitored raw writes")
    if (exact_uint(errors) and exact_uint(calls) and errors > calls) or provider_duration.get("count") != calls:
        issues.append(f"{path} provider error/duration counters do not conserve calls")
    if (not exact_uint(shared_calls) or not exact_uint(calls) or shared_calls > calls
            or shared_duration.get("count") != shared_calls
            or shared_duration.get("total_nanoseconds", 0) > provider_duration.get("total_nanoseconds", 0)
            or shared_duration.get("max_nanoseconds", 0) > provider_duration.get("max_nanoseconds", 0)):
        issues.append(f"{path} shared provider subset exceeds total provider work")
    if exact_uint(attempts) and exact_uint(value.get("raw_write_ordinal")) and attempts > value.get("raw_write_ordinal"):
        issues.append(f"{path} monitored attempts exceed the raw-write ordinal")
    if not exact_uint(attempts) or attempts > RECORD_LIMIT:
        issues.append(f"{path}.monitored_raw_write_attempts exceeds the fixed record limit")
    if value.get("enabled") is not True:
        issues.append(f"{path}.enabled is not true")
    if value.get("pre_bind_unobserved") is not (value.get("failure_reason") == "pre_bind_unobserved"):
        issues.append(f"{path}.pre_bind_unobserved differs from its exact failure reason")
    exhausted_reason = value.get("failure_reason") in {"attempt_limit", "counter_overflow"}
    if value.get("exhausted") is not exhausted_reason:
        issues.append(f"{path}.exhausted differs from the exact limit/overflow failure state")
    if value.get("failure_reason") == "attempt_limit" and attempts != RECORD_LIMIT:
        issues.append(f"{path} attempt-limit failure did not occur at the fixed record limit")
    if value.get("active_sample") is True and value.get("complete") is True:
        issues.append(f"{path} complete history retains an active sample")
    transition = value.get("transition_observed") is True
    expected_monitoring = not transition and value.get("inconclusive") is False and value.get("closed") is False
    if value.get("monitoring") is not expected_monitoring:
        issues.append(f"{path}.monitoring disagrees with transition/failure/closed state")
    if exact_uint(errors) and errors != 0 and value.get("inconclusive") is not True:
        issues.append(f"{path} healthy history retains provider capture errors")
    if transition:
        if first_different.get("present") is not True or not value.get("first_changed_threshold"):
            issues.append(f"{path} transition lacks the retained first-different record")
        elif value.get("first_changed_threshold") == baseline_threshold:
            issues.append(f"{path} first changed threshold still equals baseline")
        if value.get("monitoring") is not False:
            issues.append(f"{path} keeps monitoring after its first retained transition")
    elif first_different.get("present") is True or value.get("first_changed_threshold") != 0:
        issues.append(f"{path} retains a first transition without its observed marker")
    if last_equal.get("present") is True:
        if (last_equal.get("bracket_valid") is not True or last_equal.get("write_error") is not False
                or last_equal.get("short_write") is not False
                or last_equal.get("before", {}).get("send_slow_start_threshold") != baseline_threshold
                or last_equal.get("after", {}).get("send_slow_start_threshold") != baseline_threshold
                or last_equal.get("transition_location") != ""):
            issues.append(f"{path}.last_equal is not an exact successful equal-threshold bracket")
    if first_different.get("present") is True:
        before_threshold = first_different.get("before", {}).get("send_slow_start_threshold")
        after_threshold = first_different.get("after", {}).get("send_slow_start_threshold")
        location = first_different.get("transition_location")
        expected_changed = after_threshold if location == "across_write_observation" else before_threshold
        if (first_different.get("bracket_valid") is not True
                or first_different.get("write_error") is not False
                or first_different.get("short_write") is not False
                or location not in HISTORY_TRANSITION_LOCATION_DOMAIN - {""}
                or expected_changed != value.get("first_changed_threshold")):
            issues.append(f"{path}.first_different does not encode the earliest successful difference")
        if (location == "across_write_observation"
                and (before_threshold != baseline_threshold or after_threshold == baseline_threshold)):
            issues.append(f"{path}.first_different across-write location contradicts its thresholds")
        if (location in {"between_bind_and_first_write", "between_writes"}
                and before_threshold == baseline_threshold):
            issues.append(f"{path}.first_different between-write location lacks a changed before capture")
        if location == "between_bind_and_first_write" and last_equal.get("present") is True:
            issues.append(f"{path}.first_different claims pre-first-write after a retained equal write")
        if location == "between_writes" and last_equal.get("present") is not True:
            issues.append(f"{path}.first_different claims between writes without a retained equal write")
        if last_equal.get("present") is True and last_equal.get("raw_write_ordinal", 0) >= first_different.get("raw_write_ordinal", 0):
            issues.append(f"{path}.last_equal does not precede first_different")
    if value.get("inconclusive") is True:
        if not value.get("failure_reason") or value.get("monitoring") is not False:
            issues.append(f"{path} inconclusive state lacks its sticky failure or stopped monitor")
    elif value.get("failure_reason") or failure.get("present") is True:
        issues.append(f"{path} healthy history retains failure evidence")
    if value.get("explanation_available") is not value.get("selected_pre_matched"):
        issues.append(f"{path}.explanation_available differs from selected-pre match")
    if (value.get("selected_pre_compared") is True and transition is False
            and value.get("inconclusive") is False
            and value.get("selected_pre_initial") is not True):
        issues.append(f"{path} healthy no-transition comparison is not the exact initial state")
    if value.get("selected_pre_initial") is True:
        if (value.get("selected_pre_compared") is not True
                or value.get("selected_pre_threshold") != baseline_threshold
                or value.get("selected_pre_matched") is True):
            issues.append(f"{path}.selected_pre_initial contradicts its selected-pre comparison")
    if value.get("selected_pre_matched") is True:
        decisive = first_different.get("after") if first_different.get("transition_location") == "across_write_observation" else first_different.get("before")
        decisive = decisive if type(decisive) is dict else {}
        if (not transition or value.get("selected_pre_compared") is not True
                or value.get("selected_pre_threshold") != value.get("first_changed_threshold")
                or value.get("explanation_status") != "selected_pre_matched"
                or value.get("explanation_available") is not True
                or value.get("explanation_inconclusive") is not False
                or value.get("explanation_reason") != ""
                or decisive.get("capture_monotonic_valid") is not True
                or selected_pre.get("capture_monotonic_valid") is not True
                or decisive.get("capture_finished_monotonic_ns", UINT64_MAX)
                   > selected_pre.get("capture_started_monotonic_ns", 0)):
            issues.append(f"{path} selected-pre match lacks exact transition identity and strict chronology")
    if value.get("explanation_inconclusive") is True:
        if (transition is not True or value.get("selected_pre_compared") is not True
                or first_different.get("present") is not True
                or value.get("selected_pre_initial") is not False):
            issues.append(f"{path} explanation-inconclusive state lacks a compared retained transition")
        if value.get("explanation_reason") not in HISTORY_EXPLANATION_REASON_DOMAIN - {""}:
            issues.append(f"{path} explanation-inconclusive state lacks a fixed reason")
        if value.get("explanation_available") is not False or value.get("selected_pre_matched") is not False:
            issues.append(f"{path} explanation cannot be both available and inconclusive")
        if (value.get("explanation_reason") == "selected_pre_mismatch"
                and value.get("selected_pre_threshold") == value.get("first_changed_threshold")):
            issues.append(f"{path} selected-pre mismatch reason retains an equal threshold")
        if value.get("explanation_reason") == "selected_pre_transition_not_strictly_prior":
            decisive = first_different.get("after") if first_different.get("transition_location") == "across_write_observation" else first_different.get("before")
            decisive = decisive if type(decisive) is dict else {}
            if (value.get("selected_pre_threshold") != value.get("first_changed_threshold")
                    or decisive.get("capture_monotonic_valid") is not True
                    or selected_pre.get("capture_monotonic_valid") is not True
                    or decisive.get("capture_finished_monotonic_ns", 0)
                       <= selected_pre.get("capture_started_monotonic_ns", UINT64_MAX)):
                issues.append(f"{path} strict-prior reason does not encode overlap or reversed chronology")
    elif value.get("explanation_reason"):
        issues.append(f"{path} retains an explanation reason without its inconclusive marker")
    if (transition and value.get("selected_pre_compared") is True
            and value.get("inconclusive") is False
            and sum(flag is True for flag in (
                value.get("selected_pre_initial"), value.get("selected_pre_matched"),
                value.get("explanation_inconclusive"))) != 1):
        issues.append(f"{path} compared retained transition lacks one exact explanation state")
    expected_status = "monitoring"
    if value.get("inconclusive") is True:
        expected_status = "inconclusive"
    elif value.get("selected_pre_matched") is True:
        expected_status = "selected_pre_matched"
    elif value.get("selected_pre_initial") is True and transition:
        expected_status = "transition_observed_with_initial_pre"
    elif value.get("selected_pre_initial") is True:
        expected_status = "not_reproduced"
    elif value.get("explanation_inconclusive") is True:
        expected_status = value.get("explanation_reason")
    elif transition:
        expected_status = "transition_observed"
    elif value.get("closed") is True and value.get("required") is False:
        expected_status = "not_required_no_transition"
    if value.get("explanation_status") != expected_status:
        issues.append(f"{path}.explanation_status disagrees with its exact state precedence")
    if value.get("complete") is not (
            value.get("closed") is True and value.get("active_sample") is False
            and value.get("monitoring") is False and value.get("inconclusive") is False):
        issues.append(f"{path}.complete disagrees with closed/active/monitoring/inconclusive state")
    return {
        "schema_version": 1,
        "accepted": not issues,
        "issues": issues,
        "explanation_available": value.get("explanation_available") is True,
        "explanation_inconclusive": value.get("explanation_inconclusive") is True,
        "explanation_status": value.get("explanation_status"),
        "selected_pre_sequence": value.get("selected_pre_sequence"),
        "selected_pre_arm_ordinal": value.get("selected_pre_arm_ordinal"),
        "persistent_inconclusive": value.get("inconclusive") is True,
        "provider_capture_calls": calls,
        "provider_capture_errors": errors,
        "provider_capture_duration": provider_duration,
        "shared_provider_capture_calls": shared_calls,
        "shared_provider_capture_duration": shared_duration,
        "history": value,
    }


def validate_threshold_history_owner(history_result: dict[str, Any], owner_identity: Any, *,
                                     path: str) -> None:
    issues = history_result.get("issues", [])
    history = history_result.get("history", {})
    if type(owner_identity) is not dict:
        issues.append(f"{path} owner identity is not an object")
        return
    expected = {
        "owner_lane": owner_identity.get("lane"),
        "owner_generation": owner_identity.get("generation"),
        "local_address": owner_identity.get("local_address"),
        "remote_address": owner_identity.get("remote_address"),
    }
    if type(expected["owner_lane"]) is not int or expected["owner_lane"] not in range(5):
        issues.append(f"{path} owner lane is outside the exact five-lane domain")
    if type(history) is dict and history.get("required") is not (expected["owner_lane"] != 0):
        issues.append(f"{path}.required differs from the owner lane role")
    if not exact_uint(expected["owner_generation"], positive=True):
        issues.append(f"{path} owner generation is not positive")
    for key in ("local_address", "remote_address"):
        current = expected[key]
        if (type(current) is not str or not current or current != current.strip()
                or current in {"yamux:local", "yamux:remote"}):
            issues.append(f"{path} owner {key} is empty, noncanonical, or a Yamux placeholder")
    for label in ("last_equal", "first_different", "failure"):
        record = history.get(label, {}) if type(history) is dict else {}
        if type(record) is not dict or record.get("present") is not True:
            continue
        for key, wanted in expected.items():
            if record.get(key) != wanted:
                issues.append(f"{path}.{label}.{key} differs from the enclosing raw owner")
    history_result["accepted"] = not issues


def validate_threshold_history_progression(pre_value: Any, post_value: Any, *,
                                           sequence: int, path: str) -> dict[str, Any]:
    issues: list[str] = []
    pre_tcp = pre_value if type(pre_value) is dict else {}
    post_tcp = post_value if type(post_value) is dict else {}
    pre = pre_tcp.get("threshold_history", {}) if type(pre_tcp.get("threshold_history")) is dict else {}
    post = post_tcp.get("threshold_history", {}) if type(post_tcp.get("threshold_history")) is dict else {}
    if type(pre_value) is not dict or type(post_value) is not dict:
        issues.append(f"{path} pre/post TCP-state snapshots are not objects")
    if post.get("selected_pre_sequence") != str(sequence):
        issues.append(f"{path} post selected-pre sequence differs from the armed interval")
    pre_arm, post_arm = pre.get("selected_pre_arm_ordinal"), post.get("selected_pre_arm_ordinal")
    if (not exact_uint(pre_arm) or not exact_uint(post_arm, positive=True)
            or post_arm != pre_arm + 1):
        issues.append(f"{path} selected-pre arm ordinal did not advance exactly once")
    for key in ("required", "baseline", "baseline_threshold", "pre_bind_unobserved"):
        if pre.get(key) != post.get(key):
            issues.append(f"{path} persistent {key} changed across the interval")
    for key in ("raw_write_ordinal", "monitored_raw_write_attempts", "provider_capture_calls",
                "provider_capture_errors", "raw_provider_calls_shared_with_tcp_interval"):
        before, after = pre.get(key), post.get(key)
        if not exact_uint(before) or not exact_uint(after) or after < before:
            issues.append(f"{path} cumulative {key} regressed")
    for key in ("provider_capture_duration", "raw_provider_duration_shared_with_tcp_interval"):
        before, after = pre.get(key, {}), post.get(key, {})
        if type(before) is not dict or type(after) is not dict:
            issues.append(f"{path} cumulative {key} is not an object")
            continue
        for field in BUCKET_FIELDS:
            if (not exact_uint(before.get(field)) or not exact_uint(after.get(field))
                    or after.get(field) < before.get(field)):
                issues.append(f"{path} cumulative {key}.{field} regressed")
    pre_last = pre.get("last_equal") if type(pre.get("last_equal")) is dict else {}
    post_last = post.get("last_equal") if type(post.get("last_equal")) is dict else {}
    if pre_last.get("present") is True:
        if (post_last.get("present") is not True
                or not exact_uint(post_last.get("raw_write_ordinal"), positive=True)
                or post_last.get("raw_write_ordinal") < pre_last.get("raw_write_ordinal")):
            issues.append(f"{path} last-equal record regressed or disappeared")
    retained_stop = pre.get("transition_observed") is True or pre.get("inconclusive") is True
    if pre.get("transition_observed") is True:
        for key in ("transition_observed", "first_changed_threshold", "first_different", "last_equal"):
            if post.get(key) != pre.get(key):
                issues.append(f"{path} retained first-transition {key} changed")
    if pre.get("inconclusive") is True:
        for key in ("inconclusive", "failure_reason", "failure", "transition_observed",
                    "first_changed_threshold", "first_different", "last_equal"):
            if post.get(key) != pre.get(key):
                issues.append(f"{path} sticky history failure {key} changed")
    if retained_stop:
        for key in ("monitored_raw_write_attempts", "provider_capture_calls",
                    "provider_capture_errors", "provider_capture_duration",
                    "raw_provider_calls_shared_with_tcp_interval",
                    "raw_provider_duration_shared_with_tcp_interval"):
            if post.get(key) != pre.get(key):
                issues.append(f"{path} cumulative {key} changed after retained transition/failure")
    if pre.get("closed") is True or post.get("closed") is True:
        issues.append(f"{path} live pre/post interval contains a closed threshold history")
    return {"schema_version": 1, "accepted": not issues, "issues": issues}


def validate_activity(value: Any, path: str = "tcp_state") -> dict[str, Any]:
    issues: list[str] = []
    if type(value) is not dict:
        issues.append(f"{path} is not an object")
        value = {}
    if set(value) != ACTIVITY_FIELDS:
        issues.append(f"{path} fields changed")
    if type(value.get("schema_version")) is not int or value.get("schema_version") != 1:
        issues.append(f"{path}.schema_version is not exact integer one")
    for key in ACTIVITY_BOOL_FIELDS:
        if not exact_bool(value.get(key)):
            issues.append(f"{path}.{key} is not an exact boolean")
    for key in ACTIVITY_UINT_FIELDS:
        if not exact_uint(value.get(key)):
            issues.append(f"{path}.{key} is not a uint64")
    if type(value.get("sequence")) is not str or value.get("sequence") != value.get("sequence", "").strip():
        issues.append(f"{path}.sequence is noncanonical")
    if type(value.get("tcp_slow_start_after_idle")) is not int or value.get("tcp_slow_start_after_idle") not in (0, 1):
        issues.append(f"{path}.tcp_slow_start_after_idle is outside the fixed sysctl domain")
    capture_duration = duration_bucket(value.get("capture_duration"), f"{path}.capture_duration", issues)
    bind = tcp_info(value.get("bind_tcp_info"), f"{path}.bind_tcp_info", issues, require_healthy=False)
    last_live = tcp_info(value.get("last_live_tcp_info"), f"{path}.last_live_tcp_info", issues, require_healthy=False)
    empty_boundary = value.get("sequence") == ""
    pre = tcp_info(value.get("pre_tcp_info"), f"{path}.pre_tcp_info", issues,
                   require_healthy=False, allow_absent=empty_boundary)
    post = tcp_info(value.get("post_tcp_info"), f"{path}.post_tcp_info", issues,
                    require_healthy=False, allow_absent=empty_boundary)
    history = validate_threshold_history(
        value.get("threshold_history"), bind_tcp_info=bind,
        path=f"{path}.threshold_history")
    issues.extend(history["issues"])
    waits = value.get("wait_records")
    pairs = value.get("raw_pairs")
    if type(waits) is not list or len(waits) > RECORD_LIMIT:
        issues.append(f"{path}.wait_records is missing or exceeds {RECORD_LIMIT}")
        waits = []
    if type(pairs) is not list or len(pairs) > RECORD_LIMIT:
        issues.append(f"{path}.raw_pairs is missing or exceeds {RECORD_LIMIT}")
        pairs = []
    parsed_waits = [wait_record(row, f"{path}.wait_records[{index}]", issues)
                    for index, row in enumerate(waits)]
    parsed_pairs = [raw_pair(row, f"{path}.raw_pairs[{index}]", issues)
                    for index, row in enumerate(pairs)]
    wait_ids = [row.get("record_id") for row in parsed_waits]
    pair_ids = [row.get("record_id") for row in parsed_pairs]
    valid_wait_ids = [record_id for record_id in wait_ids if exact_uint(record_id, positive=True)]
    valid_pair_ids = [record_id for record_id in pair_ids if exact_uint(record_id, positive=True)]
    if len(valid_wait_ids) != len(wait_ids) or any(
            left >= right for left, right in zip(valid_wait_ids, valid_wait_ids[1:])):
        issues.append(f"{path} wait record IDs are not positive and strictly increasing")
    if len(valid_pair_ids) != len(pair_ids) or any(
            left >= right for left, right in zip(valid_pair_ids, valid_pair_ids[1:])):
        issues.append(f"{path} raw-pair record IDs are not positive and strictly increasing")
    if len(valid_wait_ids) + len(valid_pair_ids) != len(parsed_waits) + len(parsed_pairs) or len(
            set(valid_wait_ids + valid_pair_ids)) != len(valid_wait_ids) + len(valid_pair_ids):
        issues.append(f"{path} wait/raw-pair record IDs are not globally unique")
    wait_tokens = [(row.get("stream_id"), row.get("wait_token")) for row in parsed_waits
                   if exact_uint(row.get("stream_id"), UINT32_MAX, positive=True)
                   and exact_uint(row.get("wait_token"), positive=True)]
    if len(wait_tokens) != len(parsed_waits) or len(set(wait_tokens)) != len(wait_tokens):
        issues.append(f"{path} wait stream/token identity is repeated")
    sample_ids = [sample_value.get("sample_id") for row in parsed_waits
                  for sample_value in (row.get("entry", {}), row.get("retry", {}))]
    valid_sample_ids = [sample_id for sample_id in sample_ids if exact_uint(sample_id, positive=True)]
    if len(valid_sample_ids) != len(sample_ids) or len(set(valid_sample_ids)) != len(valid_sample_ids):
        issues.append(f"{path} sample IDs are not positive and globally unique")
    for index, row in enumerate(parsed_waits):
        entry_id = row.get("entry", {}).get("sample_id")
        retry_id = row.get("retry", {}).get("sample_id")
        if not (exact_uint(entry_id, positive=True) and exact_uint(retry_id, positive=True)
                and entry_id < retry_id):
            issues.append(f"{path}.wait_records[{index}] entry sample does not precede retry sample")
    envelope_tokens = [row.get("envelope_token") for row in parsed_pairs
                       if exact_uint(row.get("envelope_token"), positive=True)]
    if len(envelope_tokens) != len(parsed_pairs) or len(set(envelope_tokens)) != len(envelope_tokens):
        issues.append(f"{path} raw-pair envelope token is repeated")
    wait_by_id = {row.get("record_id"): row for row in parsed_waits
                  if exact_uint(row.get("record_id"), positive=True)}
    pair_by_id = {row.get("record_id"): row for row in parsed_pairs
                  if exact_uint(row.get("record_id"), positive=True)}
    for row in parsed_waits:
        pair_id = row.get("raw_pair_record_id")
        if exact_uint(pair_id, positive=True):
            pair = pair_by_id.get(pair_id)
            if pair is None:
                issues.append(f"{path} applied wait lacks its exact raw pair")
            elif (pair.get("wait_record_id"), pair.get("stream_id"), pair.get("wait_token")) != (
                    row.get("record_id"), row.get("stream_id"), row.get("wait_token")):
                issues.append(f"{path} wait/raw-pair identity does not join")
            elif pair.get("record_id", 0) <= row.get("record_id", 0):
                issues.append(f"{path} raw-pair record does not follow its wait record")
    for row in parsed_pairs:
        wait_record_id = row.get("wait_record_id")
        wait = wait_by_id.get(wait_record_id) if exact_uint(wait_record_id, positive=True) else None
        if wait is None or wait.get("raw_pair_record_id") != row.get("record_id"):
            issues.append(f"{path} raw pair lacks a unique reverse wait binding")
    expected_calls = (2 if value.get("sequence") else 0) + 2 * len(parsed_waits) + 2 * len(parsed_pairs)
    if value.get("capture_calls") != expected_calls or capture_duration.get("count") != expected_calls:
        issues.append(f"{path} capture-call count does not conserve pre/post, wait, and raw-pair samples")
    captured_infos: list[dict[str, Any]] = []
    if value.get("sequence"):
        captured_infos.extend((pre, post))
    for row in parsed_waits:
        captured_infos.extend((row.get("entry", {}).get("tcp_info", {}),
                               row.get("retry", {}).get("tcp_info", {})))
    for row in parsed_pairs:
        captured_infos.extend((row.get("before", {}), row.get("after", {})))
    captured_durations = [row.get("capture_duration_ns") for row in captured_infos if type(row) is dict]
    if (len(captured_durations) != expected_calls
            or any(not exact_uint(duration) for duration in captured_durations)
            or capture_duration.get("total_nanoseconds") != sum(
                duration for duration in captured_durations if exact_uint(duration))
            or capture_duration.get("max_nanoseconds") != max(
                (duration for duration in captured_durations if exact_uint(duration)), default=0)):
        issues.append(f"{path} capture duration bucket does not conserve exact sampled durations")
    bind_congestion = bind.get("congestion_control")
    if value.get("sequence") and any(type(row) is not dict or row.get("congestion_control") != bind_congestion
                                     for row in captured_infos):
        issues.append(f"{path} sampled congestion-control identity differs from bind capture")
    if last_live.get("congestion_control") != bind_congestion:
        issues.append(f"{path} last-live congestion-control identity differs from bind capture")
    if value.get("capture_errors") != 0:
        issues.append(f"{path}.capture_errors is nonzero")
    if value.get("complete") != value.get("reset_performed"):
        issues.append(f"{path} complete/reset flags differ")
    if value.get("complete") is True and any(value.get(key) for key in
            ("active_samples", "pending_raw_claims", "active_raw_pairs")):
        issues.append(f"{path} completed snapshot retains active state")
    if value.get("invalid") is True or value.get("ambiguous") is True or value.get("overflow") is True:
        issues.append(f"{path} is invalid, ambiguous, or overflowed")
    if value.get("dropped_wait_records") or value.get("dropped_raw_pair_records"):
        issues.append(f"{path} dropped bounded records")
    if (value.get("sequence") and len({bind.get("congestion_control"), last_live.get("congestion_control"),
            pre.get("congestion_control"), post.get("congestion_control")}) != 1):
        issues.append(f"{path} congestion-control identity changed across bind/pre/post")
    return {
        "schema_version": 1, "accepted": not issues, "issues": issues,
        "wait_records": parsed_waits, "raw_pairs": parsed_pairs,
        "capture_calls": value.get("capture_calls"), "tcp_slow_start_after_idle": value.get("tcp_slow_start_after_idle"),
        "threshold_history": history,
    }


def validate_live(value: Any, *, sequence: int | None = None, selected_stream_id: int | None = None,
                  selected_waits: Any = None, owner_identity: Any = None,
                  path: str = "tcp_state") -> dict[str, Any]:
    result = validate_activity(value, path)
    issues = result["issues"]
    normalized = value if type(value) is dict else {}
    for key in ("bind_tcp_info", "last_live_tcp_info", "pre_tcp_info", "post_tcp_info"):
        tcp_info(normalized.get(key), f"{path}.{key}", issues, require_healthy=True)
    if normalized.get("last_live_tcp_info") != normalized.get("post_tcp_info"):
        issues.append(f"{path}.last_live_tcp_info is not the final post capture")
    for key in ("enabled", "bound", "complete", "reset_performed"):
        if normalized.get(key) is not True:
            issues.append(f"{path}.{key} is not true")
    if normalized.get("armed") is not False:
        issues.append(f"{path}.armed is not false after drain")
    if sequence is not None and normalized.get("sequence") != str(sequence):
        issues.append(f"{path}.sequence differs from selected interval")
    history = result.get("threshold_history", {})
    history_value = history.get("history", {}) if type(history) is dict else {}
    if owner_identity is not None:
        prior_history_issue_count = len(history.get("issues", []))
        validate_threshold_history_owner(
            history, owner_identity,
            path=f"{path}.threshold_history")
        issues.extend(history.get("issues", [])[prior_history_issue_count:])
    if ((sequence is not None or selected_stream_id is not None)
            and history_value.get("required") is not True):
        issues.append(f"{path}.threshold_history.required is not true for the selected DATA lane")
    if history_value.get("active_sample") is not False or history_value.get("closed") is not False:
        issues.append(f"{path}.threshold_history is active or closed at a live post boundary")
    if sequence is not None:
        if history_value.get("selected_pre_sequence") != str(sequence):
            issues.append(f"{path}.threshold_history selected-pre sequence differs from the post interval")
        if not exact_uint(history_value.get("selected_pre_arm_ordinal"), positive=True):
            issues.append(f"{path}.threshold_history selected-pre arm ordinal is not positive")
        if history_value.get("selected_pre_tcp_info") != normalized.get("pre_tcp_info"):
            issues.append(f"{path}.threshold_history selected-pre TCP_INFO differs from the post interval pre capture")
    for key in ("active_samples", "pending_raw_claims", "active_raw_pairs", "capture_errors",
                "dropped_wait_records", "dropped_raw_pair_records"):
        if normalized.get(key) != 0:
            issues.append(f"{path}.{key} is nonzero")
    if selected_stream_id is not None:
        if not exact_uint(selected_stream_id, UINT32_MAX, positive=True):
            issues.append(f"{path} selected stream ID is invalid")
        for index, row in enumerate(result["wait_records"]):
            if row.get("stream_id") != selected_stream_id:
                issues.append(f"{path}.wait_records[{index}] differs from selected stream")
        for index, row in enumerate(result["raw_pairs"]):
            if row.get("stream_id") != selected_stream_id:
                issues.append(f"{path}.raw_pairs[{index}] differs from selected stream")
    if selected_waits is not None:
        if type(selected_waits) is not list:
            issues.append(f"{path} selected byte-credit waits are not a list")
            selected_waits = []
        expected = {(row.get("token"), row.get("stream_id"), row.get("classification"))
                    for row in selected_waits if type(row) is dict
                    and exact_uint(row.get("token"), positive=True)
                    and exact_uint(row.get("stream_id"), UINT32_MAX, positive=True)
                    and type(row.get("classification")) is str}
        actual = {(row.get("wait_token"), row.get("stream_id"), row.get("retry", {}).get("classification"))
                  for row in result["wait_records"]}
        if actual != expected or len(actual) != len(result["wait_records"]) or len(expected) != len(selected_waits):
            issues.append(f"{path} wait token/stream/classification set differs from byte-credit ledger")
        selected_by_identity = {(row.get("token"), row.get("stream_id")): row
                                for row in selected_waits if type(row) is dict
                                and exact_uint(row.get("token"), positive=True)
                                and exact_uint(row.get("stream_id"), UINT32_MAX, positive=True)}
        for index, row in enumerate(result["wait_records"]):
            byte_wait = selected_by_identity.get((row.get("wait_token"), row.get("stream_id")))
            if type(byte_wait) is not dict:
                continue
            entry = row.get("entry", {})
            retry = row.get("retry", {})
            if (byte_wait.get("classification") == "applied_range"
                    and not exact_uint(byte_wait.get("retry_byte_credit"), UINT32_MAX, positive=True)):
                issues.append(f"{path}.wait_records[{index}] selected applied-range retry has zero byte credit")
            for sample_value, prefix in ((entry, "entry"), (retry, "retry")):
                byte_ordinal = byte_wait.get(f"{prefix}_ordinal")
                byte_cumulative = byte_wait.get(f"{prefix}_cumulative")
                byte_credit = byte_wait.get(f"{prefix}_byte_credit")
                sample_ordinal = sample_value.get("cursor_before_ordinal")
                sample_cumulative = sample_value.get("cursor_before_cumulative")
                sample_credit = sample_value.get("credit")
                if (not exact_uint(byte_ordinal) or not exact_uint(byte_cumulative)
                        or not exact_uint(byte_credit, UINT32_MAX)
                        or not exact_uint(sample_ordinal) or not exact_uint(sample_cumulative)
                        or not exact_uint(sample_credit, UINT32_MAX)
                        or sample_ordinal < byte_ordinal or sample_cumulative < byte_cumulative
                        or sample_credit < byte_credit):
                    issues.append(f"{path}.wait_records[{index}].{prefix} precedes its byte-credit lower bound")
                byte_observed = byte_wait.get(f"{prefix}_observed_unix_ns")
                tcp_observed = sample_value.get("observed_unix_ns")
                if (not exact_int64(byte_observed, positive=True) or not exact_int64(tcp_observed, positive=True)
                        or tcp_observed < byte_observed):
                    issues.append(f"{path}.wait_records[{index}].{prefix} precedes its local byte-credit observation")
    result["accepted"] = not issues
    return result


def validate_no_data(value: Any, *, sequence: str, path: str = "tcp_state") -> dict[str, Any]:
    result = validate_activity(value, path)
    issues = result["issues"]
    normalized = value if type(value) is dict else {}
    if normalized.get("sequence") != sequence:
        issues.append(f"{path}.sequence differs from boundary")
    if result["wait_records"] or result["raw_pairs"]:
        issues.append(f"{path} contains selected records at a no-DATA boundary")
    for key in ("active_samples", "pending_raw_claims", "active_raw_pairs", "capture_errors",
                "dropped_wait_records", "dropped_raw_pair_records"):
        if normalized.get(key) != 0:
            issues.append(f"{path}.{key} is nonzero")
    result["accepted"] = not issues
    return result


def validate_boundary(value: Any, *, path: str = "tcp_state", terminal: bool = False,
                      terminal_sequence: str = "15", owner_identity: Any = None) -> dict[str, Any]:
    result = validate_no_data(value, sequence="", path=path)
    issues = result["issues"]
    normalized = value if type(value) is dict else {}
    for key in ("bind_tcp_info", "last_live_tcp_info"):
        tcp_info(normalized.get(key), f"{path}.{key}", issues, require_healthy=True)
    for key in ("enabled", "bound", "complete", "reset_performed"):
        if normalized.get(key) is not True:
            issues.append(f"{path}.{key} is not true at final closure")
    if normalized.get("armed") is not False:
        issues.append(f"{path}.armed is not false at final closure")
    if normalized.get("invalid") is not False:
        issues.append(f"{path}.invalid is not false at final closure")
    history = result.get("threshold_history", {})
    history_value = history.get("history", {}) if type(history) is dict else {}
    if owner_identity is not None:
        prior_history_issue_count = len(history.get("issues", []))
        validate_threshold_history_owner(
            history, owner_identity,
            path=f"{path}.threshold_history")
        issues.extend(history.get("issues", [])[prior_history_issue_count:])
    if terminal:
        if history_value.get("closed") is not True or history_value.get("active_sample") is not False:
            issues.append(f"{path}.threshold_history is not closed and inactive at terminal closure")
        if history_value.get("selected_pre_sequence") != terminal_sequence:
            issues.append(f"{path}.threshold_history does not retain the terminal selected-pre sequence")
        if history_value.get("required") is True:
            coherent = (history_value.get("complete") is True
                        or (history_value.get("inconclusive") is True
                            and bool(history_value.get("failure_reason"))))
            if not coherent:
                issues.append(f"{path}.threshold_history required terminal state is neither complete nor declared inconclusive")
        elif history_value.get("complete") is not True:
            issues.append(f"{path}.threshold_history control terminal state is incomplete")
    result["accepted"] = not issues
    return result


validate_final = validate_boundary

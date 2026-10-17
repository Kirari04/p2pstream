#!/usr/bin/env python3
"""Strict offline validator for the server-side selected receive-growth epoch."""

import json
import math
from pathlib import Path
from typing import Any

UINT32_MAX = (1 << 32) - 1
UINT64_MAX = (1 << 64) - 1
MAX_RESPONSE_BYTES = 4 << 20
FIXED_SEQUENCES = (9, 11, 14, 15)
CAPACITY_FIELDS = {
    "schema_version", "sequence", "observed_unix_ns", "generation", "adaptive",
    "adaptive_admission_role", "adaptive_admission_limit", "adaptive_raw_admission_limit",
    "adaptive_public_admission_limit", "reject_new", "memory_pressure", "memory_used_bytes",
    "memory_limit_bytes", "memory_source", "memory_headroom_to_hard_bytes",
    "file_descriptors_used", "file_descriptors_limit", "resource_pressure_reason",
    "resource_sample_error", "resource_sampled_unix_ns", "resource_next_sample_unix_ns",
    "resource_last_good_unix_ns", "server_receive_growth",
}
GROWTH_FIELDS = {
    "schema_version", "sequence", "phase", "state_before", "state_after",
    "captured_unix_ns", "capacity", "dropped_events", "events", "send_counters",
    "credit_snapshot", "byte_credit_traces",
}
EVENT_REQUIRED = {
    "sample_unix_nano", "lane", "session_identity", "stream_id", "aggregate", "current",
    "proposed", "maximum", "rtt_nanoseconds", "rate_bytes_per_second", "previous",
    "denied_until_unix_nano", "denial_active_after_outcome", "outcome",
}
EVENT_OPTIONAL = {"agent_id", "agent_public_id", "local_address", "remote_address"}
SESSION_FIELDS = {"agent_id", "agent_public_id", "lane", "session_identity", "snapshot"}
SESSION_SNAPSHOT_FIELDS = {
    "aggregate_enabled", "local_address", "remote_address", "rtt_nanoseconds",
    "send_available", "send_limit", "send_wake_registered", "send_queue_length",
    "control_queue_length", "total_streams", "captured_streams", "truncated", "streams",
}
STREAM_FIELDS = {
    "id", "byte_send_available", "byte_receive_limit", "allocation_send_available",
    "allocation_send_limit", "allocation_receive_limit",
}


def exact_int(value: Any, maximum: int = UINT64_MAX) -> bool:
    return type(value) is int and 0 <= value <= maximum


def exact_bool(value: Any) -> bool:
    return type(value) is bool


def nonempty(value: Any) -> bool:
    return type(value) is str and bool(value)


def envelope(value: Any, sequence: int, phase: str, issues: list[str]) -> dict[str, Any]:
    path = f"server {phase} envelope"
    if type(value) is not dict:
        issues.append(f"{path} is not an object")
        return {}
    expected = {
        "schema_version", "role", "action", "sequence", "request_started_unix_ns",
        "request_finished_unix_ns", "request_elapsed_ns", "http_status", "response_bytes",
        "response", "error", "authorization",
    }
    if set(value) != expected:
        issues.append(f"{path} fields do not match the bounded response schema")
    for key in ("schema_version", "sequence", "request_started_unix_ns", "request_finished_unix_ns",
                "request_elapsed_ns", "http_status", "response_bytes"):
        if not exact_int(value.get(key)):
            issues.append(f"{path} {key} is not a nonnegative exact integer")
    started, finished = value.get("request_started_unix_ns"), value.get("request_finished_unix_ns")
    if (value.get("schema_version") != 1 or value.get("role") != "server-capacity"
            or value.get("action") != phase or value.get("sequence") != sequence
            or value.get("authorization") != "bearer-present"):
        issues.append(f"{path} identity or authorization provenance changed")
    if value.get("http_status") != 200 or value.get("error") != "":
        issues.append(f"{path} request failed")
    if not exact_int(started) or started <= 0 or not exact_int(finished) or finished < started:
        issues.append(f"{path} wall timestamps are invalid")
    if not exact_int(value.get("response_bytes")) or not (0 < value.get("response_bytes", 0) <= MAX_RESPONSE_BYTES):
        issues.append(f"{path} response size is outside the fixed bound")
    response = value.get("response")
    if type(response) is not dict:
        issues.append(f"{path} response is not an object")
        return {}
    if set(response) != CAPACITY_FIELDS:
        issues.append(f"{path} capacity fields changed")
    observed = response.get("observed_unix_ns")
    if response.get("schema_version") != 1 or response.get("sequence") != str(sequence):
        issues.append(f"{path} capacity identity changed")
    if not exact_int(observed) or observed <= 0:
        issues.append(f"{path} capacity observation timestamp is invalid")
    elif exact_int(started) and exact_int(finished) and not (started <= observed <= finished):
        issues.append(f"{path} capacity observation is outside its request interval")
    for key in (
        "generation", "adaptive_admission_limit", "adaptive_raw_admission_limit",
        "adaptive_public_admission_limit", "memory_used_bytes", "memory_limit_bytes",
        "memory_headroom_to_hard_bytes", "file_descriptors_used", "file_descriptors_limit",
        "resource_sampled_unix_ns", "resource_next_sample_unix_ns", "resource_last_good_unix_ns",
    ):
        if not exact_int(response.get(key)):
            issues.append(f"{path} capacity {key} is outside uint64")
    if exact_int(response.get("generation")) and response.get("generation") <= 0:
        issues.append(f"{path} capacity generation is not positive")
    if (response.get("adaptive") is not True or response.get("reject_new") is not False
            or response.get("adaptive_admission_role") != "server_stream"
            or response.get("memory_pressure") != "healthy"
            or response.get("memory_source") not in {"host", "go", "cgroup_v1", "cgroup_v2"}
            or response.get("resource_sample_error") != ""):
        issues.append(f"{path} capacity health or adaptive state is invalid")
    if not nonempty(response.get("resource_pressure_reason")):
        issues.append(f"{path} capacity pressure reason is missing")
    used, limit = response.get("memory_used_bytes"), response.get("memory_limit_bytes")
    if exact_int(used) and exact_int(limit) and (limit <= 0 or used > limit):
        issues.append(f"{path} capacity selected memory usage is invalid")
    fd_used, fd_limit = response.get("file_descriptors_used"), response.get("file_descriptors_limit")
    if exact_int(fd_used) and exact_int(fd_limit) and (fd_limit <= 0 or fd_used > fd_limit):
        issues.append(f"{path} capacity file-descriptor usage is invalid")
    raw_limit = response.get("adaptive_raw_admission_limit")
    admission_limit = response.get("adaptive_admission_limit")
    public_limit = response.get("adaptive_public_admission_limit")
    if (all(exact_int(item) for item in (raw_limit, admission_limit, public_limit))
            and not (public_limit <= admission_limit <= raw_limit)):
        issues.append(f"{path} capacity admission limits are inconsistent")
    sampled = response.get("resource_sampled_unix_ns")
    next_sample = response.get("resource_next_sample_unix_ns")
    last_good = response.get("resource_last_good_unix_ns")
    if (all(exact_int(item) for item in (observed, sampled, next_sample, last_good))
            and not (sampled > 0 and last_good > 0 and observed >= sampled
                     and observed >= last_good and next_sample >= sampled)):
        issues.append(f"{path} capacity sample timestamps are inconsistent")
    return response


def validate_event(event: Any, path: str, issues: list[str]) -> dict[str, Any]:
    if type(event) is not dict:
        issues.append(f"{path} is not an object")
        return {}
    if not EVENT_REQUIRED.issubset(event) or not set(event).issubset(EVENT_REQUIRED | EVENT_OPTIONAL):
        issues.append(f"{path} fields changed")
    for key in ("sample_unix_nano", "stream_id", "current", "proposed", "maximum",
                "rtt_nanoseconds", "previous", "denied_until_unix_nano"):
        maximum = UINT32_MAX if key in ("stream_id", "current", "proposed", "maximum", "previous") else UINT64_MAX
        if not exact_int(event.get(key), maximum):
            issues.append(f"{path}.{key} is outside its exact integer range")
    if type(event.get("lane")) is not int or event.get("lane") < -1 or event.get("lane") > 4:
        issues.append(f"{path}.lane is invalid")
    if not nonempty(event.get("session_identity")):
        issues.append(f"{path}.session_identity is missing")
    if not exact_bool(event.get("aggregate")) or not exact_bool(event.get("denial_active_after_outcome")):
        issues.append(f"{path} boolean fields are invalid")
    if event.get("lane") == -1:
        if "agent_id" in event or "agent_public_id" in event:
            issues.append(f"{path} unresolved event unexpectedly names an agent")
    elif type(event.get("lane")) is int:
        if not exact_int(event.get("agent_id")) or event.get("agent_id", 0) <= 0 or not nonempty(event.get("agent_public_id")):
            issues.append(f"{path} resolved agent identity is invalid")
    if not nonempty(event.get("local_address")) or not nonempty(event.get("remote_address")):
        issues.append(f"{path} physical tuple is missing")
    rate = event.get("rate_bytes_per_second")
    if type(rate) not in (int, float) or isinstance(rate, bool) or not math.isfinite(rate) or rate < 0:
        issues.append(f"{path}.rate_bytes_per_second is invalid")
    current, proposed, maximum = event.get("current"), event.get("proposed"), event.get("maximum")
    if all(exact_int(item, UINT32_MAX) for item in (current, proposed, maximum)):
        if maximum <= 0 or current > maximum or proposed > maximum or event.get("previous", 0) > maximum:
            issues.append(f"{path} current/proposed/maximum bounds are invalid")
    outcome = event.get("outcome")
    if outcome not in ("no_proposal", "grown", "reservation_denied", "publication_error"):
        issues.append(f"{path}.outcome is invalid")
    elif exact_int(current, UINT32_MAX) and exact_int(proposed, UINT32_MAX):
        if outcome == "no_proposal" and proposed > current:
            issues.append(f"{path} no_proposal increased the target")
        if outcome != "no_proposal" and proposed <= current:
            issues.append(f"{path} publication outcome did not increase the target")
    if outcome == "reservation_denied":
        if event.get("denial_active_after_outcome") is not True or event.get("denied_until_unix_nano", 0) <= event.get("sample_unix_nano", 0):
            issues.append(f"{path} reservation denial fields are inconsistent")
    sample, denied_until = event.get("sample_unix_nano"), event.get("denied_until_unix_nano")
    if exact_int(sample) and exact_int(denied_until):
        expected_active = denied_until > sample
        if event.get("denial_active_after_outcome") is not expected_active:
            issues.append(f"{path} denial activity does not match its wall deadline")
    return event


def validate_credit(value: Any, path: str, issues: list[str]) -> list[dict[str, Any]]:
    if type(value) is not dict or set(value) != {"schema_version", "captured_unix_nano", "sessions"}:
        issues.append(f"{path} fields changed")
        return []
    if value.get("schema_version") != 2 or not exact_int(value.get("captured_unix_nano")) or value.get("captured_unix_nano", 0) <= 0:
        issues.append(f"{path} schema or capture timestamp is invalid")
    sessions = value.get("sessions")
    if type(sessions) is not list or len(sessions) > 5:
        issues.append(f"{path}.sessions is missing or over the five-lane bound")
        return []
    seen: set[tuple[Any, ...]] = set()
    valid: list[dict[str, Any]] = []
    for index, session in enumerate(sessions):
        current_path = f"{path}.sessions[{index}]"
        if type(session) is not dict or set(session) != SESSION_FIELDS:
            issues.append(f"{current_path} fields changed")
            continue
        snapshot = session.get("snapshot")
        if type(snapshot) is not dict or set(snapshot) != SESSION_SNAPSHOT_FIELDS:
            issues.append(f"{current_path}.snapshot fields changed")
            continue
        identity = (session.get("agent_id"), session.get("agent_public_id"), session.get("lane"),
                    session.get("session_identity"), snapshot.get("local_address"), snapshot.get("remote_address"))
        if (not exact_int(identity[0]) or identity[0] <= 0 or not nonempty(identity[1])
                or type(identity[2]) is not int or identity[2] not in (0, 1, 2, 3, 4)
                or not all(nonempty(item) for item in identity[3:])):
            issues.append(f"{current_path} compound identity is invalid")
        if identity in seen:
            issues.append(f"{current_path} compound identity is repeated")
        seen.add(identity)
        if not exact_bool(snapshot.get("aggregate_enabled")) or not exact_bool(snapshot.get("send_wake_registered")) or not exact_bool(snapshot.get("truncated")):
            issues.append(f"{current_path}.snapshot boolean fields are invalid")
        for key in ("rtt_nanoseconds", "send_available", "send_limit", "send_queue_length",
                    "control_queue_length", "total_streams", "captured_streams"):
            if not exact_int(snapshot.get(key)):
                issues.append(f"{current_path}.snapshot.{key} is invalid")
        if (exact_int(snapshot.get("captured_streams")) and exact_int(snapshot.get("total_streams"))
                and snapshot.get("captured_streams") > snapshot.get("total_streams")):
            issues.append(f"{current_path}.snapshot captured stream count exceeds total")
        streams = snapshot.get("streams")
        if (exact_int(snapshot.get("send_available")) and exact_int(snapshot.get("send_limit"))
                and snapshot.get("send_available") > snapshot.get("send_limit")):
            issues.append(f"{current_path}.snapshot send availability exceeds its limit")
        if type(streams) is not list or len(streams) > 8:
            issues.append(f"{current_path}.snapshot.streams is missing or over its fixed bound")
            streams = []
        if snapshot.get("captured_streams") != len(streams) or snapshot.get("captured_streams", 0) > snapshot.get("total_streams", 0):
            issues.append(f"{current_path}.snapshot stream counts do not conserve")
        stream_ids: set[int] = set()
        for stream_index, stream in enumerate(streams):
            stream_path = f"{current_path}.snapshot.streams[{stream_index}]"
            if type(stream) is not dict or set(stream) != STREAM_FIELDS:
                issues.append(f"{stream_path} fields changed")
                continue
            if any(not exact_int(stream.get(key), UINT32_MAX) for key in STREAM_FIELDS):
                issues.append(f"{stream_path} values are outside uint32")
            if (exact_int(stream.get("allocation_send_available"), UINT32_MAX)
                    and exact_int(stream.get("allocation_send_limit"), UINT32_MAX)
                    and stream.get("allocation_send_available") > stream.get("allocation_send_limit")):
                issues.append(f"{stream_path} allocation send availability exceeds its limit")
            stream_id = stream.get("id")
            if not exact_int(stream_id, UINT32_MAX) or stream_id <= 0 or stream_id in stream_ids:
                issues.append(f"{stream_path}.id is invalid or repeated")
            stream_ids.add(stream_id)
        valid.append(session)
    return valid


def selected_sender_row(post_sender: Any, sender_pair: Any, issues: list[str]) -> tuple[dict[str, Any], int, int]:
    lane = sender_pair.get("selected_lane") if type(sender_pair) is dict else None
    selected = sender_pair.get("selected") if type(sender_pair) is dict else None
    stream_id = selected.get("stream_id") if type(selected) is dict else None
    rows = post_sender.get("response", {}).get("send_counters", []) if type(post_sender) is dict else []
    matches = [row for row in rows if type(row) is dict and row.get("lane") == lane]
    if type(lane) is not int or lane not in (1, 2, 3, 4) or not exact_int(stream_id, UINT32_MAX) or stream_id <= 0 or len(matches) != 1:
        issues.append("agent selected lane/stream cannot be joined to one raw sender row")
        return {}, lane, stream_id
    row = matches[0]
    for key in ("session_identity", "local_address", "remote_address"):
        if not nonempty(row.get(key)):
            issues.append(f"agent selected row {key} is missing")
    return row, lane, stream_id


def validate_pair(pre_envelope: Any, post_envelope: Any, post_sender: Any, sender_pair: Any,
                  copy_record: Any, sequence: int) -> dict[str, Any]:
    issues: list[str] = []
    pre = envelope(pre_envelope, sequence, "pre", issues)
    post = envelope(post_envelope, sequence, "post", issues)
    pre_growth = pre.get("server_receive_growth") if type(pre) is dict else None
    post_growth = post.get("server_receive_growth") if type(post) is dict else None
    for phase, growth, before, after in (
        ("pre", pre_growth, "unstarted", "measuring"),
        ("post", post_growth, "measuring", "complete"),
    ):
        path = f"server {phase} growth"
        if type(growth) is not dict or set(growth) != GROWTH_FIELDS:
            issues.append(f"{path} fields changed")
            continue
        if (growth.get("schema_version") != 1 or growth.get("sequence") != str(sequence)
                or growth.get("phase") != phase or growth.get("state_before") != before
                or growth.get("state_after") != after or growth.get("capacity") != 4096
                or growth.get("send_counters") != []
                or type(growth.get("byte_credit_traces")) is not list
                or len(growth.get("byte_credit_traces")) != 5):
            issues.append(f"{path} phase ownership or fixed schema changed")
        if not exact_int(growth.get("captured_unix_ns")) or growth.get("captured_unix_ns", 0) <= 0:
            issues.append(f"{path} capture timestamp is invalid")
        if not exact_int(growth.get("dropped_events")):
            issues.append(f"{path} dropped-event count is invalid")
        events = growth.get("events")
        if type(events) is not list or len(events) > 4096:
            issues.append(f"{path} events are missing or over capacity")
            events = []
        for index, event in enumerate(events):
            validate_event(event, f"{path}.events[{index}]", issues)
        raw = pre_envelope if phase == "pre" else post_envelope
        if type(raw) is dict and exact_int(raw.get("request_started_unix_ns")) and exact_int(raw.get("request_finished_unix_ns")):
            captured = growth.get("captured_unix_ns")
            if exact_int(captured) and not (raw["request_started_unix_ns"] <= captured <= raw["request_finished_unix_ns"]):
                issues.append(f"{path} capture timestamp is outside its request")
        if phase == "pre" and growth.get("credit_snapshot") is not None:
            issues.append("server pre growth unexpectedly contains a credit snapshot")
    if type(post_growth) is dict and post_growth.get("dropped_events") != 0:
        issues.append("selected server growth epoch dropped events")
    if type(pre_envelope) is dict and type(post_envelope) is dict:
        pre_finished = pre_envelope.get("request_finished_unix_ns")
        post_started = post_envelope.get("request_started_unix_ns")
        if exact_int(pre_finished) and exact_int(post_started) and post_started < pre_finished:
            issues.append("server post query began before the pre query finished")
        if type(copy_record) is dict:
            copy_started, copy_finished = copy_record.get("started_unix_ns"), copy_record.get("handler_return_unix_ns")
            if not (exact_int(pre_finished) and exact_int(copy_started) and exact_int(copy_finished)
                    and exact_int(post_started) and 0 < pre_finished <= copy_started <= copy_finished <= post_started):
                issues.append("selected public handler is outside the server pre/post wall envelope")
    sender_row, selected_lane, stream_id = selected_sender_row(post_sender, sender_pair, issues)
    post_sessions = validate_credit(post_growth.get("credit_snapshot") if type(post_growth) is dict else None,
                                    "server post growth.credit_snapshot", issues)
    if type(post_growth) is dict and type(post_envelope) is dict:
        credit = post_growth.get("credit_snapshot")
        if type(credit) is dict and exact_int(credit.get("captured_unix_nano")):
            if not (post_envelope.get("request_started_unix_ns", 0) <= credit["captured_unix_nano"] <= post_envelope.get("request_finished_unix_ns", -1)):
                issues.append("server credit snapshot timestamp is outside the post request")
    candidates = []
    for session in post_sessions:
        snapshot = session.get("snapshot", {})
        stream_ids = [stream.get("id") for stream in snapshot.get("streams", []) if type(stream) is dict]
        if (session.get("lane") == selected_lane and snapshot.get("local_address") == sender_row.get("remote_address")
                and snapshot.get("remote_address") == sender_row.get("local_address") and stream_id in stream_ids):
            candidates.append(session)
    if len(candidates) != 1:
        issues.append(f"server compound session/stream candidate cardinality is {len(candidates)}, want one")
        candidate: dict[str, Any] = {}
    else:
        candidate = candidates[0]
        if candidate.get("snapshot", {}).get("aggregate_enabled") is not True or candidate.get("snapshot", {}).get("truncated") is not False:
            issues.append("selected server credit session is not complete aggregate evidence")
    selected_events: list[dict[str, Any]] = []
    if candidate and type(post_growth) is dict:
        snapshot = candidate["snapshot"]
        selected_key = (candidate.get("agent_id"), candidate.get("agent_public_id"), candidate.get("lane"),
                        candidate.get("session_identity"), snapshot.get("local_address"),
                        snapshot.get("remote_address"), stream_id)
        for event in post_growth.get("events", []):
            if type(event) is not dict:
                continue
            exact_key = (event.get("agent_id"), event.get("agent_public_id"), event.get("lane"),
                         event.get("session_identity"), event.get("local_address"),
                         event.get("remote_address"), event.get("stream_id"))
            unresolved_selected = (event.get("lane") == -1 and event.get("session_identity") == selected_key[3]
                                   and event.get("local_address") == selected_key[4]
                                   and event.get("remote_address") == selected_key[5]
                                   and event.get("stream_id") == stream_id)
            if unresolved_selected:
                issues.append("selected server growth event has unresolved lane/agent identity")
            if exact_key == selected_key:
                selected_events.append(event)
                if event.get("aggregate") is not True:
                    issues.append("selected server growth event is not aggregate")
        lower = pre_envelope.get("request_finished_unix_ns") if type(pre_envelope) is dict else None
        upper = post_envelope.get("request_started_unix_ns") if type(post_envelope) is dict else None
        previous = None
        for event in selected_events:
            timestamp = event.get("sample_unix_nano")
            if not (exact_int(lower) and exact_int(timestamp) and exact_int(upper) and lower <= timestamp <= upper):
                issues.append("selected server growth sample is outside the control envelope")
            if previous is not None and exact_int(timestamp) and timestamp < previous:
                issues.append("selected server growth sample timestamps are not ordered")
            if exact_int(timestamp):
                previous = timestamp
    selected_identity = None
    if candidate:
        snapshot = candidate["snapshot"]
        selected_identity = {
            "agent_id": candidate.get("agent_id"), "agent_public_id": candidate.get("agent_public_id"),
            "lane": candidate.get("lane"), "session_identity": candidate.get("session_identity"),
            "local_address": snapshot.get("local_address"), "remote_address": snapshot.get("remote_address"),
            "stream_id": stream_id,
        }
    return {
        "schema_version": 1, "sequence": sequence, "accepted": not issues, "issues": issues,
        "selected_identity": selected_identity, "selected_event_count": len(selected_events),
        "selected_events": selected_events,
        "pre_prior_event_count": len(pre_growth.get("events", [])) if type(pre_growth) is dict and type(pre_growth.get("events")) is list else None,
        "pre_prior_dropped_events": pre_growth.get("dropped_events") if type(pre_growth) is dict else None,
        "post_dropped_events": post_growth.get("dropped_events") if type(post_growth) is dict else None,
        "timestamp_scope": "selected-compound-events-only; unrelated process-global events retained in serialized ring order",
        "sample_timestamp_semantics": "controller sample-start wall timestamp; not publication completion and never subtracted from agent monotonic durations",
    }


def main() -> None:
    import argparse
    parser = argparse.ArgumentParser()
    parser.add_argument("--pre", required=True, type=Path)
    parser.add_argument("--post", required=True, type=Path)
    parser.add_argument("--post-sender", required=True, type=Path)
    parser.add_argument("--sender-validation", required=True, type=Path)
    parser.add_argument("--copy-record", required=True, type=Path)
    parser.add_argument("--sequence", required=True, type=int)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    try:
        if args.sequence not in FIXED_SEQUENCES:
            raise ValueError("sequence is outside fixed diagnostic set")
        result = validate_pair(json.loads(args.pre.read_text()), json.loads(args.post.read_text()),
                               json.loads(args.post_sender.read_text()), json.loads(args.sender_validation.read_text()),
                               json.loads(args.copy_record.read_text()), args.sequence)
    except Exception as error:
        result = {"schema_version": 1, "sequence": args.sequence, "accepted": False,
                  "issues": [f"malformed server growth input: {error!r}"]}
    with args.output.open("x", encoding="utf-8") as stream:
        json.dump(result, stream, indent=2, sort_keys=True)
        stream.write("\n")
    raise SystemExit(0 if result["accepted"] else 1)


if __name__ == "__main__":
    main()

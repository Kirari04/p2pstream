#!/usr/bin/env python3
"""Strict validation for one completed response-copy attribution envelope."""

import json
import pathlib
import sys


DURATION_BOUNDS_NS = [1_000, 10_000, 100_000, 1_000_000, 10_000_000, 100_000_000, 1_000_000_000, -1]


def nonnegative_int(value):
    return isinstance(value, int) and not isinstance(value, bool) and value >= 0


def validate_metric(name, metric, expected_bytes, started_ns, returned_ns, issues):
    if not isinstance(metric, dict):
        issues.append(f"{name} metric missing")
        return
    calls = metric.get("calls")
    byte_count = metric.get("bytes")
    total_ns = metric.get("total_ns")
    max_ns = metric.get("max_ns")
    first_ns = metric.get("first_unix_ns", 0)
    last_ns = metric.get("last_unix_ns", 0)
    buckets = metric.get("duration_bucket_counts")
    stalls = metric.get("longest_stalls")
    if not nonnegative_int(calls):
        issues.append(f"{name} calls invalid")
        return
    if byte_count != expected_bytes:
        issues.append(f"{name} bytes {byte_count!r} != {expected_bytes}")
    durations_valid = all(nonnegative_int(value) for value in (total_ns, max_ns))
    if not durations_valid:
        issues.append(f"{name} duration totals invalid")
    elif max_ns > total_ns:
        issues.append(f"{name} max exceeds total")
    if not isinstance(buckets, list) or len(buckets) != 8 or not all(nonnegative_int(value) for value in buckets):
        issues.append(f"{name} histogram invalid")
    elif sum(buckets) != calls:
        issues.append(f"{name} histogram does not conserve calls")
    if calls:
        if not (isinstance(first_ns, int) and isinstance(last_ns, int) and started_ns <= first_ns <= last_ns <= returned_ns):
            issues.append(f"{name} timestamps outside handler interval")
        elif durations_valid and total_ns > last_ns - first_ns:
            issues.append(f"{name} total exceeds observed metric span")
    elif first_ns != 0 or last_ns != 0 or total_ns != 0 or max_ns != 0 or byte_count != 0:
        issues.append(f"{name} empty metric has observations")
    if not isinstance(stalls, list) or len(stalls) != 8:
        issues.append(f"{name} longest stalls are not fixed at eight")
        return
    prior_duration = None
    observed_calls = set()
    saw_zero = False
    for offset, stall in enumerate(stalls):
        if not isinstance(stall, dict):
            issues.append(f"{name} stall {offset} invalid")
            continue
        call = stall.get("call")
        size = stall.get("bytes")
        duration = stall.get("duration_ns")
        stall_started = stall.get("started_unix_ns")
        populated = nonnegative_int(duration) and duration > 0
        if populated:
            if saw_zero:
                issues.append(f"{name} populated stall follows zero tail")
            if not (isinstance(call, int) and not isinstance(call, bool) and 1 <= call <= calls and call not in observed_calls):
                issues.append(f"{name} stall {offset} call invalid")
            else:
                observed_calls.add(call)
            if not (nonnegative_int(size) and size <= expected_bytes):
                issues.append(f"{name} stall {offset} bytes invalid")
            if not (durations_valid and duration <= total_ns):
                issues.append(f"{name} stall {offset} duration invalid")
            if not (isinstance(stall_started, int) and started_ns <= stall_started <= returned_ns and
                    isinstance(duration, int) and stall_started + duration <= returned_ns):
                issues.append(f"{name} stall {offset} timestamp invalid")
            if prior_duration is not None and isinstance(duration, int) and duration > prior_duration:
                issues.append(f"{name} longest stalls are not descending")
            prior_duration = duration if isinstance(duration, int) else prior_duration
        else:
            saw_zero = True
            if any(value != 0 for value in (call, size, duration, stall_started)):
                issues.append(f"{name} zero-duration stall slot {offset} has retained fields")
    if stalls and isinstance(stalls[0], dict) and durations_valid and stalls[0].get("duration_ns") != max_ns:
        issues.append(f"{name} maximum does not match longest stall")


def validate_copy_envelope(envelope, sequence, direction, payload_bytes):
    issues = []
    if direction not in ("download", "upload") or not isinstance(payload_bytes, int) or isinstance(payload_bytes, bool) or payload_bytes <= 0:
        return ["copy validation inputs are invalid"]
    expected_method = "GET" if direction == "download" else "POST"
    expected_path = "/agent/bulk" if direction == "download" else "/agent/bulk-upload"
    expected_bytes = payload_bytes if direction == "download" else 32
    matches = envelope.get("matches", []) if isinstance(envelope, dict) else []
    if not (isinstance(envelope, dict) and envelope.get("overflow") is False and
            envelope.get("store_count") == sequence and envelope.get("sequence") == str(sequence) and
            isinstance(matches, list) and len(matches) == 1):
        return ["copy envelope correlation/cardinality changed"]
    record = matches[0]
    if not isinstance(record, dict):
        return ["copy record is not an object"]
    copy = record.get("copy", {})
    if not isinstance(copy, dict):
        return ["copy payload is not an object"]
    started_ns = record.get("started_unix_ns")
    returned_ns = record.get("handler_return_unix_ns")
    if not (record.get("sequence") == str(sequence) and record.get("scope") == "response-body-copy" and
            record.get("method") == expected_method and record.get("path") == expected_path):
        issues.append("copy record scope/request correlation changed")
    if not (copy.get("complete") is True and copy.get("body_wraps") == 1 and
            copy.get("downstream_read_from_calls") == 0):
        issues.append("copy record is incomplete or wrapper/ReadFrom cardinality changed")
    if copy.get("duration_bucket_upper_bounds_ns") != DURATION_BOUNDS_NS:
        issues.append("copy duration histogram bounds changed")
    if not (isinstance(started_ns, int) and not isinstance(started_ns, bool) and
            isinstance(returned_ns, int) and not isinstance(returned_ns, bool) and
            0 < started_ns <= returned_ns):
        issues.append("copy handler timestamps invalid")
        started_ns, returned_ns = 0, 0
    for name, key in (("upstream_read", "upstream_read"), ("downstream_write", "downstream_write")):
        validate_metric(name, copy.get(key), expected_bytes, started_ns, returned_ns, issues)
    read = copy.get("upstream_read", {})
    write = copy.get("downstream_write", {})
    gap = copy.get("inter_read_gap", {})
    read_calls = read.get("calls") if isinstance(read, dict) else None
    if not isinstance(read_calls, int) or isinstance(read_calls, bool) or read_calls < 1:
        issues.append("upstream read calls invalid")
        expected_gap_calls = -1
    else:
        expected_gap_calls = read_calls - 1
    validate_metric("inter_read_gap", gap, 0, started_ns, returned_ns, issues)
    if not isinstance(gap, dict) or gap.get("calls") != expected_gap_calls:
        issues.append("inter-read gap calls do not equal read calls minus one")
    write_calls = write.get("calls") if isinstance(write, dict) else None
    if not isinstance(write_calls, int) or isinstance(write_calls, bool) or write_calls < 1:
        issues.append("downstream write calls invalid")
    if copy.get("read_errors") != 0 or copy.get("write_errors") != 0 or copy.get("short_writes") != 0:
        issues.append("copy recorded a read/write error or short write")
    if copy.get("read_eof") not in (0, 1):
        issues.append("copy EOF count is outside the exact Content-Length contract")
    handler_ns = returned_ns - started_ns
    read_total = read.get("total_ns") if isinstance(read, dict) else None
    write_total = write.get("total_ns") if isinstance(write, dict) else None
    if not (isinstance(read_total, int) and isinstance(write_total, int) and
            read_total >= 0 and write_total >= 0 and read_total + write_total <= handler_ns):
        issues.append("read plus write duration exceeds handler interval")
    gap_total = gap.get("total_ns") if isinstance(gap, dict) else None
    if not (isinstance(read_total, int) and isinstance(gap_total, int) and
            read_total >= 0 and gap_total >= 0 and read_total + gap_total <= handler_ns):
        issues.append("read plus inter-read-gap duration exceeds handler interval")
    return issues


def main():
    if len(sys.argv) != 5:
        raise SystemExit("usage: validate-copy-record.py ENVELOPE_JSON SEQUENCE DIRECTION PAYLOAD_BYTES")
    source, sequence_text, direction, payload_text = sys.argv[1:]
    if direction not in ("download", "upload"):
        raise SystemExit("direction must be download or upload")
    sequence, payload_bytes = int(sequence_text), int(payload_text)
    source_path = pathlib.Path(source)
    envelope = json.loads(source_path.read_text()) if source_path.is_file() else json.loads(source)
    issues = validate_copy_envelope(envelope, sequence, direction, payload_bytes)
    if issues:
        raise SystemExit("copy attribution validation failed: " + "; ".join(issues))


if __name__ == "__main__":
    main()

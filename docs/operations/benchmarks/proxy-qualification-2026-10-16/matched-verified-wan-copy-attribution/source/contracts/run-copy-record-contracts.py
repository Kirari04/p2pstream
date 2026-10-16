#!/usr/bin/env python3
import copy
import importlib.util
import json
import pathlib
import subprocess
import sys


root = pathlib.Path(__file__).resolve().parents[1]
validator_path = root / "validate-copy-record.py"
spec = importlib.util.spec_from_file_location("copy_record_validator", validator_path)
validator = importlib.util.module_from_spec(spec)
spec.loader.exec_module(validator)


def metric(calls, byte_count, durations):
    assert calls == len(durations)
    started = 1_000_000_000_100
    stalls = []
    for call, duration in enumerate(durations, 1):
        if duration:
            stalls.append({"call": call, "bytes": min(byte_count, call), "duration_ns": duration,
                           "started_unix_ns": started + call * 1_000})
    stalls.sort(key=lambda value: value["duration_ns"], reverse=True)
    stalls = stalls[:8]
    stalls += [{"call": 0, "bytes": 0, "duration_ns": 0, "started_unix_ns": 0} for _ in range(8 - len(stalls))]
    buckets = [calls, 0, 0, 0, 0, 0, 0, 0]
    return {"calls": calls, "bytes": byte_count, "total_ns": sum(durations), "max_ns": max(durations, default=0),
            "first_unix_ns": started, "last_unix_ns": started + max(sum(durations), 1_000_000),
            "duration_bucket_counts": buckets, "longest_stalls": stalls}


def envelope(direction="download", payload=4 << 20, zero_timing=False):
    expected = payload if direction == "download" else 32
    durations = [0, 0] if zero_timing else [100, 200]
    read = metric(2, expected, durations)
    write = metric(1, expected, [0] if zero_timing else [100])
    gap = metric(1, 0, [0] if zero_timing else [50])
    record = {
        "sequence": "1", "method": "GET" if direction == "download" else "POST",
        "path": "/agent/bulk" if direction == "download" else "/agent/bulk-upload",
        "scope": "response-body-copy", "started_unix_ns": 1_000_000_000_000,
        "handler_return_unix_ns": 1_000_010_000_000,
        "copy": {"complete": True, "body_wraps": 1, "upstream_read": read, "downstream_write": write,
                 "inter_read_gap": gap, "duration_bucket_upper_bounds_ns": validator.DURATION_BOUNDS_NS,
                 "read_eof": 0, "read_errors": 0, "write_errors": 0, "short_writes": 0,
                 "downstream_read_from_calls": 0},
    }
    return {"sequence": "1", "matches": [record], "store_count": 1, "overflow": False}


def mutate(value, path, replacement):
    target = value
    for key in path[:-1]:
        target = target[key]
    target[path[-1]] = replacement


def main():
    if len(sys.argv) != 2:
        raise SystemExit("usage: run-copy-record-contracts.py NEW_OUTPUT_DIRECTORY")
    output = pathlib.Path(sys.argv[1])
    output.mkdir(parents=True, exist_ok=False)
    cases = []
    cases.append(("valid-download", envelope(), "download", None))
    cases.append(("valid-upload-response-only", envelope("upload"), "upload", None))
    cases.append(("valid-zero-duration-observations", envelope(zero_timing=True), "download", None))

    def bad(name, path, replacement, expected):
        value = envelope()
        mutate(value, path, replacement)
        cases.append((name, value, "download", expected))

    bad("overflow", ["overflow"], True, "correlation/cardinality")
    bad("wrong-store-count", ["store_count"], 2, "correlation/cardinality")
    repeated = envelope(); repeated["matches"].append(copy.deepcopy(repeated["matches"][0]))
    cases.append(("repeated-record", repeated, "download", "correlation/cardinality"))
    bad("incomplete", ["matches", 0, "copy", "complete"], False, "incomplete")
    bad("wrong-scope", ["matches", 0, "scope"], "request-body-copy", "correlation")
    bad("wrong-method", ["matches", 0, "method"], "POST", "correlation")
    bad("wrong-path", ["matches", 0, "path"], "/agent/other", "correlation")
    bad("body-wrap-count", ["matches", 0, "copy", "body_wraps"], 2, "cardinality")
    bad("read-from", ["matches", 0, "copy", "downstream_read_from_calls"], 1, "ReadFrom")
    bad("wrong-read-bytes", ["matches", 0, "copy", "upstream_read", "bytes"], 9, "bytes")
    bad("wrong-write-bytes", ["matches", 0, "copy", "downstream_write", "bytes"], 9, "bytes")
    bad("read-error", ["matches", 0, "copy", "read_errors"], 1, "error")
    bad("write-error", ["matches", 0, "copy", "write_errors"], 1, "error")
    bad("short-write", ["matches", 0, "copy", "short_writes"], 1, "short write")
    bad("histogram-conservation", ["matches", 0, "copy", "upstream_read", "duration_bucket_counts"], [1, 0, 0, 0, 0, 0, 0, 0], "conserve")
    bad("gap-call-conservation", ["matches", 0, "copy", "inter_read_gap", "calls"], 0, "read calls minus one")
    bad("stall-call-bound", ["matches", 0, "copy", "upstream_read", "longest_stalls", 0, "call"], 9, "call invalid")
    bad("stall-order", ["matches", 0, "copy", "upstream_read", "longest_stalls", 1, "duration_ns"], 300, "not descending")
    bad("handler-time-bound", ["matches", 0, "handler_return_unix_ns"], 1_000_000_000_050, "timestamps")
    bad("metric-span", ["matches", 0, "copy", "upstream_read", "total_ns"], 2_000_000, "metric span")
    bad("read-write-handler-budget", ["matches", 0, "copy", "downstream_write", "total_ns"], 10_000_000_000, "handler interval")
    malformed = envelope(); malformed["matches"][0]["copy"]["upstream_read"] = []
    cases.append(("malformed-metric", malformed, "download", "metric missing"))
    malformed_record = envelope(); malformed_record["matches"][0] = []
    cases.append(("malformed-record", malformed_record, "download", "not an object"))

    results = []
    for name, value, direction, expected_issue in cases:
        case_path = output / f"{name}.json"
        case_path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
        issues = validator.validate_copy_envelope(value, 1, direction, 4 << 20)
        if expected_issue is None:
            assert not issues, (name, issues)
            expected_status = 0
        else:
            assert any(expected_issue in issue for issue in issues), (name, issues)
            expected_status = 1
        process = subprocess.run([str(validator_path), str(case_path), "1", direction, str(4 << 20)],
                                 text=True, capture_output=True)
        assert process.returncode == expected_status, (name, process.returncode, process.stderr)
        result = {"name": name, "expected_status": expected_status, "actual_status": process.returncode,
                  "issues": issues, "stdout": process.stdout, "stderr": process.stderr}
        (output / f"{name}.result.json").write_text(json.dumps(result, indent=2, sort_keys=True) + "\n")
        results.append(result)
    (output / "summary.json").write_text(json.dumps({"accepted": True, "cases": results}, indent=2, sort_keys=True) + "\n")
    print(f"copy record contracts passed: {len(results)} cases")


if __name__ == "__main__":
    main()

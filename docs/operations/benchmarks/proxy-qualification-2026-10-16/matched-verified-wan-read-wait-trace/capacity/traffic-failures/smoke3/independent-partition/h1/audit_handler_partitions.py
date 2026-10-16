#!/usr/bin/env python3
"""Independent, streaming audit of handler goroutine state partitions.

This intentionally consumes only `go tool trace -d=parsed` text plus the frozen
index. It does not import or invoke the qualification analyzer.
"""

import argparse
import hashlib
import json
import os
import re
import shlex
import stat
import sys


MARKERS = {
    "TaskBegin": ("wan-read-wait-request", "ID"),
    "TaskEnd": ("wan-read-wait-request", "ID"),
    "RegionBegin": ("public-proxy-handler", "Task"),
    "RegionEnd": ("public-proxy-handler", "Task"),
}
EXPECTED_STATES = {"Running", "Runnable", "Syscall", "Waiting"}
MAIN_RE = re.compile(r"^M=(\S+) P=(\S+) G=(\S+) (\w+)(?: |$)")
TYPE_RE = re.compile(r'(?:^| )Type="([^"]*)"(?: |$)')
REASON_RE = re.compile(r'(?:^| )Reason="([^"]*)"(?: |$)')
FRAME_RE = re.compile(r"^\t(.*?) @ (0x[0-9a-fA-F]+)$")
FILE_RE = re.compile(r"^\t\t(.*):(\d+)$")


def sha256(path):
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def scalar(tokens, name):
    prefix = name + "="
    values = [token[len(prefix):] for token in tokens if token.startswith(prefix)]
    if len(values) != 1:
        raise ValueError(f"expected one {name}= field, got {values!r}")
    return values[0]


def parse_trace(path):
    markers = {name: [] for name in MARKERS}
    transitions = []
    zero_pc = []
    prior_time = None
    event_ordinal = 0
    current_event = None
    section = None
    pending_frame = None

    def finish_frame():
        nonlocal pending_frame
        if pending_frame is not None:
            if pending_frame["pc"] == "0x0":
                zero_pc.append(pending_frame)
            pending_frame = None

    with open(path, "r", encoding="utf-8", errors="strict", newline="") as handle:
        for line_number, raw in enumerate(handle, 1):
            if len(raw) > 1024 * 1024:
                raise ValueError(f"line {line_number} exceeds 1 MiB")
            line = raw.rstrip("\r\n")
            main = MAIN_RE.match(line)
            if main:
                finish_frame()
                section = None
                event_ordinal += 1
                tokens = shlex.split(line, posix=True)
                kind = tokens[3]
                time_ns = int(scalar(tokens, "Time"))
                if prior_time is not None and time_ns < prior_time:
                    raise ValueError(
                        f"event time regressed at line {line_number}: {time_ns} < {prior_time}"
                    )
                prior_time = time_ns
                current_event = {
                    "kind": kind,
                    "time_ns": time_ns,
                    "ordinal": event_ordinal,
                    "line": line_number,
                    "header_gid": int(main.group(3)),
                }
                if kind in MARKERS:
                    expected_type, id_field = MARKERS[kind]
                    type_match = TYPE_RE.search(line)
                    marker_type = type_match.group(1) if type_match else None
                    markers[kind].append(
                        {
                            **current_event,
                            "marker_type": marker_type,
                            "marker_id": int(scalar(tokens, id_field)),
                            "expected_type": expected_type,
                        }
                    )
                if kind == "StateTransition":
                    transition_token = next(
                        (token for token in tokens if "->" in token and "=" not in token),
                        None,
                    )
                    if transition_token is None:
                        raise ValueError(f"missing transition at line {line_number}")
                    old_state, new_state = transition_token.split("->", 1)
                    reason_match = REASON_RE.search(line)
                    resource_goids = [
                        token[len("GoID="):] for token in tokens if token.startswith("GoID=")
                    ]
                    transition = {
                        **current_event,
                        "resource_gid": int(resource_goids[0]) if len(resource_goids) == 1 else None,
                        "old_state": old_state,
                        "new_state": new_state,
                        "reason": reason_match.group(1) if reason_match else None,
                    }
                    if len(resource_goids) > 1:
                        raise ValueError(f"multiple GoID fields at line {line_number}")
                    if resource_goids:
                        transitions.append(transition)
                    current_event = transition
                continue

            if line in ("Stack=", "TransitionStack="):
                finish_frame()
                section = line[:-1]
                continue

            frame = FRAME_RE.match(line)
            if frame and section is not None and current_event is not None:
                finish_frame()
                pending_frame = {
                    "function": frame.group(1),
                    "pc": frame.group(2),
                    "file": None,
                    "source_line": None,
                    "stack_section": section,
                    "frame_line": line_number,
                    "owner_event": dict(current_event),
                }
                continue

            source = FILE_RE.match(line)
            if source and pending_frame is not None:
                pending_frame["file"] = source.group(1)
                pending_frame["source_line"] = int(source.group(2))
                finish_frame()
                continue

            if line == "":
                finish_frame()
                section = None

    finish_frame()
    return {
        "markers": markers,
        "transitions": transitions,
        "zero_pc_frames": zero_pc,
        "event_count": event_ordinal,
        "global_event_order_nondecreasing": True,
    }


def analyze_one(entry):
    parsed_path = entry["parsed_path"]
    raw_path = entry["raw_path"]
    status_path = entry["parser_status_path"]
    for path in (parsed_path, raw_path, status_path):
        mode = os.stat(path).st_mode
        if not stat.S_ISREG(mode):
            raise ValueError(f"not a regular file: {path}")

    observed_hashes = {
        "raw_sha256": sha256(raw_path),
        "parsed_sha256": sha256(parsed_path),
        "parser_status_sha256": sha256(status_path),
    }
    for key, observed in observed_hashes.items():
        if observed != entry[key]:
            raise ValueError(
                f"sequence {entry['sequence']} {key} mismatch: {observed} != {entry[key]}"
            )
    with open(status_path, "r", encoding="ascii") as handle:
        parser_status_text = handle.read()
    if parser_status_text.strip() != "0":
        raise ValueError(
            f"sequence {entry['sequence']} parser status is {parser_status_text!r}"
        )

    parsed = parse_trace(parsed_path)
    marker = {}
    for kind, rows in parsed["markers"].items():
        if len(rows) != 1:
            raise ValueError(f"sequence {entry['sequence']} has {len(rows)} {kind} markers")
        marker[kind] = rows[0]
        if rows[0]["marker_type"] != rows[0]["expected_type"]:
            raise ValueError(f"sequence {entry['sequence']} wrong {kind} type")

    task_id = marker["TaskBegin"]["marker_id"]
    handler_gid = marker["RegionBegin"]["header_gid"]
    marker_order = [
        marker[name]["ordinal"]
        for name in ("TaskBegin", "RegionBegin", "RegionEnd", "TaskEnd")
    ]
    if marker_order != sorted(marker_order) or len(set(marker_order)) != 4:
        raise ValueError(f"sequence {entry['sequence']} marker event order is invalid")
    if marker["RegionEnd"]["time_ns"] <= marker["RegionBegin"]["time_ns"]:
        raise ValueError(f"sequence {entry['sequence']} region is not positive")
    if marker["TaskBegin"]["marker_id"] != marker["TaskEnd"]["marker_id"]:
        raise ValueError(f"sequence {entry['sequence']} task IDs differ")
    if marker["RegionBegin"]["marker_id"] != task_id or marker["RegionEnd"]["marker_id"] != task_id:
        raise ValueError(f"sequence {entry['sequence']} region task IDs differ")
    marker_gids = {row["header_gid"] for row in marker.values()}
    if marker_gids != {handler_gid}:
        raise ValueError(f"sequence {entry['sequence']} marker GIDs differ: {marker_gids}")

    handler_transitions = [
        row for row in parsed["transitions"] if row["resource_gid"] == handler_gid
    ]
    if not handler_transitions:
        raise ValueError(f"sequence {entry['sequence']} has no handler transitions")
    continuity_failures = []
    for previous, current in zip(handler_transitions, handler_transitions[1:]):
        if (current["time_ns"], current["ordinal"]) < (
            previous["time_ns"], previous["ordinal"]
        ):
            raise ValueError(f"sequence {entry['sequence']} handler transition order regressed")
        if current["old_state"] != previous["new_state"]:
            continuity_failures.append(
                {
                    "previous_line": previous["line"],
                    "previous_new": previous["new_state"],
                    "current_line": current["line"],
                    "current_old": current["old_state"],
                }
            )
    if continuity_failures:
        raise ValueError(
            f"sequence {entry['sequence']} old-state continuity failed: {continuity_failures}"
        )

    region_start = marker["RegionBegin"]["time_ns"]
    region_end = marker["RegionEnd"]["time_ns"]
    intervals = []
    for current, following in zip(handler_transitions, handler_transitions[1:]):
        start = max(current["time_ns"], region_start)
        end = min(following["time_ns"], region_end)
        if end >= start and end > region_start and start < region_end:
            intervals.append(
                {
                    "state": current["new_state"],
                    "start_ns": start,
                    "end_ns": end,
                    "duration_ns": end - start,
                    "source_transition_line": current["line"],
                }
            )
    last = handler_transitions[-1]
    if last["time_ns"] < region_end:
        start = max(last["time_ns"], region_start)
        intervals.append(
            {
                "state": last["new_state"],
                "start_ns": start,
                "end_ns": region_end,
                "duration_ns": region_end - start,
                "source_transition_line": last["line"],
            }
        )
    positive_intervals = [row for row in intervals if row["duration_ns"] > 0]
    if not positive_intervals or positive_intervals[0]["start_ns"] != region_start:
        raise ValueError(f"sequence {entry['sequence']} region start is not covered")
    if positive_intervals[-1]["end_ns"] != region_end:
        raise ValueError(f"sequence {entry['sequence']} region end is not covered")
    for previous, current in zip(positive_intervals, positive_intervals[1:]):
        if previous["end_ns"] != current["start_ns"]:
            raise ValueError(f"sequence {entry['sequence']} interval coverage has a gap/overlap")

    totals = {state: 0 for state in sorted(EXPECTED_STATES)}
    unknown_states = {}
    for interval in intervals:
        state = interval["state"]
        if state in totals:
            totals[state] += interval["duration_ns"]
        else:
            unknown_states[state] = unknown_states.get(state, 0) + interval["duration_ns"]
    duration = region_end - region_start
    partition_sum = sum(totals.values()) + sum(unknown_states.values())
    if unknown_states or partition_sum != duration:
        raise ValueError(
            f"sequence {entry['sequence']} partition failed: sum={partition_sum}, "
            f"duration={duration}, unknown={unknown_states}"
        )

    zero_pc = []
    for frame in parsed["zero_pc_frames"]:
        owner = frame["owner_event"]
        resource_gid = owner.get("resource_gid")
        in_region = region_start <= owner["time_ns"] <= region_end
        relevant = owner["kind"] == "StateTransition" and resource_gid == handler_gid and in_region
        zero_pc.append(
            {
                "pc": frame["pc"],
                "function": frame["function"],
                "file": frame["file"],
                "source_line": frame["source_line"],
                "stack_section": frame["stack_section"],
                "frame_line": frame["frame_line"],
                "owner_event": owner,
                "inside_handler_region_time": in_region,
                "owner_resource_is_handler_gid": resource_gid == handler_gid,
                "relevant_handler_transition_path": relevant,
            }
        )

    counts = {state: sum(1 for row in intervals if row["state"] == state) for state in sorted(EXPECTED_STATES)}
    return {
        "sequence": entry["sequence"],
        "public_protocol": entry["public_protocol"],
        "source_binding": {
            "raw_path": raw_path,
            "raw_sha256": observed_hashes["raw_sha256"],
            "parsed_path": parsed_path,
            "parsed_sha256": observed_hashes["parsed_sha256"],
            "parser_status_path": status_path,
            "parser_status_sha256": observed_hashes["parser_status_sha256"],
            "parser_status": 0,
            "server_binary_sha256": entry["server_binary_sha256"],
            "server_source_ref": entry["server_source_ref"],
            "runner_sha256": entry["runner_sha256"],
        },
        "marker_validation": {
            "task_id": task_id,
            "handler_gid": handler_gid,
            "task_begin": marker["TaskBegin"],
            "region_begin": marker["RegionBegin"],
            "region_end": marker["RegionEnd"],
            "task_end": marker["TaskEnd"],
            "marker_order_valid": True,
            "marker_gid_consistent": True,
            "marker_task_id_consistent": True,
        },
        "transition_validation": {
            "global_event_order_nondecreasing": parsed["global_event_order_nondecreasing"],
            "handler_transition_count": len(handler_transitions),
            "handler_transition_order_valid": True,
            "handler_old_state_continuity_valid": True,
            "continuity_failures": [],
        },
        "handler_region": {
            "start_ns": region_start,
            "end_ns": region_end,
            "duration_ns": duration,
            "state_totals_ns": totals,
            "state_interval_counts_including_zero_duration": counts,
            "clipped_interval_count": len(intervals),
            "positive_interval_count": len(positive_intervals),
            "partition_sum_ns": partition_sum,
            "partition_exact": True,
        },
        "zero_pc_frames": zero_pc,
        "zero_pc_summary": {
            "count": len(zero_pc),
            "relevant_handler_transition_path_count": sum(
                bool(row["relevant_handler_transition_path"]) for row in zero_pc
            ),
        },
        "parsed_main_event_count": parsed["event_count"],
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--index", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    if os.path.exists(args.output):
        raise SystemExit(f"refusing to overwrite {args.output}")
    with open(args.index, "r", encoding="utf-8") as handle:
        index = json.load(handle)
    if [entry["sequence"] for entry in index["entries"]] != [9, 11, 13]:
        raise SystemExit("expected exactly sequences 9, 11, 13 in that order")
    report = {
        "schema_version": 1,
        "method": "independent parsed-trace state-transition partition",
        "index_path": os.path.abspath(args.index),
        "index_sha256": sha256(args.index),
        "entries": [analyze_one(entry) for entry in index["entries"]],
    }
    with open(args.output, "x", encoding="utf-8") as handle:
        json.dump(report, handle, indent=2, sort_keys=True)
        handle.write("\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())

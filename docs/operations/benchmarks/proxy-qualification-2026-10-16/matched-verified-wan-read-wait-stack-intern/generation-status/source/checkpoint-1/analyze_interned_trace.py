#!/usr/bin/env python3
"""Reduce a lossless interned pinned Go 1.26.6 parsed-trace stream.

This parser is intentionally tied to Event.String in the pinned toolchain.  It
reads the input twice, one event at a time: first to bind the unique task and
region, then to partition only that region's handler goroutine state.
"""

from __future__ import annotations

import argparse
import hashlib
import heapq
import json
import os
from pathlib import Path
import re
import stat
import sys
from typing import Iterator

from stack_intern_format import (
    CanonicalReader,
    InvalidCanonicalTrace,
    MAX_CANONICAL_BYTES,
    MAX_DEFINITION_BYTES,
    MAX_EXPANDED_BYTES,
    MAX_STACK_DEFINITIONS,
    strict_ledger_keys,
)


TASK_TYPE = "wan-read-wait-request"
REGION_TYPE = "public-proxy-handler"
MAX_INPUT_BYTES = MAX_CANONICAL_BYTES
MAX_OUTPUT_BYTES = 8 * 1024 * 1024
MAX_LINE_BYTES = 1024 * 1024
MAX_EVENTS = 5_000_000
MAX_EVENT_CONTINUATIONS = 4096
MAX_EVENT_BYTES = 4 * 1024 * 1024
MAX_WAIT_STACK_GROUPS = 4096
TOP_WAIT_COUNT = 8

HEADER_RE = re.compile(
    rb"^M=(-?\d+) P=(-?\d+) G=(-?\d+) ([A-Za-z]+) Time=(\d+)(?: (.*))?\n?$"
)
QUOTED = rb'"((?:[^"\\]|\\.)*)"'
TASK_RE = re.compile(rb"^ID=(\d+) Parent=(\d+) Type=" + QUOTED + rb"$")
REGION_RE = re.compile(rb"^Task=(\d+) Type=" + QUOTED + rb"$")
TRANSITION_RE = re.compile(
    rb"^GoID=(\d+) ([A-Za-z]+)->([A-Za-z]+) Reason=" + QUOTED + rb"$"
)
STACK_FUNCTION_RE = re.compile(r"^\t(.+) @ 0x[0-9a-f]+$")
STACK_FILE_RE = re.compile(r"^\t\t(.+):([0-9]+)$")
ZERO_PC_FUNCTION_LINE = "\t @ 0x0"
ZERO_PC_FILE_LINE = "\t\t:0"


class InvalidTrace(Exception):
    pass


def decode_quoted(raw: bytes) -> str:
    try:
        text = raw.decode("utf-8", "strict")
    except UnicodeDecodeError as error:
        raise InvalidTrace(f"invalid quoted field: {error}") from error
    output = []
    index = 0
    simple = {"a": "\a", "b": "\b", "f": "\f", "n": "\n", "r": "\r", "t": "\t", "v": "\v", "\\": "\\", '"': '"'}
    while index < len(text):
        char = text[index]
        if char != "\\":
            if ord(char) < 0x20 or ord(char) == 0x7F:
                raise InvalidTrace("unescaped control in Go quoted field")
            output.append(char)
            index += 1
            continue
        index += 1
        if index >= len(text):
            raise InvalidTrace("trailing backslash in Go quoted field")
        escape = text[index]
        index += 1
        if escape in simple:
            output.append(simple[escape])
            continue
        width = {"x": 2, "u": 4, "U": 8}.get(escape)
        if width is None or index + width > len(text):
            raise InvalidTrace(f"unsupported Go quoted escape: \\{escape}")
        digits = text[index:index + width]
        if not re.fullmatch(r"[0-9A-Fa-f]+", digits):
            raise InvalidTrace(f"invalid Go quoted escape: \\{escape}{digits}")
        value = int(digits, 16)
        if escape == "x":
            if value > 0x7F:
                raise InvalidTrace("non-ASCII byte escape in Go quoted field is unsupported")
            output.append(chr(value))
        else:
            if value > 0x10FFFF or 0xD800 <= value <= 0xDFFF:
                raise InvalidTrace("invalid Unicode scalar in Go quoted field")
            output.append(chr(value))
        index += width
    return "".join(output)


def parse_stack(lines: list[str], label: str) -> list[dict]:
    if len(lines) % 2:
        raise InvalidTrace(f"{label} has incomplete frame pair")
    frames = []
    for index in range(0, len(lines), 2):
        function_line = lines[index]
        file_line = lines[index + 1]
        if function_line == ZERO_PC_FUNCTION_LINE or file_line == ZERO_PC_FILE_LINE:
            if function_line != ZERO_PC_FUNCTION_LINE or file_line != ZERO_PC_FILE_LINE:
                raise InvalidTrace(f"{label} has malformed zero-PC sentinel")
            frames.append({
                "function": "",
                "file": "",
                "line": 0,
                "pc": 0,
                "zero_pc_sentinel": True,
            })
            continue
        function_match = STACK_FUNCTION_RE.fullmatch(function_line)
        file_match = STACK_FILE_RE.fullmatch(file_line)
        if not function_match or not file_match:
            raise InvalidTrace(f"{label} has malformed pinned stack frame")
        frames.append({
            "function": function_match.group(1),
            "file": file_match.group(1),
            "line": int(file_match.group(2)),
        })
    return frames


def finalize_event(event: dict) -> dict:
    if event["transition_stack"] and event["kind"] != "StateTransition":
        raise InvalidTrace("TransitionStack attached to non-transition event")
    event["transition_frames"] = parse_stack(event["transition_stack"], "TransitionStack")
    event["event_frames"] = parse_stack(event["event_stack"], "Stack")
    return event


def event_stream(path: Path, provenance: dict | None = None) -> Iterator[dict]:
    count = 0
    current = None
    continuation_count = 0
    event_bytes = 0
    section = None
    previous_time = None
    canonical = CanonicalReader(path)
    stream = iter(canonical.iter_lines())
    try:
        line_number = 0
        while True:
            try:
                line = next(stream)
            except StopIteration:
                break
            line_number += 1
            if len(line) > MAX_LINE_BYTES or not line.endswith(b"\n"):
                raise InvalidTrace(f"line {line_number} exceeds fixed line bound")
            match = HEADER_RE.match(line)
            if match:
                if current is not None:
                    yield finalize_event(current)
                count += 1
                if count > MAX_EVENTS:
                    raise InvalidTrace("event count exceeds fixed bound")
                current = {
                    "order": count,
                    "m": int(match.group(1)),
                    "p": int(match.group(2)),
                    "g": int(match.group(3)),
                    "kind": match.group(4).decode("ascii"),
                    "time": int(match.group(5)),
                    "fields": match.group(6) or b"",
                    "transition_stack": [],
                    "event_stack": [],
                    "seen_transition_stack": False,
                    "seen_event_stack": False,
                }
                if previous_time is not None and current["time"] < previous_time:
                    raise InvalidTrace("event timestamps decrease")
                previous_time = current["time"]
                continuation_count = 0
                event_bytes = len(line)
                section = None
                continue
            if current is None:
                raise InvalidTrace(f"continuation before first event at line {line_number}")
            continuation_count += 1
            event_bytes += len(line)
            if continuation_count > MAX_EVENT_CONTINUATIONS:
                raise InvalidTrace(f"event at line {line_number} exceeds continuation bound")
            if event_bytes > MAX_EVENT_BYTES:
                raise InvalidTrace(f"event at line {line_number} exceeds byte bound")
            stripped = line.rstrip(b"\n")
            if stripped == b"TransitionStack=":
                if current["seen_transition_stack"] or current["seen_event_stack"]:
                    raise InvalidTrace("duplicate or misordered TransitionStack marker")
                current["seen_transition_stack"] = True
                section = "transition"
            elif stripped == b"Stack=":
                if current["seen_event_stack"]:
                    raise InvalidTrace("duplicate Stack marker")
                current["seen_event_stack"] = True
                section = "event"
            elif stripped == b"":
                # Event.String's stack printer ends with a newline and
                # debugProcessedEvents adds another through fmt.Println.
                # The resulting separator is part of the pinned text format.
                continue
            elif section == "transition":
                current["transition_stack"].append(stripped.decode("utf-8", "strict"))
            elif section == "event":
                current["event_stack"].append(stripped.decode("utf-8", "strict"))
            elif stripped:
                raise InvalidTrace(f"unrecognized event continuation at line {line_number}")
    except InvalidCanonicalTrace as error:
        raise InvalidTrace(str(error)) from error
    if current is not None:
        yield finalize_event(current)
    if count == 0:
        raise InvalidTrace("parsed trace has no events")
    if canonical.summary is None:
        raise InvalidTrace("canonical footer was not validated")
    if canonical.summary["event_count"] != count:
        raise InvalidTrace("canonical and analyzer event counts disagree")
    if provenance is not None:
        if provenance:
            raise InvalidTrace("canonical provenance target was already populated")
        provenance.update(canonical.summary)


def position(event: dict) -> tuple[int, int]:
    return event["time"], event["order"]


def parse_task(event: dict) -> dict | None:
    if event["kind"] not in ("TaskBegin", "TaskEnd"):
        return None
    match = TASK_RE.match(event["fields"])
    if not match:
        raise InvalidTrace(f"malformed {event['kind']} fields")
    return {"id": int(match.group(1)), "parent": int(match.group(2)), "type": decode_quoted(match.group(3))}


def parse_region(event: dict) -> dict | None:
    if event["kind"] not in ("RegionBegin", "RegionEnd"):
        return None
    match = REGION_RE.match(event["fields"])
    if not match:
        raise InvalidTrace(f"malformed {event['kind']} fields")
    return {"task": int(match.group(1)), "type": decode_quoted(match.group(2))}


def parse_transition(event: dict) -> dict | None:
    if event["kind"] != "StateTransition":
        return None
    match = TRANSITION_RE.match(event["fields"])
    if not match:
        # Proc transitions have ProcID rather than GoID and are irrelevant.
        if event["fields"].startswith(b"ProcID="):
            return None
        raise InvalidTrace("malformed goroutine StateTransition fields")
    return {
        "goid": int(match.group(1)),
        "old": match.group(2).decode("ascii"),
        "new": match.group(3).decode("ascii"),
        "reason": decode_quoted(match.group(4)),
        "stack": event["transition_stack"],
        "frames": event["transition_frames"],
        "header_g": event["g"],
        "time": event["time"],
        "order": event["order"],
    }


def bind_markers(path: Path, provenance: dict | None = None) -> dict:
    task_begins, task_ends, region_begins, region_ends = [], [], [], []
    event_count = 0
    for event in event_stream(path, provenance):
        event_count = event["order"]
        task = parse_task(event)
        if task and task["type"] == TASK_TYPE:
            (task_begins if event["kind"] == "TaskBegin" else task_ends).append((event, task))
        region = parse_region(event)
        if region and region["type"] == REGION_TYPE:
            (region_begins if event["kind"] == "RegionBegin" else region_ends).append((event, region))
        if max(len(task_begins), len(task_ends), len(region_begins), len(region_ends)) > 1:
            raise InvalidTrace("duplicate fixed task or region marker")
    if tuple(map(len, (task_begins, task_ends, region_begins, region_ends))) != (1, 1, 1, 1):
        raise InvalidTrace("missing or incomplete fixed task/region markers")
    task_begin, begin_fields = task_begins[0]
    task_end, end_fields = task_ends[0]
    region_begin, region_begin_fields = region_begins[0]
    region_end, region_end_fields = region_ends[0]
    task_id = begin_fields["id"]
    if task_id == 0 or end_fields["id"] != task_id:
        raise InvalidTrace("task begin/end ID mismatch or background task")
    if region_begin_fields["task"] != task_id or region_end_fields["task"] != task_id:
        raise InvalidTrace("region task ID mismatch")
    if region_begin["g"] <= 0 or region_end["g"] != region_begin["g"]:
        raise InvalidTrace("region handler GID mismatch")
    if task_begin["g"] != region_begin["g"] or task_end["g"] != region_begin["g"]:
        raise InvalidTrace("task and region header GIDs do not identify one handler goroutine")
    if not (position(task_begin) < position(region_begin) < position(region_end) < position(task_end)):
        raise InvalidTrace("task/region markers are not strictly nested")
    if region_end["time"] <= region_begin["time"]:
        raise InvalidTrace("region has nonpositive wall duration")
    return {
        "task_id": task_id,
        "handler_gid": region_begin["g"],
        "task_begin": position(task_begin),
        "task_end": position(task_end),
        "region_begin": position(region_begin),
        "region_end": position(region_end),
        "event_count": event_count,
    }


def classify_wait(reason: str, frames: list[dict]) -> str:
    if not frames:
        return "waiting_missing_stack"
    if any(frame.get("zero_pc_sentinel") for frame in frames):
        return "waiting_unknown_stack"
    candidates = []
    for index, frame in enumerate(frames):
        function = frame["function"]
        if "waitForSendErr" in function:
            candidates.append((index, 0, "waiting_credit_send_result"))
        elif "sendWindowUpdate" in function or "GrowReceiveWindow" in function:
            candidates.append((index, 1, "waiting_credit_publication_path"))
        elif "tryReserveAdaptiveResource" in function:
            candidates.append((index, 2, "waiting_adaptive_owner"))
        elif "(*GrowingConn).grow" in function or "(*AggregateReceiveWindowGrowth).Grow" in function:
            candidates.append((index, 3, "waiting_growth_or_reservation"))
    if candidates:
        # Frames are leaf first. The first matching frame describes the actual
        # blocking path; the numeric rank makes equal-depth precedence stable.
        return min(candidates)[2]
    for frame in frames:
        if (reason == "select" and frame["function"].endswith("yamux.(*Stream).Read") and
                frame["file"].endswith("/third_party/yamux/stream.go") and frame["line"] == 207):
            return "waiting_yamux_empty_receive"
    return "waiting_unknown_stack"


def reduce_states(path: Path, markers: dict) -> dict:
    gid = markers["handler_gid"]
    begin = markers["region_begin"]
    end = markers["region_end"]
    current_state = None
    current_wait = None
    cursor = begin[0]
    state_totals = {name: 0 for name in ("Running", "Runnable", "Syscall", "Waiting")}
    wait_categories: dict[str, int] = {}
    wait_reasons: dict[str, int] = {}
    wait_stacks: dict[tuple[str, str], dict] = {}
    top_waits: list[tuple[int, int, dict]] = []
    transition_count = 0
    unblock_header_gid_diff = 0
    waiting_status_snapshot_count = 0
    waiting_status_snapshot_carried_ns = 0
    zero_duration_counts = {name: 0 for name in state_totals}
    initial_state_checked = False
    waiting_status_snapshot_active = False
    serial = 0

    def add_interval(state: str, start: int, finish: int, wait: dict | None,
                     carried_status_snapshot: bool = False) -> None:
        nonlocal serial, waiting_status_snapshot_carried_ns
        if finish < start:
            raise InvalidTrace("negative state interval")
        duration = finish - start
        if state not in state_totals:
            raise InvalidTrace(f"unsupported handler state inside region: {state}")
        state_totals[state] += duration
        if duration == 0:
            zero_duration_counts[state] += 1
        if state != "Waiting" or duration == 0:
            return
        if wait is None:
            raise InvalidTrace("waiting interval lacks entry metadata")
        if carried_status_snapshot:
            waiting_status_snapshot_carried_ns += duration
        reason = wait["reason"]
        category = classify_wait(reason, wait["frames"])
        wait_categories[category] = wait_categories.get(category, 0) + duration
        wait_reasons[reason] = wait_reasons.get(reason, 0) + duration
        fingerprint = hashlib.sha256("\n".join(wait["stack"]).encode()).hexdigest()
        key = (reason, fingerprint)
        if key not in wait_stacks:
            if len(wait_stacks) >= MAX_WAIT_STACK_GROUPS:
                raise InvalidTrace("wait stack group count exceeds fixed bound")
            wait_stacks[key] = {
                "reason": reason,
                "stack_sha256": fingerprint,
                "stack": wait["stack"],
                "frames": wait["frames"],
                "category": category,
                "count": 0,
                "duration_ns": 0,
            }
        group = wait_stacks[key]
        group["count"] += 1
        group["duration_ns"] += duration
        detail = {
            "start_ns": start,
            "end_ns": finish,
            "duration_ns": duration,
            "reason": reason,
            "category": category,
            "stack_sha256": fingerprint,
        }
        serial += 1
        item = (duration, serial, detail)
        if len(top_waits) < TOP_WAIT_COUNT:
            heapq.heappush(top_waits, item)
        elif item[:2] > top_waits[0][:2]:
            heapq.heapreplace(top_waits, item)

    for event in event_stream(path):
        transition = parse_transition(event)
        if transition is None or transition["goid"] != gid:
            continue
        where = transition["time"], transition["order"]
        if where <= begin:
            current_state = transition["new"]
            current_wait = transition if current_state == "Waiting" else None
            continue
        if where >= end:
            continue
        if current_state is None:
            raise InvalidTrace("handler state not established at region start")
        if not initial_state_checked:
            if current_state != "Running":
                raise InvalidTrace("handler is not Running at region begin marker")
            initial_state_checked = True
        transition_count += 1
        if transition["old"] != current_state:
            raise InvalidTrace("handler transition old state disagrees with partition state")
        add_interval(current_state, cursor, transition["time"], current_wait,
                     waiting_status_snapshot_active)
        cursor = transition["time"]
        waiting_status_snapshot = bool(
            transition["old"] == transition["new"] == "Waiting"
            and transition["reason"] == ""
            and not transition["stack"]
            and not transition["frames"]
        )
        if waiting_status_snapshot:
            if current_wait is None:
                raise InvalidTrace("waiting status snapshot has no active wait metadata")
            waiting_status_snapshot_count += 1
            waiting_status_snapshot_active = True
        else:
            current_state = transition["new"]
            current_wait = transition if current_state == "Waiting" else None
            waiting_status_snapshot_active = False
        if transition["new"] == "Runnable" and transition["header_g"] != gid:
            unblock_header_gid_diff += 1
    if current_state is None:
        raise InvalidTrace("handler state not established at region start")
    if not initial_state_checked and current_state != "Running":
        raise InvalidTrace("handler is not Running at region begin marker")
    add_interval(current_state, cursor, end[0], current_wait, waiting_status_snapshot_active)

    region_duration = end[0] - begin[0]
    partition_sum = sum(state_totals.values())
    waiting_sum = sum(wait_categories.values())
    if partition_sum != region_duration:
        raise InvalidTrace("state partition does not exactly conserve region duration")
    if waiting_sum != state_totals["Waiting"]:
        raise InvalidTrace("waiting classification does not exactly conserve waiting duration")
    missing_stack_ns = wait_categories.get("waiting_missing_stack", 0)
    unknown_stack_ns = wait_categories.get("waiting_unknown_stack", 0)
    groups = sorted(wait_stacks.values(), key=lambda value: (-value["duration_ns"], value["stack_sha256"]))
    return {
        "region_duration_ns": region_duration,
        "partition_sum_ns": partition_sum,
        "partition_delta_ns": partition_sum - region_duration,
        "state_ns": state_totals,
        "waiting_category_ns": dict(sorted(wait_categories.items())),
        "waiting_reason_ns": dict(sorted(wait_reasons.items())),
        "waiting_stack_groups": groups,
        "top_wait_intervals": [item[2] for item in sorted(top_waits, reverse=True)],
        "handler_transition_count": transition_count,
        "waiting_status_snapshot_count": waiting_status_snapshot_count,
        "waiting_status_snapshot_carried_ns": waiting_status_snapshot_carried_ns,
        "zero_duration_state_interval_counts": zero_duration_counts,
        "unblock_header_gid_diff_count": unblock_header_gid_diff,
        "missing_stack_ns": missing_stack_ns,
        "unknown_stack_ns": unknown_stack_ns,
        "attribution_complete": missing_stack_ns == 0 and unknown_stack_ns == 0,
    }


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb", buffering=1024 * 1024) as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def file_identity(path: Path) -> dict:
    metadata = path.stat()
    if not stat.S_ISREG(metadata.st_mode):
        raise InvalidTrace("parsed trace input is not a regular file")
    return {
        "device": metadata.st_dev,
        "inode": metadata.st_ino,
        "size": metadata.st_size,
        "mtime_ns": metadata.st_mtime_ns,
        "ctime_ns": metadata.st_ctime_ns,
    }


def exact_zero_status(path: Path, label: str) -> str:
    metadata = path.stat()
    if not stat.S_ISREG(metadata.st_mode) or metadata.st_size != 2:
        raise InvalidTrace(f"{label} status has invalid size")
    value = path.read_bytes()
    if value != b"0\n":
        raise InvalidTrace(f"{label} status is not exact zero")
    return hashlib.sha256(value).hexdigest()


def load_ledger(path: Path) -> dict:
    metadata = path.stat()
    if not stat.S_ISREG(metadata.st_mode) or metadata.st_size <= 0 or metadata.st_size > 64 * 1024:
        raise InvalidTrace("canonical ledger is not a bounded regular file")
    try:
        value = json.loads(path.read_bytes())
    except (json.JSONDecodeError, UnicodeDecodeError) as error:
        raise InvalidTrace(f"canonical ledger is invalid JSON: {error}") from error
    if not isinstance(value, dict) or set(value) != strict_ledger_keys():
        raise InvalidTrace("canonical ledger fields are not exact")
    expected_bounds = {
        "max_canonical_input_bytes": MAX_CANONICAL_BYTES,
        "max_expanded_input_bytes": MAX_EXPANDED_BYTES,
        "max_line_bytes": MAX_LINE_BYTES,
        "max_events": MAX_EVENTS,
        "max_event_continuations": MAX_EVENT_CONTINUATIONS,
        "max_event_bytes": MAX_EVENT_BYTES,
        "max_stack_definitions": MAX_STACK_DEFINITIONS,
        "max_definition_bytes": MAX_DEFINITION_BYTES,
    }
    if value.get("schema_version") != 1 or value.get("format") != "p2pstream-parsed-stack-intern-v1":
        raise InvalidTrace("canonical ledger schema/version mismatch")
    if value.get("fixed_bounds") != expected_bounds:
        raise InvalidTrace("canonical ledger fixed bounds mismatch")
    integer_fields = (
        "canonical_bytes", "expanded_input_bytes", "event_count", "definition_count",
        "definition_bytes", "record_count",
    )
    if any(type(value.get(field)) is not int or value[field] < 0 for field in integer_fields):
        raise InvalidTrace("canonical ledger has invalid integer field")
    digest_fields = ("canonical_sha256", "expanded_input_sha256", "canonical_prefooter_sha256")
    if any(not isinstance(value.get(field), str) or not re.fullmatch(r"[0-9a-f]{64}", value[field])
           for field in digest_fields):
        raise InvalidTrace("canonical ledger has invalid digest field")
    return value


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", required=True, type=Path)
    parser.add_argument("--tool-status", required=True, type=Path)
    parser.add_argument("--canonicalizer-status", required=True, type=Path)
    parser.add_argument("--ledger", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    arguments = parser.parse_args()
    source = arguments.input.resolve(strict=False)
    tool_status_path = arguments.tool_status.resolve(strict=False)
    canonicalizer_status_path = arguments.canonicalizer_status.resolve(strict=False)
    ledger_path = arguments.ledger.resolve(strict=False)
    output = arguments.output.resolve(strict=False)
    if output.exists():
        raise SystemExit("output already exists")
    result = {
        "schema_version": 2,
        "accepted": False,
        "task_type": TASK_TYPE,
        "region_type": REGION_TYPE,
        "input": str(source),
        "input_representation": "p2pstream-parsed-stack-intern-v1",
        "input_bytes": None,
        "parsed_tool_status_file": str(tool_status_path),
        "canonicalizer_status_file": str(canonicalizer_status_path),
        "canonical_ledger_file": str(ledger_path),
        "fixed_bounds": {
            "max_canonical_input_bytes": MAX_INPUT_BYTES,
            "max_expanded_input_bytes": MAX_EXPANDED_BYTES,
            "max_line_bytes": MAX_LINE_BYTES,
            "max_events": MAX_EVENTS,
            "max_event_continuations": MAX_EVENT_CONTINUATIONS,
            "max_event_bytes": MAX_EVENT_BYTES,
            "max_wait_stack_groups": MAX_WAIT_STACK_GROUPS,
            "max_stack_definitions": MAX_STACK_DEFINITIONS,
            "max_definition_bytes": MAX_DEFINITION_BYTES,
            "max_output_bytes": MAX_OUTPUT_BYTES,
        },
        "errors": [],
    }
    try:
        initial_identity = file_identity(source)
        size = initial_identity["size"]
        result["input_bytes"] = size
        parser_status_identity = file_identity(tool_status_path)
        canonicalizer_status_identity = file_identity(canonicalizer_status_path)
        result["parsed_tool_status_sha256"] = exact_zero_status(
            tool_status_path, "pinned go tool trace parsed"
        )
        result["canonicalizer_status_sha256"] = exact_zero_status(
            canonicalizer_status_path, "stack canonicalizer"
        )
        if size <= 0 or size > MAX_INPUT_BYTES:
            raise InvalidTrace("physical canonical input size violates fixed 512 MiB bound")
        result["input_sha256"] = sha256_file(source)
        ledger_identity = file_identity(ledger_path)
        ledger_sha256 = sha256_file(ledger_path)
        ledger = load_ledger(ledger_path)
        provenance: dict = {}
        markers = bind_markers(source, provenance)
        if ledger["canonical_bytes"] != size or ledger["canonical_sha256"] != result["input_sha256"]:
            raise InvalidTrace("canonical ledger file identity mismatch")
        for field in (
            "expanded_input_bytes", "expanded_input_sha256", "event_count",
            "definition_count", "definition_bytes", "record_count",
            "canonical_prefooter_sha256",
        ):
            if ledger[field] != provenance[field]:
                raise InvalidTrace(f"canonical ledger {field} mismatch")
        result["canonical_ledger_sha256"] = ledger_sha256
        result["expanded_input_bytes"] = provenance["expanded_input_bytes"]
        result["expanded_input_sha256"] = provenance["expanded_input_sha256"]
        result["canonical_dictionary"] = {
            "definition_count": provenance["definition_count"],
            "definition_bytes": provenance["definition_bytes"],
            "record_count": provenance["record_count"],
            "prefooter_sha256": provenance["canonical_prefooter_sha256"],
        }
        if file_identity(source) != initial_identity:
            raise InvalidTrace("canonical trace input changed during marker pass")
        reduction = reduce_states(source, markers)
        if file_identity(source) != initial_identity or sha256_file(source) != result["input_sha256"]:
            raise InvalidTrace("canonical trace input changed across streaming passes")
        if file_identity(ledger_path) != ledger_identity or sha256_file(ledger_path) != ledger_sha256:
            raise InvalidTrace("canonical ledger changed during streaming passes")
        if (file_identity(tool_status_path) != parser_status_identity or
                exact_zero_status(tool_status_path, "pinned go tool trace parsed") !=
                result["parsed_tool_status_sha256"]):
            raise InvalidTrace("pinned go tool trace parsed status changed during streaming passes")
        if (file_identity(canonicalizer_status_path) != canonicalizer_status_identity or
                exact_zero_status(canonicalizer_status_path, "stack canonicalizer") !=
                result["canonicalizer_status_sha256"]):
            raise InvalidTrace("stack canonicalizer status changed during streaming passes")
        result["markers"] = markers
        result["reduction"] = reduction
        if reduction["missing_stack_ns"]:
            raise InvalidTrace("positive waiting interval has no TransitionStack")
        result["accepted"] = True
    except (InvalidTrace, OSError, UnicodeError) as error:
        result["errors"].append(str(error))
    encoded = (json.dumps(result, indent=2, sort_keys=True) + "\n").encode()
    if len(encoded) > MAX_OUTPUT_BYTES:
        raise SystemExit("result exceeds fixed output bound")
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
    descriptor = os.open(output, flags, 0o600)
    with os.fdopen(descriptor, "wb") as stream:
        stream.write(encoded)
        stream.flush()
        os.fsync(stream.fileno())
    return 0 if result["accepted"] else 1


if __name__ == "__main__":
    raise SystemExit(main())

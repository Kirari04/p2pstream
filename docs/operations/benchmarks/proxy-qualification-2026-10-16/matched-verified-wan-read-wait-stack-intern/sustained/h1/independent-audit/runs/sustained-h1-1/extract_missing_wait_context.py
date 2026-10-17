#!/usr/bin/env python3
import hashlib
import importlib.util
import json
from pathlib import Path
import re
import shlex
import sys


FORMAT_SHA256 = "f370243b753a94a122157e9b0b13eb535065fe88746269bd70de5475d82e4dbf"
HEADER_RE = re.compile(r"^M=(\S+) P=(\S+) G=(\S+) (\w+)(?: |$)")
TYPE_RE = re.compile(r'(?:^| )Type="([^"]*)"(?: |$)')
REASON_RE = re.compile(r'(?:^| )Reason="([^"]*)"(?: |$)')


def sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def scalar(tokens, name):
    prefix = name + "="
    values = [token[len(prefix):] for token in tokens if token.startswith(prefix)]
    if len(values) != 1:
        raise ValueError(f"expected one {name}= field, got {values!r}")
    return values[0]


def load_reader(path):
    path = path.resolve(strict=True)
    observed = sha256(path)
    if observed != FORMAT_SHA256:
        raise ValueError(f"format module hash mismatch: {observed}")
    spec = importlib.util.spec_from_file_location("missing_wait_format", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module.CanonicalReader, observed


def event_stream(path, reader_type):
    reader = reader_type(path)
    current = None
    ordinal = 0
    for raw in reader.iter_lines():
        line = raw.decode("utf-8", errors="strict").rstrip("\r\n")
        match = HEADER_RE.match(line)
        if match:
            if current is not None:
                yield current
            ordinal += 1
            tokens = shlex.split(line, posix=True)
            kind = tokens[3]
            event = {
                "header": line,
                "kind": kind,
                "time_ns": int(scalar(tokens, "Time")),
                "ordinal": ordinal,
                "header_gid": int(match.group(3)),
                "resource_gid": None,
                "old_state": None,
                "new_state": None,
                "reason": None,
                "has_transition_stack": False,
            }
            type_match = TYPE_RE.search(line)
            event["marker_type"] = type_match.group(1) if type_match else None
            if kind == "StateTransition":
                transition = next((token for token in tokens if "->" in token and "=" not in token), None)
                if transition is None:
                    raise ValueError(f"transition missing at event {ordinal}")
                event["old_state"], event["new_state"] = transition.split("->", 1)
                resource = [token[5:] for token in tokens if token.startswith("GoID=")]
                if len(resource) > 1:
                    raise ValueError(f"multiple resource GoIDs at event {ordinal}")
                event["resource_gid"] = int(resource[0]) if resource else None
                reason_match = REASON_RE.search(line)
                event["reason"] = reason_match.group(1) if reason_match else None
            current = event
        elif current is not None and line == "TransitionStack=":
            current["has_transition_stack"] = True
    if current is not None:
        yield current
    if reader.summary is None:
        raise ValueError("canonical footer was not validated")


def compact_event(event):
    if event is None:
        return None
    return {key: event[key] for key in (
        "header", "kind", "time_ns", "ordinal", "header_gid", "resource_gid",
        "old_state", "new_state", "reason", "has_transition_stack",
    )}


def inspect(path, reader_type, sequence):
    events = event_stream(path, reader_type)
    previous = None
    current = next(events, None)
    handler_gid = None
    region_start = None
    region_end = None
    candidates = []
    pending = None
    while current is not None:
        following = next(events, None)
        if current["kind"] == "RegionBegin" and current["marker_type"] == "public-proxy-handler":
            handler_gid = current["header_gid"]
            region_start = current["time_ns"]
        if current["kind"] == "RegionEnd" and current["marker_type"] == "public-proxy-handler":
            region_end = current["time_ns"]
        is_handler_transition = (
            current["kind"] == "StateTransition"
            and handler_gid is not None
            and current["resource_gid"] == handler_gid
        )
        if is_handler_transition:
            if pending is not None:
                pending["next_handler_transition"] = compact_event(current)
                pending["next_handler_time_ns"] = current["time_ns"]
                candidates.append(pending)
                pending = None
            if (current["new_state"] == "Waiting" and current["reason"] == ""
                    and not current["has_transition_stack"]):
                pending = {
                    "transition": compact_event(current),
                    "previous_global_event": compact_event(previous),
                    "next_global_event": compact_event(following),
                }
        previous, current = current, following
    if pending is not None:
        pending["next_handler_transition"] = None
        pending["next_handler_time_ns"] = None
        candidates.append(pending)
    if handler_gid is None or region_start is None or region_end is None or region_end <= region_start:
        raise ValueError(f"sequence {sequence} has invalid handler markers")
    positive = []
    for candidate in candidates:
        start = max(candidate["transition"]["time_ns"], region_start)
        next_time = candidate["next_handler_time_ns"]
        end = region_end if next_time is None else min(next_time, region_end)
        duration = end - start
        if duration > 0 and start < region_end:
            candidate["clipped_start_ns"] = start
            candidate["clipped_end_ns"] = end
            candidate["duration_ns"] = duration
            positive.append(candidate)
    return {
        "sequence": sequence,
        "canonical_path": str(path),
        "canonical_sha256": sha256(path),
        "handler_gid": handler_gid,
        "region_start_ns": region_start,
        "region_end_ns": region_end,
        "positive_empty_reason_missing_transition_stack_count": len(positive),
        "positive_empty_reason_missing_transition_stack_ns": sum(row["duration_ns"] for row in positive),
        "entries": positive,
    }


if len(sys.argv) != 4:
    raise SystemExit("usage: extract_missing_wait_context.py INDEX FORMAT_MODULE OUTPUT")
index_path, format_path, output_path = map(Path, sys.argv[1:])
if output_path.exists():
    raise SystemExit("output must be fresh")
index = json.loads(index_path.read_text())
if [entry.get("sequence") for entry in index.get("entries", [])] != [9, 11, 14, 15]:
    raise SystemExit("unexpected sustained sequence set")
reader_type, format_hash = load_reader(format_path)
results = [inspect(Path(entry["canonical_path"]), reader_type, entry["sequence"]) for entry in index["entries"]]
report = {
    "schema_version": 1,
    "scope": "positive handler Waiting intervals with empty reason and no TransitionStack",
    "index_path": str(index_path),
    "index_sha256": sha256(index_path),
    "format_module_path": str(format_path.resolve(strict=True)),
    "format_module_sha256": format_hash,
    "results": results,
}
output_path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")

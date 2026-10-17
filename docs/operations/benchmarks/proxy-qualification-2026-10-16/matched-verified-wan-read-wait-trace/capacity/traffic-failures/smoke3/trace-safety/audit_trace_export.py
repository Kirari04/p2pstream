#!/usr/bin/env python3
import base64
import hashlib
import json
import re
from collections import Counter
from pathlib import Path


ROOT = Path("/home/user/.codex/worktrees/astra-proxy-performance/p2pstream")
RUN = ROOT / "tmp/continuation-20261016/wan-read-wait-capacity-source/runs/tiny-read-wait-capacity-smoke-3/h1"
TLS = ROOT / "tmp/continuation-20261016/wan-read-wait-capacity-source/runs/tiny-read-wait-capacity-smoke-3/tls"
REVIEW = ROOT / "tmp/continuation-20261016/wan-read-wait-capacity-source/review/h1-smoke3-trace-export-safety-1"
INDEX = RUN / "read-wait-reductions/index.json"
SEQUENCES = (9, 11, 13)


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def pem_der(path: Path) -> bytes:
    lines = [line.strip() for line in path.read_bytes().splitlines()]
    payload = b"".join(line for line in lines if line and not line.startswith(b"-----"))
    return base64.b64decode(payload, validate=True)


def quoted_field(line: str, name: str):
    match = re.search(rf"(?:^| ){re.escape(name)}=(\"(?:[^\"\\]|\\.)*\")", line)
    if not match:
        return None
    return json.loads(match.group(1))


def parsed_inventory(path: Path):
    kinds = Counter()
    inventory = {
        "task_types": set(),
        "region_types": set(),
        "log_categories": set(),
        "log_messages": set(),
        "labels": set(),
        "state_reasons": set(),
        "metric_names": set(),
        "range_names": set(),
        "experimental_names": set(),
    }
    main_event = re.compile(r"^M=-?\d+ P=-?\d+ G=-?\d+ ([A-Za-z]+) Time=")
    with path.open("r", encoding="utf-8", errors="strict") as source:
        for line in source:
            match = main_event.match(line)
            if not match:
                continue
            kind = match.group(1)
            kinds[kind] += 1
            if kind in {"TaskBegin", "TaskEnd"}:
                value = quoted_field(line, "Type")
                if value is not None:
                    inventory["task_types"].add(value)
            elif kind in {"RegionBegin", "RegionEnd"}:
                value = quoted_field(line, "Type")
                if value is not None:
                    inventory["region_types"].add(value)
            elif kind == "Log":
                category = quoted_field(line, "Category")
                message = quoted_field(line, "Message")
                if category is not None:
                    inventory["log_categories"].add(category)
                if message is not None:
                    inventory["log_messages"].add(message)
            elif kind == "Label":
                value = quoted_field(line, "Label")
                if value is not None:
                    inventory["labels"].add(value)
            elif kind == "StateTransition":
                value = quoted_field(line, "Reason")
                if value is not None:
                    inventory["state_reasons"].add(value)
            elif kind == "Metric":
                value = quoted_field(line, "Name")
                if value is not None:
                    inventory["metric_names"].add(value)
            elif kind in {"RangeBegin", "RangeActive", "RangeEnd"}:
                value = quoted_field(line, "Name")
                if value is not None:
                    inventory["range_names"].add(value)
            elif kind == "Experimental":
                match_name = re.search(r"(?:^| )Name=([^ ]+)", line)
                if match_name:
                    inventory["experimental_names"].add(match_name.group(1))
    return dict(sorted(kinds.items())), {key: sorted(values) for key, values in inventory.items()}


def wire_strings(path: Path):
    strings = []
    event_counts = Counter()
    pending_string = False
    with path.open("r", encoding="utf-8", errors="strict") as source:
        for line in source:
            if line and not line[0].isspace():
                event_counts[line.split(" ", 1)[0]] += 1
            if line.startswith("String id="):
                pending_string = True
                continue
            if pending_string:
                if not line.startswith("\tdata="):
                    raise ValueError(f"String record in {path} lacks data line")
                strings.append(json.loads(line[len("\tdata="):]))
                pending_string = False
    if pending_string:
        raise ValueError(f"unterminated String record in {path}")
    return strings, dict(sorted(event_counts.items()))


def scan_bytes(label: str, data: bytes, needles):
    hits = []
    for needle_label, needle in needles:
        if needle and needle in data:
            hits.append(needle_label)
    return {"artifact": label, "unsafe_labels": sorted(set(hits)), "accepted": not hits}


index = json.loads(INDEX.read_text(encoding="utf-8"))
entries = {int(entry["sequence"]): entry for entry in index["entries"]}
if set(entries) != set(SEQUENCES):
    raise SystemExit("unexpected reduction index sequence set")

# Load sensitive values only into memory. The report records availability and
# match labels, never the values, fragments, lengths, or secret-derived hashes.
ready_path = next((RUN / "proxy-agent-growth.uOesQn").glob("*-server.ready.json"))
agent_token = json.loads(ready_path.read_text(encoding="utf-8"))["agents"][0]["token"].encode()
private_key_path = TLS / "public.test.key.pem"
certificate_path = TLS / "public.test.crt.pem"
private_key_pem = private_key_path.read_bytes()
private_key_der = pem_der(private_key_path)
certificate_pem = certificate_path.read_bytes()
certificate_der = pem_der(certificate_path)
private_key_body = b"".join(line.strip() for line in private_key_pem.splitlines() if line and not line.startswith(b"-----"))
certificate_body = b"".join(line.strip() for line in certificate_pem.splitlines() if line and not line.startswith(b"-----"))

needles = [
    ("agent_auth_token", agent_token),
    ("agent_authorization_value", b"Bearer " + agent_token),
    ("private_key_pem", private_key_pem),
    ("private_key_der", private_key_der),
    ("private_key_base64_body", private_key_body),
    ("certificate_pem", certificate_pem),
    ("certificate_der", certificate_der),
    ("certificate_base64_body", certificate_body),
    ("authorization_header_name", b"Authorization"),
    ("diagnostic_sequence_header_name", b"X-Qualification-Diagnostic-Sequence"),
    ("public_host_header_value", b"public.test"),
    ("public_request_path", b"/agent/bulk"),
    ("payload_marker", b"p2pstream-qualification-payload-"),
    ("payload_marker_repeated", b"p2pstream-qualification-payload-" * 8),
]

# A generated trace bearer is intentionally process-local and was not persisted
# by this runner. Cover accidental ASCII serialization independently by rejecting
# any 64-character lowercase hexadecimal run in raw, wire, or parsed trace data.
hex64 = re.compile(rb"(?<![0-9a-f])[0-9a-f]{64}(?![0-9a-f])")

report = {
    "schema_version": 1,
    "review_kind": "offline-go-execution-trace-export-safety",
    "source_run": str(RUN),
    "index": {"path": str(INDEX), "sha256": sha256(INDEX)},
    "secret_handling": {
        "values_emitted": False,
        "secret_derived_hashes_emitted": False,
        "agent_auth_token_loaded": True,
        "private_key_loaded": True,
        "certificate_loaded": True,
        "trace_bearer_recovered": False,
        "trace_bearer_limitation": "generated process-local bearer was not persisted in the closed H1 run; generic ASCII hex64 rejection and annotation inventory cover accidental text serialization, but exact decoded-byte comparison is unavailable",
    },
    "expected_annotations": {
        "task_types": ["wan-read-wait-request"],
        "region_types": ["public-proxy-handler"],
        "event_logs": 0,
    },
    "entries": [],
}

all_accepted = True
for sequence in SEQUENCES:
    entry = entries[sequence]
    raw = Path(entry["raw_path"])
    parsed = Path(entry["parsed_path"])
    reduction = Path(entry["reduction_path"])
    status_path = Path(entry["parser_status_path"])
    wire = REVIEW / f"sequence-{sequence}.wire.txt"
    wire_status_path = REVIEW / f"sequence-{sequence}.wire.status"
    wire_stderr = REVIEW / f"sequence-{sequence}.wire.stderr.log"

    raw_hash = sha256(raw)
    parsed_hash = sha256(parsed)
    reduction_hash = sha256(reduction)
    parser_status = status_path.read_text(encoding="ascii").strip()
    wire_status = wire_status_path.read_text(encoding="ascii").strip()
    strings, wire_event_counts = wire_strings(wire)
    parsed_event_counts, annotations = parsed_inventory(parsed)

    scans = []
    for label, path in (("raw", raw), ("wire", wire), ("parsed", parsed), ("reduction", reduction)):
        data = path.read_bytes()
        scan = scan_bytes(label, data, needles)
        if label in {"raw", "wire", "parsed"}:
            scan["ascii_hex64_runs"] = len(hex64.findall(data))
            scan["accepted"] = scan["accepted"] and scan["ascii_hex64_runs"] == 0
        scans.append(scan)

    exact_annotations = (
        annotations["task_types"] == ["wan-read-wait-request"]
        and annotations["region_types"] == ["public-proxy-handler"]
        and parsed_event_counts.get("TaskBegin") == 1
        and parsed_event_counts.get("TaskEnd") == 1
        and parsed_event_counts.get("RegionBegin") == 1
        and parsed_event_counts.get("RegionEnd") == 1
        and parsed_event_counts.get("Log", 0) == 0
        and not annotations["log_categories"]
        and not annotations["log_messages"]
    )
    labels_allowed = set(annotations["labels"]).issubset({"Not worker", "GC (dedicated)", "GC (fractional)", "GC (idle)"})
    string_controls = {
        "count": len(strings),
        "unique_count": len(set(strings)),
        "all_utf8": True,
        "contains_nul": any("\x00" in value for value in strings),
        "max_utf8_bytes": max((len(value.encode("utf-8")) for value in strings), default=0),
        "exact_task_name_present": "wan-read-wait-request" in strings,
        "exact_region_name_present": "public-proxy-handler" in strings,
        "ascii_hex64_values": sum(1 for value in strings if re.fullmatch(r"[0-9a-f]{64}", value)),
    }
    string_controls["accepted"] = (
        not string_controls["contains_nul"]
        and string_controls["exact_task_name_present"]
        and string_controls["exact_region_name_present"]
        and string_controls["ascii_hex64_values"] == 0
    )

    accepted = (
        raw_hash == entry["raw_sha256"]
        and parsed_hash == entry["parsed_sha256"]
        and reduction_hash == entry["reduction_sha256"]
        and parser_status == "0"
        and wire_status == "0"
        and wire_stderr.stat().st_size == 0
        and exact_annotations
        and labels_allowed
        and string_controls["accepted"]
        and all(scan["accepted"] for scan in scans)
    )
    all_accepted = all_accepted and accepted
    report["entries"].append({
        "sequence": sequence,
        "protocol": entry["public_protocol"],
        "raw": {"path": str(raw), "sha256": raw_hash, "bytes": raw.stat().st_size, "index_hash_match": raw_hash == entry["raw_sha256"]},
        "parsed": {"path": str(parsed), "sha256": parsed_hash, "bytes": parsed.stat().st_size, "index_hash_match": parsed_hash == entry["parsed_sha256"], "parser_status": int(parser_status)},
        "reduction": {"path": str(reduction), "sha256": reduction_hash, "bytes": reduction.stat().st_size, "index_hash_match": reduction_hash == entry["reduction_sha256"]},
        "wire": {"path": str(wire), "sha256": sha256(wire), "bytes": wire.stat().st_size, "status": int(wire_status), "stderr_bytes": wire_stderr.stat().st_size},
        "wire_event_counts": wire_event_counts,
        "parsed_event_counts": parsed_event_counts,
        "annotation_and_runtime_strings": annotations,
        "wire_string_controls": string_controls,
        "sensitive_content_scans": scans,
        "exact_annotations_accepted": exact_annotations,
        "runtime_labels_accepted": labels_allowed,
        "accepted_for_export_safety": accepted,
    })

report["accepted"] = all_accepted
report["export_scope"] = {
    "safe_if_packaged_with_hashes": [
        "three raw runtime trace files",
        "three reduction JSON files as rejected/analyzer-limited diagnostic records",
        "parser status sidecars",
        "this export-safety report and its source/provenance manifest",
    ],
    "omit_by_default": [
        "three full parsed traces (expanded to approximately 73 MiB total)",
        "three wire dumps (review intermediates)",
    ],
    "disclosed_metadata": [
        "whole-server scheduler and timing events",
        "goroutine/thread/proc identifiers",
        "GC/heap metrics and concurrency",
        "function names and absolute source, build, and module-cache paths",
        "Go trace wall-clock synchronization",
    ],
    "causal_limit": "analyzer rejection caused by the known zero-PC frame issue remains authoritative; export safety does not validate the reduction or establish a cause",
}

destination = REVIEW / "export-safety-report.json"
with destination.open("x", encoding="utf-8") as output:
    json.dump(report, output, indent=2, sort_keys=True)
    output.write("\n")
raise SystemExit(0 if all_accepted else 1)

#!/usr/bin/env python3
import hashlib
import json
from pathlib import Path
import sys


def sha256(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


if len(sys.argv) != 4:
    raise SystemExit("usage: compare_primary_v2.py INDEPENDENT INDEX OUTPUT")
independent_path, index_path, output_path = map(Path, sys.argv[1:])
if output_path.exists():
    raise SystemExit("comparison output must be fresh")
independent = json.loads(independent_path.read_text())
index = json.loads(index_path.read_text())
indexed = {entry["sequence"]: entry for entry in index["entries"]}
comparisons = []
for entry in independent["entries"]:
    sequence = entry["sequence"]
    primary_path = Path(indexed[sequence]["reduction_path"])
    primary = json.loads(primary_path.read_text())
    markers = primary["markers"]
    independent_markers = entry["marker_validation"]
    marker_equal = {
        "task_id": independent_markers["task_id"] == markers["task_id"],
        "handler_gid": independent_markers["handler_gid"] == markers["handler_gid"],
    }
    for name in ("task_begin", "region_begin", "region_end", "task_end"):
        observed = independent_markers[name]
        marker_equal[name] = [observed["time_ns"], observed["ordinal"]] == markers[name]
    primary_reduction = primary["reduction"]
    independent_region = entry["handler_region"]
    comparison = {
        "sequence": sequence,
        "primary_path": str(primary_path),
        "primary_sha256": sha256(primary_path),
        "marker_equal": marker_equal,
        "region_duration_equal": independent_region["duration_ns"] == primary_reduction["region_duration_ns"],
        "partition_sum_equal": independent_region["partition_sum_ns"] == primary_reduction["partition_sum_ns"],
        "state_ns_equal": independent_region["state_totals_ns"] == primary_reduction["state_ns"],
        "independent_whole_trace_handler_transition_count": entry["transition_validation"]["handler_transition_count"],
        "primary_handler_transition_count": primary_reduction["handler_transition_count"],
        "transition_count_relation": "descriptive-only; implementations use different transition scopes",
        "primary_missing_stack_ns": primary_reduction["missing_stack_ns"],
        "primary_attribution_complete": primary_reduction["attribution_complete"],
    }
    comparison["equal"] = (
        all(marker_equal.values())
        and comparison["region_duration_equal"]
        and comparison["partition_sum_equal"]
        and comparison["state_ns_equal"]
    )
    comparisons.append(comparison)
report = {
    "schema_version": 1,
    "independent_path": str(independent_path),
    "independent_sha256": sha256(independent_path),
    "index_path": str(index_path),
    "index_sha256": sha256(index_path),
    "accepted": len(comparisons) == 4 and all(row["equal"] for row in comparisons),
    "comparisons": comparisons,
}
output_path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")
raise SystemExit(0 if report["accepted"] else 1)

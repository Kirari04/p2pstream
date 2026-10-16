import json
import re
import statistics
import sys
from pathlib import Path

source, destination = map(Path, sys.argv[1:])
rows = [json.loads(line) for line in source.read_text(encoding="utf-8").splitlines() if line.strip()]
sha256 = re.compile(r"[0-9a-f]{64}")
git_ref = re.compile(r"[0-9a-f]{40}")
variants = {
    "current": "aggregate-runtime-current",
    "historical": "aggregate-runtime-historical",
}
expected_orders = [
    [variants["current"], variants["historical"]],
    [variants["historical"], variants["current"]],
    [variants["current"], variants["historical"]],
]
result = {
    "scope": "diagnostic_only",
    "row_count": len(rows),
    "predeclared_gates": {
        "every_pair_download_historical_to_current_ratio_minimum": 1.10,
        "every_pair_upload_historical_to_current_ratio_minimum": 0.90,
        "payload_and_event_conservation": "exact",
        "selective_retries": False,
    },
    "fixed_workload": {
        "pairs": 3,
        "mode_order": ["current-then-historical", "historical-then-current", "current-then-historical"],
        "transfers_per_variant": 16,
        "total_transfers": 96,
        "warmup_mib": 256,
        "steady_mib": 256,
        "transfer_repetitions": 3,
        "required_pool_mib": 8,
        "configured_rtt_ms": 80,
        "transport_mode_both_variants": "aggregate-v1",
    },
    "pairs": {},
}
failures = []
if len(rows) != 96:
    failures.append(f"row count {len(rows)}, want 96")


def rate_summary(selected):
    values = [row["useful_transfer_bytes_per_second"] for row in selected]
    return {
        "count": len(values),
        "samples": values,
        "minimum": min(values) if values else None,
        "median": statistics.median(values) if values else None,
        "maximum": max(values) if values else None,
    }

for index, row in enumerate(rows, 1):
    if row.get("transport_mode") != "aggregate-v1":
        failures.append(f"row {index} transport mode is not aggregate-v1")
    exact = (
        row.get("attempted") == 1
        and row.get("useful_completed") == 1
        and row.get("errors") == 0
        and row.get("status_classes", {}).get("2xx") == 1
        and row.get("configured_payload_bytes") == 256 * 1024 * 1024
        and row.get("response_bytes")
        == (256 * 1024 * 1024 if row.get("transfer_direction") == "download" else 32)
        and row.get("persisted_event_delta") == 1
        and row.get("dropped_event_delta", 0) == 0
        and row.get("event_queue_after_drain", 0) == 0
        and not row.get("event_drain_error")
        and row.get("configured_rtt_ms") == 80
        and row.get("shaped_rate_bits_per_second") == 1_000_000_000
        and len(row.get("agent_tunnels_final", [])) == 1
        and row.get("agent_tunnels_final", [{}])[0].get("flow_control") == "aggregate-v1"
    )
    if row.get("measurement_stage") == "steady":
        exact = exact and row.get("receiving_pool_observed_bytes", 0) > 8 * 1024 * 1024
    if not exact:
        failures.append(f"row {index} failed request/payload/event conservation")
    provenance = row.get("provenance") or {}
    expected_server_key = (
        "current_server_binary_sha256"
        if row.get("label") == variants["current"]
        else "historical_server_binary_sha256"
    )
    expected_agent_key = (
        "current_agent_binary_sha256"
        if row.get("label") == variants["current"]
        else "historical_agent_binary_sha256"
    )
    provenance_exact = (
        row.get("label") in variants.values()
        and all(
            sha256.fullmatch(provenance.get(key) or "")
            for key in (
                "driver_binary_sha256",
                "current_server_binary_sha256",
                "historical_server_binary_sha256",
                "endpoint_server_binary_sha256",
                "endpoint_agent_binary_sha256",
                "current_agent_binary_sha256",
                "historical_agent_binary_sha256",
                "historical_overlay_sha256",
            )
        )
        and git_ref.fullmatch(provenance.get("app_source_ref") or "")
        and git_ref.fullmatch(provenance.get("current_yamux_ref") or "")
        and git_ref.fullmatch(provenance.get("historical_yamux_ref") or "")
        and provenance.get("endpoint_server_binary_sha256")
        == provenance.get(expected_server_key)
        and provenance.get("endpoint_agent_binary_sha256")
        == provenance.get(expected_agent_key)
    )
    if not provenance_exact:
        failures.append(f"row {index} failed endpoint/provenance identity")

for pair_index, expected_order in enumerate(expected_orders, 1):
    pair_rows = rows[(pair_index - 1) * 32 : pair_index * 32]
    observed_order = []
    for row in pair_rows:
        label = row.get("label")
        if label and (not observed_order or observed_order[-1] != label):
            observed_order.append(label)
    if observed_order != expected_order:
        failures.append(f"pair {pair_index} order {observed_order!r}, want {expected_order!r}")
    pair = {"label_order": observed_order, "variants": {}, "gates": {}}
    for variant, label in variants.items():
        variant_rows = [row for row in pair_rows if row.get("label") == label]
        if len(variant_rows) != 16:
            failures.append(f"pair {pair_index} {variant} has {len(variant_rows)} rows, want 16")
        variant_summary = {}
        for direction in ("download", "upload"):
            directional = [row for row in variant_rows if row.get("transfer_direction") == direction]
            warm = [row for row in directional if row.get("measurement_stage") == "warmup"]
            retained = [row for row in directional if row.get("measurement_stage") == "retained-warmup"]
            timed = sorted(
                [row for row in directional if row.get("measurement_stage") == "steady"],
                key=lambda row: row.get("repetition", 0),
            )
            if len(warm) != 4 or len(retained) != 1 or len(timed) != 3:
                failures.append(
                    f"pair {pair_index} {variant} {direction} stages are "
                    f"warm={len(warm)}, retained={len(retained)}, timed={len(timed)}"
                )
            variant_summary[direction] = {
                "fresh_lane_warmups": rate_summary(warm),
                "retained_transport_warmup": rate_summary(retained),
                "timed_all": rate_summary(timed),
                "timed_first_direction_uses": rate_summary(timed[:2]),
                "timed_reused_direction": rate_summary(timed[2:3]),
            }
        pair["variants"][variant] = variant_summary
    for direction in ("download", "upload"):
        current_median = pair["variants"]["current"][direction]["timed_all"]["median"]
        historical_median = pair["variants"]["historical"][direction]["timed_all"]["median"]
        ratio = historical_median / current_median if current_median else None
        minimum = 1.10 if direction == "download" else 0.90
        passed = ratio is not None and ratio >= minimum
        pair["gates"][direction] = {
            "current_median_useful_bytes_per_second": current_median,
            "historical_median_useful_bytes_per_second": historical_median,
            "historical_to_current_ratio": ratio,
            "minimum": minimum,
            "passed": passed,
        }
        if not passed:
            failures.append(f"pair {pair_index} {direction} historical/current ratio {ratio!r} below {minimum}")
    result["pairs"][str(pair_index)] = pair

for direction in ("download", "upload"):
    current_rows = [
        row for row in rows
        if row.get("label") == variants["current"]
        and row.get("measurement_stage") == "steady"
        and row.get("transfer_direction") == direction
    ]
    historical_rows = [
        row for row in rows
        if row.get("label") == variants["historical"]
        and row.get("measurement_stage") == "steady"
        and row.get("transfer_direction") == direction
    ]
    current_summary = rate_summary(current_rows)
    historical_summary = rate_summary(historical_rows)
    result.setdefault("pooled_diagnostic", {})[direction] = {
        "current": current_summary,
        "historical": historical_summary,
        "historical_to_current_ratio_of_medians": historical_summary["median"] / current_summary["median"] if current_summary["median"] else None,
    }

result["failures"] = failures
result["acceptance"] = {
    "complete": len(rows) == 96,
    "all_rows_exact": not any(
        "conservation" in failure or "transport mode" in failure or "provenance identity" in failure
        for failure in failures
    ),
    "all_pair_gates_passed": not any("ratio" in failure for failure in failures),
    "overall": not failures,
}
destination.write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")
raise SystemExit(0 if not failures else 1)

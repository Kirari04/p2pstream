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
    "control": "refund-current-batch-control",
    "deferred": "refund-streams-before-global-epoch",
}
expected_orders = [
    [variants["control"], variants["deferred"]],
    [variants["deferred"], variants["control"]],
    [variants["control"], variants["deferred"]],
]
result = {
    "scope": "diagnostic_only",
    "row_count": len(rows),
    "predeclared_gates": {
        "every_pair_download_deferred_to_control_ratio_minimum": 1.10,
        "every_pair_upload_deferred_to_control_ratio_minimum": 0.90,
        "deferred_all_24_download_samples_minimum_bytes_per_second": 29_700_000,
        "control_pairs_with_subthreshold_download_sample_minimum": 2,
        "payload_and_event_conservation": "exact",
        "selective_retries": False,
    },
    "fixed_workload": {
        "pairs": 3,
        "mode_order": ["control-then-deferred", "deferred-then-control", "control-then-deferred"],
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
transition_threshold = 29_700_000
control_pairs_with_subthreshold_download = 0
deferred_download_samples = []
all_upload_pair_gates_passed = True
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
        and row.get("status_codes") == {"200": 1}
        and row.get("observed_http_major") == 2
        and row.get("observed_origin_http_major") == 1
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
        "control_server_binary_sha256"
        if row.get("label") == variants["control"]
        else "deferred_server_binary_sha256"
    )
    expected_agent_key = (
        "control_agent_binary_sha256"
        if row.get("label") == variants["control"]
        else "deferred_agent_binary_sha256"
    )
    provenance_exact = (
        row.get("label") in variants.values()
        and all(
            sha256.fullmatch(provenance.get(key) or "")
            for key in (
                "driver_binary_sha256",
                "control_server_binary_sha256",
                "deferred_server_binary_sha256",
                "endpoint_server_binary_sha256",
                "endpoint_agent_binary_sha256",
                "control_agent_binary_sha256",
                "deferred_agent_binary_sha256",
                "common_scaffold_sha256",
                "deferred_global_overlay_sha256",
            )
        )
        and git_ref.fullmatch(provenance.get("base_source_ref") or "")
        and git_ref.fullmatch(provenance.get("control_source_ref") or "")
        and git_ref.fullmatch(provenance.get("deferred_source_ref") or "")
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
        control_median = pair["variants"]["control"][direction]["timed_all"]["median"]
        deferred_median = pair["variants"]["deferred"][direction]["timed_all"]["median"]
        ratio = deferred_median / control_median if control_median else None
        minimum = 1.10 if direction == "download" else 0.90
        passed = ratio is not None and ratio >= minimum
        pair["gates"][direction] = {
            "control_median_useful_bytes_per_second": control_median,
            "deferred_median_useful_bytes_per_second": deferred_median,
            "deferred_to_control_ratio": ratio,
            "minimum": minimum,
            "passed": passed,
        }
        if not passed:
            failures.append(f"pair {pair_index} {direction} deferred/control ratio {ratio!r} below {minimum}")
        if direction == "upload" and not passed:
            all_upload_pair_gates_passed = False
    control_pair_download = [
        row["useful_transfer_bytes_per_second"]
        for row in pair_rows
        if row.get("label") == variants["control"] and row.get("transfer_direction") == "download"
    ]
    deferred_pair_download = [
        row["useful_transfer_bytes_per_second"]
        for row in pair_rows
        if row.get("label") == variants["deferred"] and row.get("transfer_direction") == "download"
    ]
    if control_pair_download and min(control_pair_download) < transition_threshold:
        control_pairs_with_subthreshold_download += 1
    deferred_download_samples.extend(deferred_pair_download)
    pair["transition_screen"] = {
        "threshold_bytes_per_second": transition_threshold,
        "control_download_minimum": min(control_pair_download) if control_pair_download else None,
        "control_has_subthreshold_sample": bool(control_pair_download and min(control_pair_download) < transition_threshold),
        "deferred_download_minimum": min(deferred_pair_download) if deferred_pair_download else None,
        "deferred_all_eight_download_samples_pass": bool(
            len(deferred_pair_download) == 8 and min(deferred_pair_download) >= transition_threshold
        ),
    }
    result["pairs"][str(pair_index)] = pair

for direction in ("download", "upload"):
    control_rows = [
        row for row in rows
        if row.get("label") == variants["control"]
        and row.get("measurement_stage") == "steady"
        and row.get("transfer_direction") == direction
    ]
    deferred_rows = [
        row for row in rows
        if row.get("label") == variants["deferred"]
        and row.get("measurement_stage") == "steady"
        and row.get("transfer_direction") == direction
    ]
    control_summary = rate_summary(control_rows)
    deferred_summary = rate_summary(deferred_rows)
    result.setdefault("pooled_diagnostic", {})[direction] = {
        "control": control_summary,
        "deferred": deferred_summary,
        "deferred_to_control_ratio_of_medians": deferred_summary["median"] / control_summary["median"] if control_summary["median"] else None,
    }

ratio_failure = re.compile(r"^pair [1-3] (?:download|upload) deferred/control ratio .+ below (?:1\.1|0\.9)$")
timing_failures = [failure for failure in failures if ratio_failure.fullmatch(failure)]
non_timing_failures = [failure for failure in failures if not ratio_failure.fullmatch(failure)]
result["failures"] = failures
result["timing_gate_failures"] = timing_failures
result["non_timing_failures"] = non_timing_failures
result["acceptance"] = {
    "complete": len(rows) == 96,
    "all_rows_exact": not non_timing_failures,
    "no_non_timing_failures": not non_timing_failures,
    "all_pair_gates_passed": not timing_failures,
    "overall": not failures,
}
result["transition_screen"] = {
    "diagnostic_only": True,
    "does_not_override_main_pair_gates": True,
    "threshold_bytes_per_second": transition_threshold,
    "deferred_download_sample_count": len(deferred_download_samples),
    "deferred_download_minimum": min(deferred_download_samples) if deferred_download_samples else None,
    "deferred_all_24_download_samples_pass": bool(
        len(deferred_download_samples) == 24 and min(deferred_download_samples) >= transition_threshold
    ),
    "control_pairs_with_subthreshold_download_sample": control_pairs_with_subthreshold_download,
    "control_pairs_with_subthreshold_download_sample_minimum": 2,
    "all_pair_upload_gates_passed": all_upload_pair_gates_passed,
    "exact_correctness_and_cleanup": bool(
        result["acceptance"]["complete"]
        and result["acceptance"]["all_rows_exact"]
        and result["acceptance"]["no_non_timing_failures"]
    ),
}
result["transition_screen"]["consistent_with_stall_elimination"] = bool(
    result["transition_screen"]["deferred_all_24_download_samples_pass"]
    and control_pairs_with_subthreshold_download >= 2
    and all_upload_pair_gates_passed
    and result["transition_screen"]["exact_correctness_and_cleanup"]
)
result["all_phase_rows"] = [
    {
        "sequence": index,
        "pair": (index - 1) // 32 + 1,
        "within_pair": (index - 1) % 32 + 1,
        "label": row.get("label"),
        "measurement_stage": row.get("measurement_stage"),
        "transfer_direction": row.get("transfer_direction"),
        "repetition": row.get("repetition"),
        "useful_transfer_bytes_per_second": row.get("useful_transfer_bytes_per_second"),
        "wall_seconds": row.get("wall_seconds"),
    }
    for index, row in enumerate(rows, 1)
]
destination.write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")
raise SystemExit(0 if not failures else 1)

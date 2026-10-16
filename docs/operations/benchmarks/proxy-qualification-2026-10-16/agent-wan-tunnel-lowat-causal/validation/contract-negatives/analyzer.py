import json
import re
import statistics
import sys
from pathlib import Path

source, socket_source, destination = map(Path, sys.argv[1:])
rows = [json.loads(line) for line in source.read_text(encoding="utf-8").splitlines() if line.strip()]
socket_rows = [json.loads(line) for line in socket_source.read_text(encoding="utf-8").splitlines() if line.strip()]
sha256 = re.compile(r"[0-9a-f]{64}")
git_ref = re.compile(r"[0-9a-f]{40}")
variants = {
    "control": "tunnel-lowat-default-control",
    "lowat": "tunnel-lowat-32k",
}
expected_orders = [
    [variants["control"], variants["lowat"]],
    [variants["lowat"], variants["control"]],
    [variants["control"], variants["lowat"]],
]
result = {
    "scope": "diagnostic_only",
    "row_count": len(rows),
    "tunnel_socket_snapshot_count": len(socket_rows),
    "predeclared_gates": {
        "every_pair_download_lowat_to_control_ratio_minimum": 1.10,
        "every_pair_upload_lowat_to_control_ratio_minimum": 0.90,
        "lowat_all_24_download_samples_minimum_bytes_per_second": 29_700_000,
        "control_pairs_with_subthreshold_download_sample_minimum": 2,
        "physical_tunnel_socket_records": "exactly five per endpoint, reverse-correlated and unchanged after load",
        "lowat_effective_baseline": "role-matched across arms and greater than 32768",
        "candidate_notsent_lowat_after": 32768,
        "live_socket_option_polling": False,
        "payload_and_event_conservation": "exact",
        "selective_retries": False,
    },
    "fixed_workload": {
        "pairs": 3,
        "mode_order": ["control-then-lowat", "lowat-then-control", "control-then-lowat"],
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
lowat_download_samples = []
all_upload_pair_gates_passed = True
if len(rows) != 96:
    failures.append(f"row count {len(rows)}, want 96")
if len(socket_rows) != 12:
    failures.append(f"tunnel socket snapshot count {len(socket_rows)}, want 12")
expected_socket_sequence = [
    (iteration, label, stage)
    for iteration, order in enumerate(expected_orders, 1)
    for label in order
    for stage in ("before-load", "after-load")
]
observed_socket_sequence = [
    (row.get("iteration"), row.get("label"), row.get("stage")) for row in socket_rows
]
if observed_socket_sequence != expected_socket_sequence:
    failures.append("tunnel socket snapshot order does not match the fixed arm/stage sequence")


def socket_endpoint(value):
    import ipaddress
    if not isinstance(value, str) or not value:
        raise ValueError(value)
    if value.startswith("["):
        host, separator, port = value[1:].partition("]:")
        if not separator:
            raise ValueError(value)
    else:
        host, port = value.rsplit(":", 1)
    parsed_port = int(port)
    if parsed_port < 1 or parsed_port > 65535:
        raise ValueError(value)
    return str(ipaddress.ip_address(host)), parsed_port


socket_error_fields = (
    "namespace_notsent_lowat_error", "notsent_lowat_before_error",
    "notsent_lowat_after_error", "send_buffer_before_error",
    "send_buffer_after_error", "receive_buffer_before_error",
    "receive_buffer_after_error", "setter_error", "unwrap_error",
    "syscall_conn_error", "control_error",
)
socket_lowat_fields = (
    "requested_notsent_lowat", "namespace_notsent_lowat",
    "notsent_lowat_before", "notsent_lowat_after",
)
socket_buffer_fields = (
    "send_buffer_before", "send_buffer_after",
    "receive_buffer_before", "receive_buffer_after",
)
socket_boolean_fields = (
    "platform_supported", "apply_requested", "setter_attempted", "conclusive",
)


def valid_exact_int(mapping, name, minimum, maximum):
    return isinstance(mapping, dict) and name in mapping and type(mapping[name]) is int and minimum <= mapping[name] <= maximum


def valid_exact_bool(mapping, name):
    return isinstance(mapping, dict) and name in mapping and type(mapping[name]) is bool


def valid_empty_string(mapping, name):
    return isinstance(mapping, dict) and name in mapping and isinstance(mapping[name], str) and mapping[name] == ""


def validate_socket_response(response, role, candidate, context):
    if not isinstance(response, dict):
        failures.append(f"{context} {role} socket response is not an object")
        return [], [], [], []
    if not valid_exact_int(response, "schema_version", 1, 1) or response.get("process_role") != role:
        failures.append(f"{context} {role} socket schema/role mismatch")
    if "process_role" not in response or not isinstance(response["process_role"], str):
        failures.append(f"{context} {role} socket process role is missing or mistyped")
    if not valid_exact_int(response, "captured_unix_nano", 1, (1 << 63) - 1):
        failures.append(f"{context} {role} socket capture timestamp is missing")
    if (
        not valid_exact_int(response, "capacity", 0, (1 << 31) - 1)
        or not valid_exact_int(response, "dropped_observations", 0, (1 << 64) - 1)
        or response.get("capacity") != 64
        or response.get("dropped_observations") != 0
    ):
        failures.append(f"{context} {role} socket capacity/drop mismatch")
    observations = response.get("observations") if "observations" in response else None
    if not isinstance(observations, list) or len(observations) != 5:
        failures.append(f"{context} {role} socket observation count is not exactly 5")
        return [], [], [], []
    effective_baselines = []
    raw_baselines = []
    namespace_baselines = []
    for expected_index, item in enumerate(observations):
        if not isinstance(item, dict) or not valid_exact_int(item, "index", 0, 63) or item["index"] != expected_index:
            failures.append(f"{context} {role} socket indexes are not exactly 0..4")
        if not isinstance(item, dict) or "socket" not in item or not isinstance(item["socket"], dict):
            failures.append(f"{context} {role} socket object is missing or mistyped")
            continue
        socket = item["socket"]
        try:
            if "local_address" not in socket or "remote_address" not in socket:
                raise ValueError("missing address")
            socket_endpoint(socket["local_address"])
            socket_endpoint(socket["remote_address"])
        except (TypeError, ValueError):
            failures.append(f"{context} {role} has an invalid socket tuple")
        if any(not valid_exact_bool(socket, field) for field in socket_boolean_fields):
            failures.append(f"{context} {role} has a missing or mistyped socket boolean")
        elif not socket["conclusive"] or not socket["platform_supported"]:
            failures.append(f"{context} {role} has a nonconclusive socket")
        if any(not valid_empty_string(socket, field) for field in (*socket_error_fields, "inconclusive_reason")):
            failures.append(f"{context} {role} has a missing, mistyped, or nonempty socket error field")
        if any(not valid_exact_int(socket, field, 0, (1 << 32) - 1) for field in socket_lowat_fields):
            failures.append(f"{context} {role} has a missing or invalid LOWAT integer")
            raw_baseline = 0
            namespace_baseline = 0
        else:
            raw_baseline = socket["notsent_lowat_before"]
            namespace_baseline = socket["namespace_notsent_lowat"]
            if socket["requested_notsent_lowat"] != 32768:
                failures.append(f"{context} {role} requested LOWAT is not 32768")
        if any(not valid_exact_int(socket, field, 1, (1 << 63) - 1) for field in socket_buffer_fields):
            failures.append(f"{context} {role} has a missing or invalid observed socket buffer")
        baseline = raw_baseline or namespace_baseline
        raw_baselines.append(raw_baseline)
        namespace_baselines.append(namespace_baseline)
        effective_baselines.append(baseline)
        if candidate:
            if socket.get("apply_requested") is not True or socket.get("setter_attempted") is not True:
                failures.append(f"{context} {role} candidate did not attempt LOWAT")
            if socket.get("requested_notsent_lowat") != 32768 or socket.get("notsent_lowat_after") != 32768:
                failures.append(f"{context} {role} candidate LOWAT is not 32768")
        else:
            if socket.get("apply_requested") is not False or socket.get("setter_attempted") is not False:
                failures.append(f"{context} {role} control attempted LOWAT")
            if socket.get("notsent_lowat_after") != socket.get("notsent_lowat_before"):
                failures.append(f"{context} {role} control LOWAT changed")
    return observations, sorted(effective_baselines), sorted(raw_baselines), sorted(namespace_baselines)


socket_evidence = {}
for iteration in range(1, 4):
    iteration_evidence = {}
    for variant, label in variants.items():
        selected = [row for row in socket_rows if row.get("iteration") == iteration and row.get("label") == label]
        by_stage = {row.get("stage"): row for row in selected}
        context = f"pair {iteration} {variant}"
        if len(selected) != 2 or set(by_stage) != {"before-load", "after-load"}:
            failures.append(f"{context} socket stages are not exactly before-load/after-load")
            continue
        before, after = by_stage["before-load"], by_stage["after-load"]
        candidate = variant == "lowat"
        if before.get("validation_ok") is not True or before.get("validation_error"):
            failures.append(f"{context} before-load online socket validation failed")
        if after.get("validation_ok") is not True or after.get("validation_error"):
            failures.append(f"{context} after-load online socket validation failed")
        before_server, server_baselines, server_raw, server_namespace = validate_socket_response(
            before.get("server") or {}, "server", candidate, context + " before-load"
        )
        before_agent, agent_baselines, agent_raw, agent_namespace = validate_socket_response(
            before.get("agent") or {}, "agent", candidate, context + " before-load"
        )
        after_server, after_server_baselines, after_server_raw, after_server_namespace = validate_socket_response(
            after.get("server") or {}, "server", candidate, context + " after-load"
        )
        after_agent, after_agent_baselines, after_agent_raw, after_agent_namespace = validate_socket_response(
            after.get("agent") or {}, "agent", candidate, context + " after-load"
        )
        if before_server != after_server or before_agent != after_agent:
            failures.append(f"{context} physical socket observation set changed during load")
        if (
            server_baselines != after_server_baselines or agent_baselines != after_agent_baselines
            or server_raw != after_server_raw or agent_raw != after_agent_raw
            or server_namespace != after_server_namespace or agent_namespace != after_agent_namespace
        ):
            failures.append(f"{context} physical socket effective baseline changed during load")
        server_lane_fields_valid = True
        for row in before_server:
            if (
                not valid_exact_int(row, "lane", 0, 4)
                or "lane_role" not in row or not isinstance(row["lane_role"], str)
                or not valid_exact_bool(row, "aggregate")
            ):
                server_lane_fields_valid = False
                failures.append(f"{context} server lane metadata is missing or mistyped")
        if not server_lane_fields_valid or sorted(row["lane"] for row in before_server if valid_exact_int(row, "lane", 0, 4)) != list(range(5)):
            failures.append(f"{context} server lanes are not exactly 0..4")
        for row in (before_server if server_lane_fields_valid else []):
            lane = row.get("lane")
            if row.get("lane_role") != ("control" if lane == 0 else "bulk") or row.get("aggregate") is not True:
                failures.append(f"{context} server lane {lane!r} role/mode mismatch")
        try:
            agent_tuples = {
                (socket_endpoint(row["socket"]["local_address"]), socket_endpoint(row["socket"]["remote_address"])): row["index"]
                for row in before_agent
            }
            if len(agent_tuples) != len(before_agent):
                failures.append(f"{context} contains duplicate agent socket tuples")
            matches = []
            for row in sorted(before_server, key=lambda value: value["lane"]):
                reverse = (
                    socket_endpoint(row["socket"]["remote_address"]),
                    socket_endpoint(row["socket"]["local_address"]),
                )
                if reverse not in agent_tuples:
                    raise KeyError(reverse)
                matches.append({"lane": row["lane"], "server_index": row["index"], "agent_index": agent_tuples.pop(reverse)})
            if agent_tuples:
                failures.append(f"{context} contains unmatched agent socket tuples")
        except (KeyError, TypeError, ValueError):
            matches = []
            failures.append(f"{context} reverse socket tuple matching failed")
        if before.get("reverse_tuple_matches") != matches or after.get("reverse_tuple_matches") != matches:
            failures.append(f"{context} recorded reverse tuple mapping mismatch")
        iteration_evidence[variant] = {
            "server_effective_baselines": server_baselines,
            "agent_effective_baselines": agent_baselines,
            "server_raw_baselines": server_raw,
            "agent_raw_baselines": agent_raw,
            "server_namespace_baselines": server_namespace,
            "agent_namespace_baselines": agent_namespace,
            "reverse_tuple_matches": matches,
            "observations_stable_during_load": before_server == after_server and before_agent == after_agent,
        }
    if set(iteration_evidence) == {"control", "lowat"}:
        for role in ("server", "agent"):
            control_baselines = iteration_evidence["control"][f"{role}_effective_baselines"]
            lowat_baselines = iteration_evidence["lowat"][f"{role}_effective_baselines"]
            if control_baselines != lowat_baselines:
                failures.append(f"pair {iteration} {role} effective LOWAT baselines differ across arms")
            if iteration_evidence["control"][f"{role}_raw_baselines"] != iteration_evidence["lowat"][f"{role}_raw_baselines"]:
                failures.append(f"pair {iteration} {role} raw LOWAT baselines differ across arms")
            if iteration_evidence["control"][f"{role}_namespace_baselines"] != iteration_evidence["lowat"][f"{role}_namespace_baselines"]:
                failures.append(f"pair {iteration} {role} namespace LOWAT baselines differ across arms")
            if len(control_baselines) != 5 or any(value <= 32768 for value in control_baselines):
                failures.append(f"pair {iteration} {role} effective LOWAT baseline does not establish a >32768 contrast")
    socket_evidence[str(iteration)] = iteration_evidence
result["tunnel_socket_evidence"] = socket_evidence


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
        else "lowat_server_binary_sha256"
    )
    expected_agent_key = (
        "control_agent_binary_sha256"
        if row.get("label") == variants["control"]
        else "lowat_agent_binary_sha256"
    )
    provenance_exact = (
        row.get("label") in variants.values()
        and all(
            sha256.fullmatch(provenance.get(key) or "")
            for key in (
                "driver_binary_sha256",
                "control_server_binary_sha256",
                "lowat_server_binary_sha256",
                "endpoint_server_binary_sha256",
                "endpoint_agent_binary_sha256",
                "control_agent_binary_sha256",
                "lowat_agent_binary_sha256",
                "common_scaffold_sha256",
                "lowat_32k_overlay_sha256",
            )
        )
        and git_ref.fullmatch(provenance.get("base_source_ref") or "")
        and git_ref.fullmatch(provenance.get("control_source_ref") or "")
        and git_ref.fullmatch(provenance.get("lowat_source_ref") or "")
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
        lowat_median = pair["variants"]["lowat"][direction]["timed_all"]["median"]
        ratio = lowat_median / control_median if control_median else None
        minimum = 1.10 if direction == "download" else 0.90
        passed = ratio is not None and ratio >= minimum
        pair["gates"][direction] = {
            "control_median_useful_bytes_per_second": control_median,
            "lowat_median_useful_bytes_per_second": lowat_median,
            "lowat_to_control_ratio": ratio,
            "minimum": minimum,
            "passed": passed,
        }
        if not passed:
            failures.append(f"pair {pair_index} {direction} lowat/control ratio {ratio!r} below {minimum}")
        if direction == "upload" and not passed:
            all_upload_pair_gates_passed = False
    control_pair_download = [
        row["useful_transfer_bytes_per_second"]
        for row in pair_rows
        if row.get("label") == variants["control"] and row.get("transfer_direction") == "download"
    ]
    lowat_pair_download = [
        row["useful_transfer_bytes_per_second"]
        for row in pair_rows
        if row.get("label") == variants["lowat"] and row.get("transfer_direction") == "download"
    ]
    if control_pair_download and min(control_pair_download) < transition_threshold:
        control_pairs_with_subthreshold_download += 1
    lowat_download_samples.extend(lowat_pair_download)
    pair["transition_screen"] = {
        "threshold_bytes_per_second": transition_threshold,
        "control_download_minimum": min(control_pair_download) if control_pair_download else None,
        "control_has_subthreshold_sample": bool(control_pair_download and min(control_pair_download) < transition_threshold),
        "lowat_download_minimum": min(lowat_pair_download) if lowat_pair_download else None,
        "lowat_all_eight_download_samples_pass": bool(
            len(lowat_pair_download) == 8 and min(lowat_pair_download) >= transition_threshold
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
    lowat_rows = [
        row for row in rows
        if row.get("label") == variants["lowat"]
        and row.get("measurement_stage") == "steady"
        and row.get("transfer_direction") == direction
    ]
    control_summary = rate_summary(control_rows)
    lowat_summary = rate_summary(lowat_rows)
    result.setdefault("pooled_diagnostic", {})[direction] = {
        "control": control_summary,
        "lowat": lowat_summary,
        "lowat_to_control_ratio_of_medians": lowat_summary["median"] / control_summary["median"] if control_summary["median"] else None,
    }

ratio_failure = re.compile(r"^pair [1-3] (?:download|upload) lowat/control ratio .+ below (?:1\.1|0\.9)$")
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
    "lowat_download_sample_count": len(lowat_download_samples),
    "lowat_download_minimum": min(lowat_download_samples) if lowat_download_samples else None,
    "lowat_all_24_download_samples_pass": bool(
        len(lowat_download_samples) == 24 and min(lowat_download_samples) >= transition_threshold
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
    result["transition_screen"]["lowat_all_24_download_samples_pass"]
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
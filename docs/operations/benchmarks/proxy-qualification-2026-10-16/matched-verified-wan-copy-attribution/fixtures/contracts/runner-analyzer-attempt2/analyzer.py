import importlib.util, json, statistics, sys

source, destination, required_pool_mib, smoke_mode, pressure_mode, tiny_response_mode, protocol, expected_fingerprint, copy_validator_path = sys.argv[1], sys.argv[2], int(sys.argv[3]), int(sys.argv[4]), int(sys.argv[5]), int(sys.argv[6]), sys.argv[7], sys.argv[8], sys.argv[9]
spec = importlib.util.spec_from_file_location("copy_record_validator", copy_validator_path)
copy_record_validator = importlib.util.module_from_spec(spec)
spec.loader.exec_module(copy_record_validator)
rows = [json.loads(line) for line in open(source, encoding="utf-8") if line.strip()]
if protocol in ("h1", "h2"):
    expected_major = 1 if protocol == "h1" else 2
    expected_alpn = "http/1.1" if protocol == "h1" else "h2"
    expected = [
        ("warmup", "download", repetition) for repetition in range(1, 5)
    ] + [
        ("warmup", "upload", repetition) for repetition in range(1, 5)
    ] + [
        ("retained-warmup", "download", 1),
        ("retained-warmup", "upload", 1),
    ]
    if smoke_mode:
        expected += [("steady", "download", 1), ("steady", "upload", 1), ("reuse-probe", "download", 1)]
    else:
        expected += [
            ("steady", "download", 1), ("steady", "upload", 1),
            ("steady", "upload", 2), ("steady", "download", 2),
            ("steady", "download", 3), ("steady", "upload", 3),
        ]
    expected_stages = {"warmup": 8, "retained-warmup": 2, "steady": 2 if smoke_mode else 6}
    if smoke_mode:
        expected_stages["reuse-probe"] = 1
    stage_counts = {stage: sum(row.get("measurement_stage") == stage for row in rows) for stage in expected_stages}
    expected_rows = sum(expected_stages.values())
    actual = [(row.get("measurement_stage"), row.get("transfer_direction"), row.get("repetition")) for row in rows]
    if len(rows) != expected_rows or stage_counts != expected_stages or actual != expected:
        raise SystemExit(f"diagnostic row schedule mismatch: rows={len(rows)} stages={stage_counts} actual={actual!r}")
    correctness = all(
        row.get("attempted") == 1 and row.get("useful_completed") == 1 and row.get("errors") == 0 and
        row.get("status_classes") == {"2xx": 1} and row.get("status_codes") == {"200": 1} and
        row.get("transport_mode") == "aggregate-v1" and row.get("public_protocol") == protocol and
        row.get("configured_payload_bytes") == ((4 if smoke_mode else 256) << 20) and
        row.get("response_bytes") == (((4 if smoke_mode else 256) << 20) if row.get("transfer_direction") == "download" else 32) and
        isinstance(row.get("wall_seconds"), (int, float)) and row.get("wall_seconds") > 0 and
        isinstance(row.get("useful_transfer_bytes_per_second"), (int, float)) and row.get("useful_transfer_bytes_per_second") > 0 and
        row.get("event_stats_captured") is True and
        row.get("persisted_event_delta") == 1 and row.get("dropped_event_delta", 0) == 0 and
        row.get("event_queue_after_drain", 0) == 0 and not row.get("event_drain_error")
        for row in rows)
    tls_complete = all(
        len(row.get("wan_response_tls", [])) == 1 and
        row["wan_response_tls"][0].get("http_major") == expected_major and
        row["wan_response_tls"][0].get("negotiated_alpn") == expected_alpn and
        row["wan_response_tls"][0].get("peer_leaf_der_sha256") == expected_fingerprint and
        row["wan_response_tls"][0].get("origin_connection_id") and
        not row["wan_response_tls"][0].get("validation_error")
        for row in rows)
    copy_issues_by_sequence = {}
    for expected_sequence, row in enumerate(rows, 1):
        found = copy_record_validator.validate_copy_envelope(
            row.get("response_copy_attribution", {}), expected_sequence,
            row.get("transfer_direction"), row.get("configured_payload_bytes"))
        if found:
            copy_issues_by_sequence[str(expected_sequence)] = found
    copy_complete = not copy_issues_by_sequence
    trace_rows = []
    trace_complete = True
    for expected_sequence, row in enumerate(rows, 1):
        envelope = row.get("upstream_transport_trace", {})
        matches = envelope.get("matches", [])
        # Sequence is retained in the raw trace and the runner query; method/path and
        # callback cardinality are the fail-closed correlation checks.
        if len(matches) == 1:
            trace = matches[0]
            expected_method = "GET" if row["transfer_direction"] == "download" else "POST"
            expected_path = "/agent/bulk" if row["transfer_direction"] == "download" else "/agent/bulk-upload"
            valid = (envelope.get("overflow") is False and envelope.get("store_count") == expected_sequence and
                     row.get("diagnostic_sequence") == expected_sequence and
                     envelope.get("sequence") == str(expected_sequence) and trace.get("sequence") == str(expected_sequence) and
                     trace.get("method") == expected_method and
                     trace.get("path") == expected_path and trace.get("callback_overflow") is False and
                     len(trace.get("got_conn", [])) == 1 and len(trace.get("put_idle_conn", [])) <= 1)
        else:
            trace = {}
            valid = False
        trace_complete = trace_complete and valid
        got = trace.get("got_conn", [{}])[0] if len(trace.get("got_conn", [])) == 1 else {}
        connection_parts = (got.get("connection_type"), got.get("local_address"), got.get("remote_address"))
        connection_key = "|".join(connection_parts) if all(isinstance(part, str) and part for part in connection_parts) else ""
        trace_rows.append({
            "stage": row["measurement_stage"], "direction": row["transfer_direction"],
            "repetition": row["repetition"], "valid": valid, "reused": got.get("reused"),
            "was_idle": got.get("was_idle"), "idle_time_ns": got.get("idle_time_ns"),
            "got_conn_observed_unix_ns": got.get("observed_unix_ns"), "connection_key": connection_key,
            "connection_type": got.get("connection_type"), "local_address": got.get("local_address"),
            "remote_address": got.get("remote_address"),
            "put_idle_conn": trace.get("put_idle_conn", []),
            "origin_connection_id": row["wan_response_tls"][0].get("origin_connection_id") if row.get("wan_response_tls") else None,
        })
    download_floor = 29_700_000
    download_rows = [row for row in rows if row["transfer_direction"] == "download" and row["measurement_stage"] != "reuse-probe"]
    floor_by_stage = {}
    for stage in ("warmup", "retained-warmup", "steady"):
        values = [row["useful_transfer_bytes_per_second"] for row in download_rows if row["measurement_stage"] == stage]
        floor_by_stage[stage] = {"samples": len(values), "minimum_bytes_per_second": min(values),
                                 "all_at_or_above_floor": all(value >= download_floor for value in values)}
    floor_pass = all(value["all_at_or_above_floor"] for value in floor_by_stage.values())
    floor_gate_pass = floor_pass or bool(smoke_mode)
    reuse_issues = []
    reuse_expectations = []
    first_visits = trace_rows[8:12]
    if len(first_visits) != 4:
        reuse_issues.append("missing four first post-close lane-key visits")
    first_by_key = {}
    for position, trace in enumerate(first_visits, 1):
        key = trace["connection_key"]
        put = trace["put_idle_conn"]
        successful_put = (len(put) == 1 and isinstance(put[0].get("observed_unix_ns"), int)
                          and put[0]["observed_unix_ns"] > 0 and not put[0].get("error"))
        expected_fresh = (key and key not in first_by_key and trace["reused"] is False
                          and trace["was_idle"] is False and trace["idle_time_ns"] == 0)
        if not expected_fresh:
            reuse_issues.append(f"sequence {position + 8} is not a distinct expected-fresh post-close key")
        if not successful_put:
            reuse_issues.append(f"sequence {position + 8} lacks a successful PutIdleConn timestamp")
        if key:
            first_by_key[key] = trace
        reuse_expectations.append({"sequence": position + 8, "kind": "first-post-close-visit", "lane_position": position,
                                   "connection_key": key, "satisfied": bool(expected_fresh and successful_put)})
    revisits = trace_rows[12:]
    for revisit_index, trace in enumerate(revisits):
        expected_first = first_visits[revisit_index % 4] if len(first_visits) == 4 else {}
        expected_key = expected_first.get("connection_key")
        prior_put = expected_first.get("put_idle_conn", [])
        got_ns = trace.get("got_conn_observed_unix_ns")
        put_ns = prior_put[0].get("observed_unix_ns") if len(prior_put) == 1 else None
        elapsed_idle_ns = got_ns - put_ns if isinstance(got_ns, int) and isinstance(put_ns, int) else None
        satisfied = bool(expected_key and trace["connection_key"] == expected_key and
                         trace["reused"] is True and trace["was_idle"] is True and
                         isinstance(trace["idle_time_ns"], int) and trace["idle_time_ns"] >= 0 and
                         isinstance(elapsed_idle_ns, int) and 0 <= elapsed_idle_ns < 30_000_000_000 and
                         trace["origin_connection_id"] == expected_first.get("origin_connection_id"))
        if not satisfied:
            reuse_issues.append(f"sequence {revisit_index + 13} did not reuse its bound post-close lane key")
        reuse_expectations.append({
            "sequence": revisit_index + 13, "kind": "same-key-revisit", "lane_position": revisit_index % 4 + 1,
            "connection_key": trace["connection_key"],
            "expected_connection_key": expected_key, "prior_put_idle_unix_ns": put_ns,
            "got_conn_unix_ns": got_ns, "elapsed_idle_ns": elapsed_idle_ns,
            "httptrace_idle_time_ns": trace.get("idle_time_ns"),
            "prior_origin_connection_id": expected_first.get("origin_connection_id"),
            "observed_origin_connection_id": trace.get("origin_connection_id"), "satisfied": satisfied,
        })
    expected_reuse = not reuse_issues and len(revisits) == (1 if smoke_mode else 4)
    if len(revisits) != (1 if smoke_mode else 4):
        reuse_issues.append(f"same-key revisit count {len(revisits)} changed")
        expected_reuse = False
    classification = "invalid" if not (correctness and tls_complete and trace_complete and copy_complete) else ("inconclusive-upstream-reuse" if not expected_reuse else "observational-complete")
    summary = {
        "diagnostic_only": True, "public_protocol": protocol, "classification": classification,
        "acceptance": {"correctness": correctness, "verified_tls_and_protocol_every_response": tls_complete,
                       "transport_trace_complete": trace_complete, "expected_upstream_reuse": expected_reuse,
                       "response_copy_attribution_complete": copy_complete,
                       "download_floor_every_phase": floor_gate_pass},
        "gates": {"download_bytes_per_second_floor": download_floor}, "download_by_phase": floor_by_stage,
        "transport_traces": trace_rows, "reuse_expectations": reuse_expectations, "reuse_issues": reuse_issues,
        "reuse_counts": {"reused": sum(trace["reused"] is True for trace in trace_rows),
                         "new": sum(trace["reused"] is False for trace in trace_rows),
                         "put_idle_absent": sum(not trace["put_idle_conn"] for trace in trace_rows)},
        "copy_attribution_issues_by_sequence": copy_issues_by_sequence,
        "copy_attribution_interpretation": "Diagnostic observer only. Upstream Read and downstream Write durations are compared separately with handler wall time. Inter-read gaps include downstream Write time and bookkeeping and are never added as an independent component. POST observations cover only the 32-byte response copy, not upload request-body handling.",
        "interpretation": "Observational diagnostic only; throughput is not qualification evidence and medians do not establish health or causality. Warmup rows explicitly close idle transports. Sequences 9-12 are the first post-close visits to four concrete lane keys; only later same-key visits classify reuse, bound to the prior successful PutIdleConn timestamp and exact origin connection identity.",
    }
    with open(destination, "w", encoding="utf-8") as output_file:
        json.dump(summary, output_file, indent=2, sort_keys=True)
        output_file.write("\n")
    if classification != "observational-complete" or not all(summary["acceptance"].values()):
        raise SystemExit(f"WAN protocol diagnostic failed: {summary['acceptance']}")
    raise SystemExit(0)
if pressure_mode:
    result = next((row for row in rows if row.get("measurement_stage") == "pressure-result"), None)
    rejected = next((row for row in rows if row.get("measurement_stage") == "pressure-rejection"), None)
    control = next((row for row in rows if row.get("measurement_stage") == "pressure-control-roundtrip"), None)
    canceled = next((row for row in rows if row.get("measurement_stage") == "canceled-upload"), None)
    baseline = next((row for row in rows if row.get("measurement_stage") == "pressure-baseline"), None)
    recovered = next((row for row in rows if row.get("measurement_stage") == "pressure-recovered"), None)
    critical = next((row for row in rows if row.get("measurement_stage") == "pressure-critical"), None)
    if not result or not rejected or not control or not canceled or not baseline or not critical or not recovered:
        raise SystemExit("pressure qualification is missing required cancellation, pressure, control, rejection, or recovery rows")
    def receiving_pools(row, endpoint):
        if endpoint == "server":
            tunnels = row["server_runtime"].get("agent_tunnels", [])
            lanes = tunnels[0].get("lanes", []) if len(tunnels) == 1 else []
        else:
            lanes = row["agent_runtime"].get("tunnel_sessions", [])
        values = sorted(lane["receive_limit_bytes"] for lane in lanes
                        if lane.get("aggregate") is True and lane.get("lane", 0) > 0)
        if len(values) != 4:
            raise SystemExit(f"{row['measurement_stage']} {endpoint} receiving lane count = {len(values)}, want 4")
        return values
    baseline_server_pools = receiving_pools(baseline, "server")
    baseline_agent_pools = receiving_pools(baseline, "agent")
    recovered_server_pools = receiving_pools(recovered, "server")
    recovered_agent_pools = receiving_pools(recovered, "agent")
    required_pool_bytes = required_pool_mib << 20
    summary = {
        "acceptance": {
            "pressure_reached": result["pressure_reached"],
            "admission_rejected_under_pressure": rejected["rejected_pressure_after"] > rejected["rejected_pressure_before"],
            "functional_recovery": result["functional_recovery"],
            "control_progress_during_pressure": result["control_progress_during_pressure"],
            "ordinary_admission_closed_during_pressure":
                critical["agent_runtime"]["agent_capacity"]["ordinary_admission_limit"] == 0 and
                control["ordinary_admission_limit_before"] == 0 and
                control["ordinary_admission_limit_after"] == 0,
            "control_reservation_owned":
                control["control_reserved_slots"] > 0 and
                control["control_reserved_base_bytes"] > 0 and
                control["control_reserved_base_fds"] > 0,
            "control_slot_returned_to_baseline":
                control["control_in_use_before"] == 0 and control["control_in_use_after"] == 0,
            "control_did_not_consume_ordinary_pressure_rejection":
                control["agent_rejected_pressure_after"] == control["agent_rejected_pressure_before"],
            "canceled_upload_refunds_quiescent": canceled["agent_lane_refunds_quiescent"],
            "canceled_upload_reached_origin": canceled["origin_receiving_payload_observed"],
            "all_receiving_pools_exceeded_required_size_before_pressure":
                min(baseline_server_pools + baseline_agent_pools) > required_pool_bytes,
            "all_receiving_pools_retained_after_recovery":
                min(recovered_server_pools + recovered_agent_pools) > required_pool_bytes,
            "no_oom": all((row["agent_cgroup"]["memory_events"]["oom"] == 0 and
                           row["agent_cgroup"]["memory_events"]["oom_kill"] == 0 and
                           row["server_cgroup"]["memory_events"]["oom"] == 0 and
                           row["server_cgroup"]["memory_events"]["oom_kill"] == 0)
                          for row in rows if "agent_cgroup" in row and "server_cgroup" in row),
        },
        "pressure_ballast_mib": result["pressure_ballast_mib"],
        "baseline_control_roundtrip": result["baseline_control_roundtrip"],
        "pressure_control_roundtrip": result["pressure_control_roundtrip"],
        "recovery_control_roundtrip": result["recovery_control_roundtrip"],
        "control_reservation": {
            "reserved_slots": control["control_reserved_slots"],
            "reserved_base_bytes": control["control_reserved_base_bytes"],
            "reserved_base_fds": control["control_reserved_base_fds"],
            "in_use_before": control["control_in_use_before"],
            "in_use_immediate": control["control_in_use_immediate"],
            "in_use_after": control["control_in_use_after"],
            "ordinary_admission_limit_before": control["ordinary_admission_limit_before"],
            "ordinary_admission_limit_after": control["ordinary_admission_limit_after"],
        },
        "receiving_pools": {
            "required_strictly_greater_than_bytes": required_pool_bytes,
            "pre_pressure": {
                "server_bytes": baseline_server_pools,
                "server_min_bytes": min(baseline_server_pools),
                "server_max_bytes": max(baseline_server_pools),
                "agent_bytes": baseline_agent_pools,
                "agent_min_bytes": min(baseline_agent_pools),
                "agent_max_bytes": max(baseline_agent_pools),
                "server_cgroup_memory_current_bytes": baseline["server_cgroup"]["memory_current_bytes"],
                "agent_cgroup_memory_current_bytes": baseline["agent_cgroup"]["memory_current_bytes"],
            },
            "post_recovery": {
                "server_bytes": recovered_server_pools,
                "server_min_bytes": min(recovered_server_pools),
                "server_max_bytes": max(recovered_server_pools),
                "agent_bytes": recovered_agent_pools,
                "agent_min_bytes": min(recovered_agent_pools),
                "agent_max_bytes": max(recovered_agent_pools),
                "server_cgroup_memory_current_bytes": recovered["server_cgroup"]["memory_current_bytes"],
                "agent_cgroup_memory_current_bytes": recovered["agent_cgroup"]["memory_current_bytes"],
            },
        },
    }
    with open(destination, "w", encoding="utf-8") as output_file:
        json.dump(summary, output_file, indent=2, sort_keys=True)
        output_file.write("\n")
    if not all(summary["acceptance"].values()):
        raise SystemExit(f"pressure qualification failed acceptance: {summary['acceptance']}")
    raise SystemExit(0)
if tiny_response_mode:
    tiny = [row for row in rows if row.get("measurement_stage") == "tiny-response"]
    measured_rtts = sorted({row["measured_rtt_ms"] for row in tiny})
    configured_rtts = sorted({row["configured_rtt_ms"] for row in tiny})
    if len(measured_rtts) != 1 or len(configured_rtts) != 1:
        raise SystemExit(f"tiny-response RTT metadata changed within cohort: configured={configured_rtts}, measured={measured_rtts}")
    summary = {"acceptance": {}, "workloads": {},
               "network": {"configured_rtt_ms": configured_rtts[0], "measured_rtt_ms": measured_rtts[0],
                           "shaped_rate_bits_per_second": 1000000000},
               "gates": {"minimum_throughput_ratio": .90, "maximum_latency_ratio": 1.10}}
    failed = []
    for protocol in ("h1", "h2"):
        for concurrency in (16, 128):
            key = f"{protocol}-c{concurrency}"
            summary["workloads"][key] = {}
            by_mode = {}
            for mode in ("legacy", "aggregate-v1"):
                selected = sorted((row for row in tiny if row["transport_mode"] == mode and
                                   row["measured_protocol"] == protocol and row["concurrency"] == concurrency),
                                  key=lambda row: row["repetition"])
                if len(selected) != 3:
                    raise SystemExit(f"{key} {mode} sample count = {len(selected)}, want 3")
                by_mode[mode] = selected
                summary["workloads"][key][mode] = {
                    "samples": len(selected),
                    "useful_requests_per_second": [row["useful_requests_per_second"] for row in selected],
                    "median_useful_requests_per_second": statistics.median(row["useful_requests_per_second"] for row in selected),
                    "p95_ms": [row["p95_ms"] for row in selected],
                    "median_p95_ms": statistics.median(row["p95_ms"] for row in selected),
                    "p99_ms": [row["p99_ms"] for row in selected],
                    "median_p99_ms": statistics.median(row["p99_ms"] for row in selected),
                }
            candidate = summary["workloads"][key]["aggregate-v1"]
            baseline = summary["workloads"][key]["legacy"]
            throughput_ratio = candidate["median_useful_requests_per_second"] / baseline["median_useful_requests_per_second"]
            p95_ratio = candidate["median_p95_ms"] / baseline["median_p95_ms"]
            p99_ratio = candidate["median_p99_ms"] / baseline["median_p99_ms"]
            pairs = []
            for candidate_row, baseline_row in zip(by_mode["aggregate-v1"], by_mode["legacy"]):
                if candidate_row["repetition"] != baseline_row["repetition"]:
                    raise SystemExit(f"{key} repetition mismatch")
                pairs.append({
                    "repetition": candidate_row["repetition"],
                    "throughput_ratio": candidate_row["useful_requests_per_second"] / baseline_row["useful_requests_per_second"],
                    "p95_ratio": candidate_row["p95_ms"] / baseline_row["p95_ms"],
                    "p99_ratio": candidate_row["p99_ms"] / baseline_row["p99_ms"],
                })
            summary["workloads"][key]["candidate_to_legacy"] = {
                "median_throughput_ratio": throughput_ratio,
                "median_p95_ratio": p95_ratio,
                "median_p99_ratio": p99_ratio,
                "paired_ratios": pairs,
                "paired_throughput_ratio_range": [min(pair["throughput_ratio"] for pair in pairs), max(pair["throughput_ratio"] for pair in pairs)],
                "paired_throughput_ratio_population_stddev": statistics.pstdev(pair["throughput_ratio"] for pair in pairs),
            }
            throughput_pass = throughput_ratio >= .90
            p95_pass = p95_ratio <= 1.10
            p99_pass = p99_ratio <= 1.10
            summary["acceptance"][f"{key}_throughput"] = throughput_pass
            summary["acceptance"][f"{key}_p95"] = p95_pass
            summary["acceptance"][f"{key}_p99"] = p99_pass
            if not (throughput_pass and p95_pass and p99_pass):
                failed.append(key)
    with open(destination, "w", encoding="utf-8") as output_file:
        json.dump(summary, output_file, indent=2, sort_keys=True)
        output_file.write("\n")
    if failed:
        raise SystemExit(f"tiny-response qualification failed: {sorted(set(failed))}")
    raise SystemExit(0)
steady = [row for row in rows if row["measurement_stage"] == "steady"]
summary = {"acceptance": {}, "cohorts": {}}
for mode in ("legacy", "aggregate-v1"):
    summary["cohorts"][mode] = {}
    for direction in ("download", "upload"):
        values = [row["useful_transfer_bytes_per_second"] for row in steady if row["transport_mode"] == mode and row["transfer_direction"] == direction]
        if not values:
            raise SystemExit(f"missing steady {mode} {direction} rows")
        summary["cohorts"][mode][direction] = {
            "samples": len(values),
            "median_useful_bytes_per_second": statistics.median(values),
            "min_useful_bytes_per_second": min(values),
            "max_useful_bytes_per_second": max(values),
        }
for direction in ("download", "upload"):
    candidate = summary["cohorts"]["aggregate-v1"][direction]["median_useful_bytes_per_second"]
    baseline = summary["cohorts"]["legacy"][direction]["median_useful_bytes_per_second"]
    ratio = candidate / baseline
    summary["acceptance"][f"{direction}_candidate_to_legacy_ratio"] = ratio
    pools = [row["receiving_pool_observed_bytes"] for row in steady if row["transport_mode"] == "aggregate-v1" and row["transfer_direction"] == direction and row["receiving_pool_observed_bytes"] is not None]
    summary["cohorts"]["aggregate-v1"][direction]["min_receiving_pool_observed_bytes"] = min(pools) if pools else None
    summary["cohorts"]["aggregate-v1"][direction]["max_receiving_pool_observed_bytes"] = max(pools) if pools else None
    summary["cohorts"]["aggregate-v1"][direction]["receiving_pool_growth_proven"] = bool(pools) and min(pools) > (required_pool_mib << 20)
    if not smoke_mode and ratio < .90:
        raise SystemExit(f"{direction} candidate/legacy median throughput ratio {ratio:.4f} is below 0.90")
summary["acceptance"]["required_receiving_pool_bytes"] = required_pool_mib << 20
with open(destination, "w", encoding="utf-8") as output_file:
    json.dump(summary, output_file, indent=2, sort_keys=True)
    output_file.write("\n")

#!/usr/bin/env python3
import hashlib
import importlib.util
import json
import math
import pathlib
import sys

if len(sys.argv) != 11:
    raise SystemExit("usage: validate-wan-copy-attribution-arm.py RESULTS LIFECYCLE SUMMARY OUTER SUPERVISOR PRE_HASHES POST_HASHES ATTESTATION PROTOCOL SMOKE")

integration_directory = pathlib.Path(__file__).resolve().parent
copy_validator_path = integration_directory.parent.parent / "wan-read-wait-capacity-source" / "validate-copy-record.py"
spec = importlib.util.spec_from_file_location("copy_record_validator", copy_validator_path)
copy_record_validator = importlib.util.module_from_spec(spec)
spec.loader.exec_module(copy_record_validator)

(results_path, lifecycle_path, summary_path, outer_path, supervisor_path,
 pre_path, post_path, attestation_path) = map(pathlib.Path, sys.argv[1:9])
protocol, smoke_text = sys.argv[9:]
if protocol not in ("h1", "h2") or smoke_text not in ("0", "1"):
    raise SystemExit("invalid protocol or smoke argument")
smoke = smoke_text == "1"
rows = [json.loads(line) for line in results_path.read_text().splitlines() if line.strip()]
lifecycle = [json.loads(line) for line in lifecycle_path.read_text().splitlines() if line.strip()]
summary = json.loads(summary_path.read_text())
outer = json.loads(outer_path.read_text())
supervisor = json.loads(supervisor_path.read_text())
pre = json.loads(pre_path.read_text())
post = json.loads(post_path.read_text())
issues = []

def sha(path: pathlib.Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()

def safe_sha(path: pathlib.Path):
    try:
        return sha(path)
    except OSError:
        return None

def safe_size(path: pathlib.Path):
    try:
        return path.stat().st_size
    except OSError:
        return None

def valid_compact_child_identities(parser_process, canonicalizer_process):
    parser_pid = parser_process.get("pid")
    parser_pgid = parser_process.get("pgid")
    canonicalizer_pid = canonicalizer_process.get("pid")
    canonicalizer_pgid = canonicalizer_process.get("pgid")
    return bool(
        type(parser_pid) is int and parser_pid > 0
        and type(parser_pgid) is int and parser_pgid == parser_pid
        and type(canonicalizer_pid) is int and canonicalizer_pid > 0
        and type(canonicalizer_pgid) is int and canonicalizer_pgid == canonicalizer_pid
        and parser_pid != canonicalizer_pid
    )

expected = [("warmup", "download", n) for n in range(1, 5)]
expected += [("warmup", "upload", n) for n in range(1, 5)]
expected += [("retained-warmup", "download", 1), ("retained-warmup", "upload", 1)]
if smoke:
    expected += [("steady", "download", 1), ("steady", "upload", 1), ("reuse-probe", "download", 1)]
else:
    expected += [
        ("steady", "download", 1), ("steady", "upload", 1),
        ("steady", "upload", 2), ("steady", "download", 2),
        ("steady", "download", 3), ("steady", "upload", 3),
    ]
actual = [(row.get("measurement_stage"), row.get("transfer_direction"), row.get("repetition")) for row in rows]
if actual != expected:
    issues.append(f"result order/shape changed: {actual!r}")
if pre != post:
    issues.append("binary, source overlay, certificate, or harness hashes changed during the arm")
hashes = pre.get("sha256", {})
required_hashes = {
    "supervisor", "runner", "validator", "approved_lifecycle_runner", "frozen_protocol_runner", "frozen_copy_runner",
    "wan_protocol_runner_patch", "copy_runner_patch", "monotonic_runner_patch", "read_wait_runner_patch", "server_capacity_snapshot_patch", "cleanup_status_patch", "go_base_patch", "go_helper",
    "copy_record_validator", "copy_attribution_overlay_patch", "copy_attribution_design",
    "monotonic_observer_patch", "monotonic_validator_patch", "monotonic_arm_validator_patch",
    "final_source_attestation", "integration_notes", "copy_timing_decision_map",
    "runner_reconstruction_attestation", "monotonic_build_manifest", "monotonic_audited_build_manifest",
    "read_wait_observer_patch", "read_wait_observer_checkpoint", "read_wait_focused_checkpoint",
    "read_wait_audited_build_manifest", "capacity_source_checkpoint", "capacity_source_checkpoint_manifest",
    "capacity_focused_checkpoint", "capacity_audited_build_manifest", "server_scope_entrypoint", "server_scope_sentinel",
    "server_scope_validator", "server_scope_resource_validator", "delegated_scope_probe_attestation", "trace_analyzer", "trace_source_checkpoint_manifest",
    "trace_analyzer_schema", "trace_compact_checkpoint", "trace_parser_adapter", "trace_canonicalizer", "trace_format",
    "trace_focused_manifest", "trace_focused_summary", "trace_lifecycle_manifest", "trace_lifecycle_summary", "pinned_go",
    "runner_integration_design", "static_contract", "resource_contract", "traced_transfer_contract",
    "arm_validator_contract", "cleanup_status_contract", "cleanup_status_body", "cleanup_status_holder", "all_contracts",
    "server_binary", "aggregate_agent_binary", "certificate_pem", "private_key_pem",
}
if set(hashes) != required_hashes or any(not isinstance(hashes.get(name), str) or len(hashes[name]) != 64 for name in required_hashes):
    issues.append("preflight hash manifest is incomplete")
expected_major = 1 if protocol == "h1" else 2
expected_alpn = "http/1.1" if protocol == "h1" else "h2"
payload_bytes = (4 if smoke else 256) << 20
required_pool_bytes = (2 if smoke else 8) << 20
floor = 29_700_000
phase_download_rates = {stage: [] for stage in ("warmup", "retained-warmup", "steady")}
trace_records = []
copy_records = []
copy_issues_by_sequence = {}

for index, row in enumerate(rows, 1):
    direction = row.get("transfer_direction")
    expected_response = payload_bytes if direction == "download" else 32
    wall = row.get("wall_seconds")
    rate = row.get("useful_transfer_bytes_per_second")
    finite_rate = isinstance(wall, (int, float)) and math.isfinite(wall) and wall > 0
    finite_rate = finite_rate and isinstance(rate, (int, float)) and math.isfinite(rate) and rate > 0
    rate_matches = finite_rate and math.isclose(rate, payload_bytes / wall, rel_tol=1e-9, abs_tol=1e-6)
    exact = (
        row.get("diagnostic_sequence") == index
        and row.get("transport_mode") == "aggregate-v1"
        and row.get("public_protocol") == protocol
        and row.get("protocol") == protocol
        and row.get("observed_http_major") == expected_major
        and row.get("attempted") == row.get("useful_completed") == 1
        and row.get("errors") == 0
        and row.get("status_classes") == {"2xx": 1}
        and row.get("status_codes") == {"200": 1}
        and row.get("configured_payload_bytes") == payload_bytes
        and row.get("response_bytes") == expected_response
        and rate_matches
        and row.get("event_stats_captured") is True
        and row.get("persisted_event_delta") == 1
        and (row.get("dropped_event_delta") or 0) == 0
        and (row.get("event_queue_after_drain") or 0) == 0
        and (row.get("event_drain_error") or "") == ""
        and row.get("configured_rtt_ms") == 80
        and row.get("shaped_rate_bits_per_second") == 1_000_000_000
    )
    if not exact:
        issues.append(f"row {index} failed exact schedule/payload/status/rate/event conservation")
    if direction == "download" and isinstance(rate, (int, float)) and math.isfinite(rate):
        phase_download_rates.setdefault(row.get("measurement_stage"), []).append(rate)
    tls = row.get("wan_response_tls", [])
    if not (len(tls) == 1 and tls[0].get("http_major") == expected_major
            and tls[0].get("negotiated_alpn") == expected_alpn
            and tls[0].get("peer_leaf_der_sha256") == pre.get("expected_leaf_der_sha256")
            and tls[0].get("origin_connection_id") and not tls[0].get("validation_error")):
        issues.append(f"row {index} failed verified public TLS/protocol/origin identity")
    copy_envelope = row.get("response_copy_attribution", {})
    copy_issues = copy_record_validator.validate_copy_envelope(copy_envelope, index, direction, payload_bytes)
    if copy_issues:
        copy_issues_by_sequence[str(index)] = copy_issues
        issues.append(f"row {index} copy attribution failed: {'; '.join(copy_issues)}")
    copy_matches = copy_envelope.get("matches", []) if isinstance(copy_envelope, dict) else []
    copy_record = copy_matches[0] if len(copy_matches) == 1 and isinstance(copy_matches[0], dict) else {}
    copy_metrics = copy_record.get("copy", {}) if isinstance(copy_record.get("copy"), dict) else {}
    started_ns, returned_ns = copy_record.get("started_unix_ns"), copy_record.get("handler_return_unix_ns")
    copy_records.append({
        "sequence": index, "direction": direction, "scope": copy_record.get("scope"),
        "method": copy_record.get("method"), "path": copy_record.get("path"),
        "complete": copy_metrics.get("complete"),
        "handler_elapsed_ns": copy_record.get("handler_elapsed_ns"),
        "upstream_read": copy_metrics.get("upstream_read"),
        "downstream_write": copy_metrics.get("downstream_write"),
        "inter_read_gap": copy_metrics.get("inter_read_gap"),
        "read_eof": copy_metrics.get("read_eof"),
    })
    envelope = row.get("upstream_transport_trace", {})
    matches = envelope.get("matches", [])
    method = "GET" if direction == "download" else "POST"
    path = "/agent/bulk" if direction == "download" else "/agent/bulk-upload"
    if len(matches) != 1:
        issues.append(f"row {index} missing or repeated upstream trace")
        trace_records.append({})
    else:
        trace = matches[0]
        if not (
            envelope.get("overflow") is False
            and envelope.get("store_count") == index
            and envelope.get("sequence") == str(index)
            and trace.get("sequence") == str(index)
            and trace.get("method") == method
            and trace.get("path") == path
            and trace.get("callback_overflow") is False
            and len(trace.get("got_conn", [])) == 1
            and len(trace.get("put_idle_conn", [])) <= 1
        ):
            issues.append(f"row {index} upstream trace correlation/cardinality changed")
        got = trace.get("got_conn", [{}])[0] if len(trace.get("got_conn", [])) == 1 else {}
        parts = (got.get("connection_type"), got.get("local_address"), got.get("remote_address"))
        trace_records.append({
            "connection_key": "|".join(parts) if all(isinstance(part, str) and part for part in parts) else "",
            "reused": got.get("reused"), "was_idle": got.get("was_idle"),
            "idle_time_ns": got.get("idle_time_ns"), "got_conn_unix_ns": got.get("observed_unix_ns"),
            "put_idle_conn": trace.get("put_idle_conn", []),
            "origin_connection_id": tls[0].get("origin_connection_id") if len(tls) == 1 else None,
        })
    provenance = row.get("provenance", {})
    if not (
        provenance.get("server_binary_sha256") == hashes.get("server_binary")
        and provenance.get("aggregate_agent_binary_sha256") == hashes.get("aggregate_agent_binary")
        and provenance.get("harness_sha256") == hashes.get("runner")
        and provenance.get("server_source_ref") == pre.get("server_source_ref")
        and provenance.get("aggregate_agent_source_ref") == pre.get("aggregate_agent_source_ref")
    ):
        issues.append(f"row {index} binary, source, or harness provenance changed")
    if row.get("measurement_stage") == "steady" and not row.get("receiving_pool_observed_bytes", 0) > required_pool_bytes:
        issues.append(f"row {index} did not retain required aggregate receive-pool evidence")

phase_classification = {}
for stage in ("warmup", "retained-warmup", "steady"):
    rates = phase_download_rates.get(stage, [])
    expected_samples = 4 if stage == "warmup" else (1 if stage == "retained-warmup" else (1 if smoke else 3))
    at_floor = len(rates) == expected_samples and all(rate >= floor for rate in rates)
    phase_classification[stage] = {
        "samples": len(rates),
        "minimum_bytes_per_second": min(rates) if rates else None,
        "all_at_or_above_floor": at_floor,
        "gating": not smoke,
    }
    if not smoke and not at_floor:
        issues.append(f"{stage} download phase failed independent {floor} B/s floor")

reuse_expectations = []
reuse_issues = []
first_visits = trace_records[8:12]
if len(first_visits) != 4:
    reuse_issues.append("missing four first post-close lane-key visits")
first_keys = set()
for offset, trace in enumerate(first_visits):
    sequence = offset + 9
    key = trace.get("connection_key")
    puts = trace.get("put_idle_conn", [])
    successful_put = (len(puts) == 1 and isinstance(puts[0].get("observed_unix_ns"), int)
                      and puts[0]["observed_unix_ns"] > 0 and not puts[0].get("error"))
    satisfied = bool(key and key not in first_keys and trace.get("reused") is False
                     and trace.get("was_idle") is False and trace.get("idle_time_ns") == 0 and successful_put)
    if not satisfied:
        reuse_issues.append(f"sequence {sequence} is not a distinct expected-fresh post-close key with successful PutIdleConn")
    if key:
        first_keys.add(key)
    reuse_expectations.append({"sequence": sequence, "kind": "first-post-close-visit", "lane_position": offset + 1,
                               "connection_key": key, "satisfied": satisfied})
revisits = trace_records[12:]
expected_revisit_count = 1 if smoke else 4
if len(revisits) != expected_revisit_count:
    reuse_issues.append(f"same-key revisit count {len(revisits)} changed")
for offset, trace in enumerate(revisits):
    first = first_visits[offset % 4] if len(first_visits) == 4 else {}
    prior_put = first.get("put_idle_conn", [])
    prior_put_ns = prior_put[0].get("observed_unix_ns") if len(prior_put) == 1 else None
    got_ns = trace.get("got_conn_unix_ns")
    elapsed = got_ns - prior_put_ns if isinstance(got_ns, int) and isinstance(prior_put_ns, int) else None
    satisfied = bool(first.get("connection_key") and trace.get("connection_key") == first.get("connection_key")
                     and trace.get("reused") is True and trace.get("was_idle") is True
                     and isinstance(trace.get("idle_time_ns"), int) and trace["idle_time_ns"] >= 0
                     and isinstance(elapsed, int) and 0 <= elapsed < 30_000_000_000
                     and trace.get("origin_connection_id") == first.get("origin_connection_id"))
    if not satisfied:
        reuse_issues.append(f"sequence {offset + 13} did not reuse its bound post-close lane key")
    reuse_expectations.append({
        "sequence": offset + 13, "kind": "same-key-revisit", "lane_position": offset % 4 + 1,
        "connection_key": trace.get("connection_key"),
        "expected_connection_key": first.get("connection_key"), "prior_put_idle_unix_ns": prior_put_ns,
        "got_conn_unix_ns": got_ns, "elapsed_idle_ns": elapsed, "httptrace_idle_time_ns": trace.get("idle_time_ns"),
        "prior_origin_connection_id": first.get("origin_connection_id"),
        "observed_origin_connection_id": trace.get("origin_connection_id"), "satisfied": satisfied,
    })
issues.extend(reuse_issues)

trace_records_path = pathlib.Path(str(results_path).removesuffix(".jsonl") + ".read-wait-trace-records.jsonl")
trace_directory = pathlib.Path(str(results_path).removesuffix(".jsonl") + ".read-wait-traces")
trace_control_directory = pathlib.Path(str(results_path).removesuffix(".jsonl") + ".read-wait-controls")
expected_trace_specs = ({9: ("retained-warmup", 1), 11: ("steady", 1), 13: ("reuse-probe", 1)} if smoke else
                        {9: ("retained-warmup", 1), 11: ("steady", 1), 14: ("steady", 2), 15: ("steady", 3)})
try:
    read_wait_records = [json.loads(line) for line in trace_records_path.read_text().splitlines() if line.strip()]
except (OSError, json.JSONDecodeError) as error:
    read_wait_records = []
    issues.append(f"read-wait trace records are unreadable: {error}")
if len(read_wait_records) != len(expected_trace_specs):
    issues.append(f"read-wait trace record count {len(read_wait_records)} changed")
trace_artifacts = []
seen_trace_sequences = set()
capacity_fields = {
    "schema_version", "sequence", "observed_unix_ns", "generation", "adaptive",
    "adaptive_admission_role", "adaptive_admission_limit", "adaptive_raw_admission_limit",
    "adaptive_public_admission_limit", "reject_new", "memory_pressure", "memory_used_bytes",
    "memory_limit_bytes", "memory_source", "memory_headroom_to_hard_bytes",
    "file_descriptors_used", "file_descriptors_limit", "resource_pressure_reason",
    "resource_sample_error", "resource_sampled_unix_ns", "resource_next_sample_unix_ns",
    "resource_last_good_unix_ns",
}

def exact_int(value):
    return type(value) is int

def valid_server_capacity(value, sequence):
    return bool(
        isinstance(value, dict) and set(value) == capacity_fields
        and value.get("schema_version") == 1 and value.get("sequence") == str(sequence)
        and value.get("adaptive") is True and value.get("adaptive_admission_role") == "server_stream"
        and value.get("reject_new") is False and value.get("memory_pressure") == "healthy"
        and value.get("resource_sample_error") == ""
        and value.get("memory_source") in {"cgroup_v2", "cgroup_v1", "host", "go"}
        and exact_int(value.get("memory_used_bytes")) and exact_int(value.get("memory_limit_bytes"))
        and 0 <= value["memory_used_bytes"] <= value["memory_limit_bytes"] and value["memory_limit_bytes"] > 0
        and exact_int(value.get("memory_headroom_to_hard_bytes")) and value["memory_headroom_to_hard_bytes"] >= 0
        and exact_int(value.get("file_descriptors_used")) and exact_int(value.get("file_descriptors_limit"))
        and 0 <= value["file_descriptors_used"] <= value["file_descriptors_limit"] and value["file_descriptors_limit"] > 0
        and isinstance(value.get("resource_pressure_reason"), str) and bool(value["resource_pressure_reason"])
        and exact_int(value.get("generation")) and value["generation"] > 0
        and exact_int(value.get("observed_unix_ns")) and exact_int(value.get("resource_sampled_unix_ns"))
        and exact_int(value.get("resource_next_sample_unix_ns")) and exact_int(value.get("resource_last_good_unix_ns"))
        and value["resource_sampled_unix_ns"] > 0 and value["resource_last_good_unix_ns"] > 0
        and value["observed_unix_ns"] >= value["resource_sampled_unix_ns"]
        and value["observed_unix_ns"] >= value["resource_last_good_unix_ns"]
        and value["resource_next_sample_unix_ns"] >= value["resource_sampled_unix_ns"]
        and exact_int(value.get("adaptive_raw_admission_limit")) and exact_int(value.get("adaptive_admission_limit"))
        and exact_int(value.get("adaptive_public_admission_limit"))
        and 0 <= value["adaptive_public_admission_limit"] <= value["adaptive_admission_limit"] <= value["adaptive_raw_admission_limit"]
    )

def valid_capacity_request(value, snapshot, sequence):
    return bool(
        isinstance(value, dict) and set(value) == {
            "schema_version", "sequence", "request_started_unix_ns", "request_elapsed_ns",
            "http_status", "response", "error",
        }
        and value.get("schema_version") == 1 and value.get("sequence") == str(sequence)
        and exact_int(value.get("request_started_unix_ns")) and value["request_started_unix_ns"] > 0
        and exact_int(value.get("request_elapsed_ns")) and value["request_elapsed_ns"] >= 0
        and value.get("http_status") == 200 and value.get("error") == "" and value.get("response") == snapshot
    )

for record in read_wait_records:
    sequence = record.get("sequence") if isinstance(record, dict) else None
    if sequence in seen_trace_sequences:
        issues.append(f"read-wait sequence {sequence!r} was repeated")
    seen_trace_sequences.add(sequence)
    expected_spec = expected_trace_specs.get(sequence)
    start = record.get("start_control") if isinstance(record, dict) else None
    stop = record.get("stop_control") if isinstance(record, dict) else None
    start_capture = start.get("response", {}).get("capture", {}) if isinstance(start, dict) else {}
    stop_capture = stop.get("response", {}).get("capture", {}) if isinstance(stop, dict) else {}
    pre_capacity = record.get("pre_server_capacity") if isinstance(record, dict) else None
    post_capacity = record.get("post_server_capacity") if isinstance(record, dict) else None
    pre_capacity_request = record.get("pre_capacity_request") if isinstance(record, dict) else None
    post_capacity_request = record.get("post_capacity_request") if isinstance(record, dict) else None
    pre_capacity_path = trace_control_directory / f"sequence-{sequence}-pre-capacity.json"
    post_capacity_path = trace_control_directory / f"sequence-{sequence}-post-capacity.json"
    try:
        pre_capacity_file = json.loads(pre_capacity_path.read_text())
        post_capacity_file = json.loads(post_capacity_path.read_text())
    except (OSError, json.JSONDecodeError) as error:
        pre_capacity_file, post_capacity_file = None, None
        issues.append(f"read-wait sequence {sequence!r} capacity request is unreadable: {error}")
    expected_path = trace_directory / f"runtime-trace-sequence-{sequence}.out"
    writer = stop_capture.get("writer", {}) if isinstance(stop_capture.get("writer"), dict) else {}
    try:
        artifact_size = expected_path.stat().st_size
        artifact_sha = sha(expected_path)
    except OSError as error:
        artifact_size, artifact_sha = None, None
        issues.append(f"read-wait sequence {sequence!r} artifact is unreadable: {error}")
    valid = bool(
        expected_spec is not None
        and record.get("stage") == expected_spec[0] and record.get("direction") == "download"
        and record.get("repetition") == expected_spec[1]
        and record.get("control_boundary") == "start-before-run_transfer-stop-after-run_transfer-including-postqueries"
        and record.get("handler_region_boundary") == "unchanged-public-proxy-handler-only"
        and record.get("trace_artifact_scope") == "whole-server-process"
        and record.get("qualification_result") is False
        and record.get("pre_resource_status") == record.get("pre_healthy_status") == 0
        and record.get("pre_capacity_query_status") == record.get("post_capacity_query_status") == 0
        and record.get("post_healthy_status") == 0
        and record.get("transfer_status") == record.get("stop_status") == 0
        and record.get("post_runtime_status") == record.get("post_cgroup_status") == record.get("resource_status") == 0
        and record.get("complete") is True and record.get("valid") is True
        and isinstance(start, dict) and start.get("action") == "start" and start.get("sequence") == str(sequence)
        and start.get("http_status") == 200 and start.get("error") == ""
        and start_capture.get("sequence") == str(sequence) and start_capture.get("state") == "active"
        and start_capture.get("complete") is False and start_capture.get("valid") is False
        and isinstance(stop, dict) and stop.get("action") == "stop" and stop.get("sequence") == str(sequence)
        and stop.get("http_status") == 200 and stop.get("error") == ""
        and stop_capture.get("sequence") == str(sequence) and stop_capture.get("state") == "complete"
        and stop_capture.get("task_type") == "wan-read-wait-request"
        and stop_capture.get("region_type") == "public-proxy-handler"
        and stop_capture.get("expected_method") == "GET" and stop_capture.get("expected_path") == "/agent/bulk"
        and stop_capture.get("path") == str(expected_path)
        and stop_capture.get("candidate_requests") == stop_capture.get("marked_requests") == 1
        and stop_capture.get("active_requests") == stop_capture.get("requests_outside_capture") == 0
        and stop_capture.get("controller_timed_out") is False and "issues" in stop_capture
        and (stop_capture["issues"] is None or stop_capture["issues"] == [])
        and stop_capture.get("issue_overflow") is False and stop_capture.get("complete") is True and stop_capture.get("valid") is True
        and isinstance(stop_capture.get("started_unix_ns"), int) and isinstance(stop_capture.get("stopped_unix_ns"), int)
        and stop_capture["started_unix_ns"] <= stop_capture["stopped_unix_ns"]
        and isinstance(stop_capture.get("trace_elapsed_ns"), int) and stop_capture["trace_elapsed_ns"] >= 0
        and writer.get("cap_bytes") == 64 << 20 and isinstance(writer.get("bytes"), int)
        and writer.get("input_bytes") == writer.get("bytes") == artifact_size and artifact_size is not None and artifact_size > 0
        and writer.get("overflow") is False and (writer.get("error") or "") == ""
        and valid_server_capacity(pre_capacity, sequence) and valid_server_capacity(post_capacity, sequence)
        and valid_capacity_request(pre_capacity_request, pre_capacity, sequence)
        and valid_capacity_request(post_capacity_request, post_capacity, sequence)
        and pre_capacity_file == pre_capacity_request and post_capacity_file == post_capacity_request
    )
    if not valid:
        issues.append(f"read-wait sequence {sequence!r} control/artifact contract failed")
    trace_artifacts.append({"sequence": sequence, "path": str(expected_path), "size_bytes": artifact_size,
                            "sha256": artifact_sha, "valid": valid,
                            "pre_server_capacity": pre_capacity, "post_server_capacity": post_capacity,
                            "pre_capacity_request_elapsed_ns": pre_capacity_request.get("request_elapsed_ns") if isinstance(pre_capacity_request, dict) else None,
                            "post_capacity_request_elapsed_ns": post_capacity_request.get("request_elapsed_ns") if isinstance(post_capacity_request, dict) else None})
if seen_trace_sequences != set(expected_trace_specs):
    issues.append(f"read-wait trace sequence set {sorted(v for v in seen_trace_sequences if isinstance(v, int))!r} changed")
expected_control_names = {f"sequence-{sequence}-{action}.json" for sequence in expected_trace_specs
                          for action in ("pre-capacity", "start", "stop", "post-capacity")}
try:
    actual_control_names = {path.name for path in trace_control_directory.iterdir() if path.is_file()}
except OSError as error:
    actual_control_names = set()
    issues.append(f"read-wait control directory is unreadable: {error}")
if actual_control_names != expected_control_names:
    issues.append(f"read-wait control file set changed: {sorted(actual_control_names)!r}")
try:
    actual_trace_names = {path.name for path in trace_directory.iterdir() if path.is_file()}
except OSError as error:
    actual_trace_names = set()
    issues.append(f"read-wait trace directory is unreadable: {error}")
expected_trace_names = {f"runtime-trace-sequence-{sequence}.out" for sequence in expected_trace_specs}
if actual_trace_names != expected_trace_names:
    issues.append(f"read-wait trace artifact set changed: {sorted(actual_trace_names)!r}")

reduction_directory = results_path.parent / "read-wait-reductions"
reduction_index_path = reduction_directory / "index.json"
expected_reduction_names = {"index.json", "adapter-all.status", "parser-all.status",
                            "canonicalizer-all.status", "analyzer-all.status"}
for sequence in expected_trace_specs:
    expected_reduction_names.update({
        f"sequence-{sequence}.intern", f"sequence-{sequence}.ledger.json",
        f"sequence-{sequence}.parser.status", f"sequence-{sequence}.canonicalizer.status",
        f"sequence-{sequence}.adapter.status", f"sequence-{sequence}.analyzer.status",
        f"sequence-{sequence}.process.json", f"sequence-{sequence}.reduction.json",
        f"sequence-{sequence}.adapter.invocation.txt", f"sequence-{sequence}.adapter.stdout.log",
        f"sequence-{sequence}.adapter.stderr.log", f"sequence-{sequence}.parser.stderr.log",
        f"sequence-{sequence}.canonicalizer.stderr.log", f"sequence-{sequence}.analyzer.invocation.txt",
        f"sequence-{sequence}.analyzer.stdout.log", f"sequence-{sequence}.analyzer.stderr.log",
    })
try:
    actual_reduction_names = {path.name for path in reduction_directory.iterdir() if path.is_file()}
except OSError as error:
    actual_reduction_names = set()
    issues.append(f"trace reduction directory is unreadable: {error}")
if actual_reduction_names != expected_reduction_names:
    issues.append(f"trace reduction artifact set changed: {sorted(actual_reduction_names)!r}")
try:
    all_statuses_exact = all((reduction_directory / name).read_bytes() == b"0\n" for name in (
        "adapter-all.status", "parser-all.status", "canonicalizer-all.status", "analyzer-all.status"))
except OSError:
    all_statuses_exact = False
if not all_statuses_exact:
    issues.append("one or more aggregate adapter/parser/reducer statuses are nonzero or unreadable")
try:
    reduction_index = json.loads(reduction_index_path.read_text())
except (OSError, json.JSONDecodeError) as error:
    reduction_index = {}
    issues.append(f"trace reduction index is unreadable: {error}")
reduction_entries = reduction_index.get("entries", []) if isinstance(reduction_index, dict) else []
if not (reduction_index.get("schema_version") == 2 and reduction_index.get("diagnostic_only") is True
        and reduction_index.get("input_representation") == "p2pstream-parsed-stack-intern-v1"
        and reduction_index.get("duration_relation_to_copy_observer") == "descriptive-only"
        and [entry.get("sequence") for entry in reduction_entries if isinstance(entry, dict)] == list(expected_trace_specs)):
    issues.append("trace reduction index scope, representation, or sequence order changed")
trace_reductions = []
zero_status_sha = hashlib.sha256(b"0\n").hexdigest()
expected_bounds = {
    "max_canonical_input_bytes": 512 << 20,
    "max_expanded_input_bytes": 4_294_967_296,
    "max_line_bytes": 1 << 20,
    "max_events": 5_000_000,
    "max_event_continuations": 4096,
    "max_event_bytes": 4 << 20,
    "max_wait_stack_groups": 4096,
    "max_stack_definitions": 4096,
    "max_definition_bytes": 64 << 20,
    "max_output_bytes": 8 << 20,
}
expected_environment = {
    "GOENV": "off", "GOTOOLCHAIN": "local", "HOME": "/home/user",
    "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8", "PATH": "/usr/bin:/bin",
    "PYTHONDONTWRITEBYTECODE": "1", "PYTHONHASHSEED": "0",
    "PYTHONNOUSERSITE": "1", "TZ": "Europe/Zurich",
}
compact_root = integration_directory.parent
expected_go = pathlib.Path("/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin/go")
expected_python = pathlib.Path("/usr/bin/python3.14")
expected_canonicalizer = compact_root / "intern_parsed_trace.py"
expected_format = compact_root / "stack_intern_format.py"
expected_pinned_inputs = {
    "go": {"path": str(expected_go), "sha256": "29e6e0b8be61beb1489ceae62b304343566de8a1dc700af74bde7aeb9c80ad45"},
    "python": {"path": str(expected_python), "sha256": "52e0a13e60a981d8c4b6478be2ba5176f69da07948a056bf49cf6f077e30cb41"},
    "canonicalizer": {"path": str(expected_canonicalizer), "sha256": "4d1bbcdd2140d148741775842e1ba9d2d74f2541b15933a6232b18cf3473e793"},
    "format_module": {"path": str(expected_format), "sha256": "f370243b753a94a122157e9b0b13eb535065fe88746269bd70de5475d82e4dbf"},
}
for entry in reduction_entries:
    sequence = entry.get("sequence") if isinstance(entry, dict) else None
    if sequence not in expected_trace_specs or not (1 <= sequence <= len(rows)):
        issues.append(f"trace reduction has invalid sequence {sequence!r}")
        continue
    row = rows[sequence - 1]
    raw_path = pathlib.Path(entry.get("raw_path", ""))
    canonical_path = pathlib.Path(entry.get("canonical_path", ""))
    ledger_path = pathlib.Path(entry.get("ledger_path", ""))
    parser_status_path = pathlib.Path(entry.get("parser_status_path", ""))
    canonicalizer_status_path = pathlib.Path(entry.get("canonicalizer_status_path", ""))
    adapter_status_path = pathlib.Path(entry.get("adapter_status_path", ""))
    analyzer_status_path = pathlib.Path(entry.get("analyzer_status_path", ""))
    process_record_path = pathlib.Path(entry.get("process_record_path", ""))
    reduction_path = pathlib.Path(entry.get("reduction_path", ""))
    expected_raw = trace_directory / f"runtime-trace-sequence-{sequence}.out"
    expected_canonical = reduction_directory / f"sequence-{sequence}.intern"
    expected_ledger = reduction_directory / f"sequence-{sequence}.ledger.json"
    expected_parser_status = reduction_directory / f"sequence-{sequence}.parser.status"
    expected_canonicalizer_status = reduction_directory / f"sequence-{sequence}.canonicalizer.status"
    expected_adapter_status = reduction_directory / f"sequence-{sequence}.adapter.status"
    expected_analyzer_status = reduction_directory / f"sequence-{sequence}.analyzer.status"
    expected_process_record = reduction_directory / f"sequence-{sequence}.process.json"
    expected_reduction = reduction_directory / f"sequence-{sequence}.reduction.json"
    try:
        reduction = json.loads(reduction_path.read_text())
        ledger = json.loads(ledger_path.read_text())
        process_record = json.loads(process_record_path.read_text())
        statuses_exact = all(path.read_bytes() == b"0\n" for path in (
            parser_status_path, canonicalizer_status_path, adapter_status_path, analyzer_status_path))
    except (OSError, json.JSONDecodeError) as error:
        reduction = {}
        ledger = {}
        process_record = {}
        statuses_exact = False
        issues.append(f"trace reduction sequence {sequence} is unreadable: {error}")
    tls = row.get("wan_response_tls", [])
    origin_identity = tls[0].get("origin_connection_id") if len(tls) == 1 and isinstance(tls[0], dict) else None
    state_ns = reduction.get("reduction", {}).get("state_ns", {})
    region_duration = reduction.get("reduction", {}).get("region_duration_ns")
    joins = {
        "raw": (raw_path, expected_raw, "raw_sha256"),
        "canonical": (canonical_path, expected_canonical, "canonical_sha256"),
        "ledger": (ledger_path, expected_ledger, "ledger_sha256"),
        "parser_status": (parser_status_path, expected_parser_status, "parser_status_sha256"),
        "canonicalizer_status": (canonicalizer_status_path, expected_canonicalizer_status, "canonicalizer_status_sha256"),
        "adapter_status": (adapter_status_path, expected_adapter_status, "adapter_status_sha256"),
        "analyzer_status": (analyzer_status_path, expected_analyzer_status, "analyzer_status_sha256"),
        "process_record": (process_record_path, expected_process_record, "process_record_sha256"),
        "reduction": (reduction_path, expected_reduction, "reduction_sha256"),
    }
    join_valid = bool(
        entry.get("server_binary_sha256") == hashes.get("server_binary")
        and entry.get("server_source_ref") == pre.get("server_source_ref")
        and entry.get("runner_sha256") == hashes.get("runner")
        and entry.get("public_protocol") == protocol
        and entry.get("certificate_der_sha256") == pre.get("expected_leaf_der_sha256")
        and isinstance(origin_identity, str) and origin_identity
        and entry.get("origin_connection_id") == origin_identity
        and all(path == expected and entry.get(hash_key) == safe_sha(path)
                for path, expected, hash_key in joins.values())
    )
    parser_process = process_record.get("parser", {})
    canonicalizer_process = process_record.get("canonicalizer", {})
    expected_process = {
        "status": 0, "joined": True, "group_gone": True,
        "term_attempted": False, "kill_attempted": False,
        "stderr_overflow": False,
    }
    adapter_valid = bool(
        process_record.get("schema_version") == 1 and process_record.get("accepted") is True
        and process_record.get("startup_error") is None and process_record.get("artifact_error") is None
        and process_record.get("interrupted_signal") is None and process_record.get("timed_out") is False
        and process_record.get("source_stable") is True and process_record.get("working_directory_stable") is True
        and process_record.get("removed_invalid_artifacts") == {}
        and process_record.get("environment") == expected_environment
        and process_record.get("limits") == {"kill_grace_seconds": 5, "max_raw_trace_bytes": 64 << 20,
                                               "max_stderr_bytes": 1 << 20, "normal_deadline_seconds": 600,
                                               "term_grace_seconds": 5}
        and process_record.get("pinned_inputs") == expected_pinned_inputs
        and process_record.get("parser_argv") == [str(expected_go), "tool", "trace", "-d=parsed", str(raw_path)]
        and process_record.get("canonicalizer_argv") == [str(expected_python), str(expected_canonicalizer),
                                                           "--input", "-", "--output", str(canonical_path),
                                                           "--ledger", str(ledger_path)]
        and valid_compact_child_identities(parser_process, canonicalizer_process)
        and all(parser_process.get(key) == value for key, value in expected_process.items())
        and all(canonicalizer_process.get(key) == value for key, value in expected_process.items())
        and process_record.get("raw_trace", {}).get("path") == str(raw_path)
        and process_record.get("raw_trace", {}).get("stable") is True
        and process_record.get("raw_trace", {}).get("pre") == process_record.get("raw_trace", {}).get("post")
        and process_record.get("raw_trace", {}).get("pre", {}).get("sha256") == safe_sha(raw_path)
        and process_record.get("artifact_identities", {}).get("canonical_output", {}).get("sha256") == safe_sha(canonical_path)
        and process_record.get("artifact_identities", {}).get("canonical_output", {}).get("size") == safe_size(canonical_path)
        and process_record.get("artifact_identities", {}).get("ledger", {}).get("sha256") == safe_sha(ledger_path)
        and process_record.get("artifact_identities", {}).get("ledger", {}).get("size") == safe_size(ledger_path)
        and process_record.get("working_directory") == str(reduction_directory)
        and process_record.get("working_directory_identity") == process_record.get("working_directory_post_identity")
    )
    reduction_valid = bool(
        statuses_exact and adapter_valid
        and reduction.get("schema_version") == 2 and reduction.get("accepted") is True and reduction.get("errors") == []
        and reduction.get("task_type") == "wan-read-wait-request" and reduction.get("region_type") == "public-proxy-handler"
        and reduction.get("input_representation") == "p2pstream-parsed-stack-intern-v1"
        and reduction.get("input") == str(canonical_path) and reduction.get("input_sha256") == safe_sha(canonical_path)
        and reduction.get("parsed_tool_status_file") == str(parser_status_path)
        and reduction.get("parsed_tool_status_sha256") == zero_status_sha
        and reduction.get("canonicalizer_status_file") == str(canonicalizer_status_path)
        and reduction.get("canonicalizer_status_sha256") == zero_status_sha
        and reduction.get("canonical_ledger_file") == str(ledger_path)
        and reduction.get("canonical_ledger_sha256") == safe_sha(ledger_path)
        and reduction.get("fixed_bounds") == expected_bounds
        and ledger.get("canonical_sha256") == safe_sha(canonical_path)
        and ledger.get("expanded_input_sha256") == reduction.get("expanded_input_sha256")
        and ledger.get("expanded_input_bytes") == reduction.get("expanded_input_bytes")
        and reduction.get("canonical_dictionary") == {
            "definition_bytes": ledger.get("definition_bytes"),
            "definition_count": ledger.get("definition_count"),
            "prefooter_sha256": ledger.get("canonical_prefooter_sha256"),
            "record_count": ledger.get("record_count"),
        }
        and isinstance(reduction.get("markers", {}).get("task_id"), int) and reduction["markers"]["task_id"] > 0
        and isinstance(reduction.get("markers", {}).get("handler_gid"), int) and reduction["markers"]["handler_gid"] > 0
        and isinstance(region_duration, int) and region_duration > 0
        and set(state_ns) == {"Running", "Runnable", "Syscall", "Waiting"}
        and all(isinstance(state_ns[name], int) and state_ns[name] >= 0 for name in state_ns)
        and sum(state_ns.values()) == region_duration
        and reduction.get("reduction", {}).get("partition_delta_ns") == 0
        and reduction.get("reduction", {}).get("partition_sum_ns") == region_duration
        and reduction.get("reduction", {}).get("missing_stack_ns") == 0
        and isinstance(reduction.get("reduction", {}).get("unknown_stack_ns"), int)
        and reduction["reduction"]["unknown_stack_ns"] >= 0
        and isinstance(reduction.get("reduction", {}).get("waiting_category_ns"), dict)
        and isinstance(reduction.get("reduction", {}).get("waiting_reason_ns"), dict)
        and isinstance(reduction.get("reduction", {}).get("waiting_stack_groups"), list)
        and len(reduction["reduction"]["waiting_stack_groups"]) <= 4096
        and isinstance(reduction.get("reduction", {}).get("top_wait_intervals"), list)
        and len(reduction["reduction"]["top_wait_intervals"]) <= 8
        and isinstance(reduction.get("reduction", {}).get("attribution_complete"), bool)
    )
    if not join_valid:
        issues.append(f"trace reduction sequence {sequence} failed cohort identity join")
    if not reduction_valid:
        issues.append(f"trace reduction sequence {sequence} failed adapter/parser/reducer contract")
    trace_reductions.append({
        "sequence": sequence, "join_valid": join_valid, "adapter_valid": adapter_valid,
        "reduction_valid": reduction_valid, "region_duration_ns": region_duration, "state_ns": state_ns,
        "waiting_category_ns": reduction.get("reduction", {}).get("waiting_category_ns"),
        "waiting_reason_ns": reduction.get("reduction", {}).get("waiting_reason_ns"),
        "waiting_stack_groups": reduction.get("reduction", {}).get("waiting_stack_groups"),
        "top_wait_intervals": reduction.get("reduction", {}).get("top_wait_intervals"),
        "unknown_stack_ns": reduction.get("reduction", {}).get("unknown_stack_ns"),
        "attribution_complete": reduction.get("reduction", {}).get("attribution_complete"),
        "copy_handler_elapsed_ns": copy_records[sequence - 1].get("handler_elapsed_ns"),
        "duration_comparison": "descriptive-only",
    })

acceptance = summary.get("acceptance")
expected_acceptance = {
    "correctness", "verified_tls_and_protocol_every_response", "transport_trace_complete", "expected_upstream_reuse",
    "response_copy_attribution_complete", "download_floor_every_phase"
}
if summary.get("classification") != "observational-complete":
    issues.append(f"diagnostic classification is not conclusive: {summary.get('classification')!r}")
if not isinstance(acceptance, dict) or set(acceptance) != expected_acceptance or any(value is not True for value in acceptance.values()):
    issues.append(f"diagnostic acceptance failed or incomplete: {acceptance!r}")
if summary.get("public_protocol") != protocol or summary.get("diagnostic_only") is not True:
    issues.append("summary protocol/scope changed")
if summary.get("copy_attribution_issues_by_sequence") != copy_issues_by_sequence:
    issues.append("summary copy-attribution issue evidence differs from independently validated rows")
expected_copy_interpretation = ("Diagnostic observer only. Upstream Read and downstream Write durations are compared separately with monotonic handler elapsed time. Unix timestamps are retained only for coarse correlation. "
                                "Inter-read gaps include downstream Write time and bookkeeping and are never added as an independent component. "
                                "POST observations cover only the 32-byte response copy, not upload request-body handling.")
if summary.get("copy_attribution_interpretation") != expected_copy_interpretation:
    issues.append("summary copy-attribution interpretation changed")

expected_labels = {
    f"current-server-aggregate-v1-{protocol}-1-agent",
    f"current-server-aggregate-v1-{protocol}-1-server",
    "origin", "agent-namespace", "router-namespace",
}
process_exits = [row for row in lifecycle if row.get("event") == "process-exit"]
labels = [row.get("label") for row in process_exits]
pids = [row.get("pid") for row in process_exits]
lifecycle_identity_ok = not (len(process_exits) != len(expected_labels) or set(labels) != expected_labels or len(set(labels)) != len(labels)
        or any(not isinstance(pid, int) or pid <= 0 for pid in pids) or len(set(pids)) != len(pids))
if not lifecycle_identity_ok:
    issues.append(f"endpoint/namespace exit count, labels, or process identities changed: {process_exits!r}")
lifecycle_status_ok = not any(not (row.get("forced") is False and row.get("status") == 0 and row.get("group_joined") is True
               and row.get("invalidates_success") is False) for row in process_exits)
if not lifecycle_status_ok:
    issues.append(f"endpoint or namespace lifecycle was not wholly natural: {process_exits!r}")
lifecycle_no_invalidation = not any(row.get("invalidates_success") is True for row in lifecycle)
if not lifecycle_no_invalidation:
    issues.append("lifecycle contains an invalidating event")
namespace_rows = [row for row in lifecycle if row.get("event") == "namespace-helpers-start"]
namespace_ok = len(namespace_rows) == 1
if not namespace_ok:
    issues.append("missing unique namespace-helper identity row")
else:
    helper = namespace_rows[0]
    if helper.get("agent", {}).get("namespace") == helper.get("router", {}).get("namespace"):
        namespace_ok = False
        issues.append("agent and router namespace identities are equal")
    for role in ("agent", "router"):
        if not isinstance(helper.get(role, {}).get("start_ticks"), int) or helper[role]["start_ticks"] <= 0:
            namespace_ok = False
            issues.append(f"invalid {role} namespace helper start ticks")
outer_ok = (outer.get("all_private") is True and outer.get("shell_pid_is_one") is True
            and outer.get("python_parent_is_one") is True
            and outer.get("cgroup_namespace_unchanged") is True
            and outer.get("owned", {}).get("cgroup") == outer.get("parent", {}).get("cgroup"))
if not outer_ok:
    issues.append(f"outer user/PID/mount/network namespace ownership missing: {outer!r}")

scope_directory = results_path.parent / "server-scope"
def read_scope_json(name):
    try:
        return json.loads((scope_directory / name).read_text())
    except (OSError, json.JSONDecodeError) as error:
        issues.append(f"server scope {name} is unreadable: {error}")
        return {}
scope_preexec_binding = read_scope_json("server-preexec-binding.json")
scope_application_binding = read_scope_json("server-application-binding.json")
scope_resource = read_scope_json("server-resource-attestation.json")
scope_process_join = read_scope_json("scope-process-join.json")
scope_pre_traffic = read_scope_json("server-cgroup-pre-traffic.json")
scope_post_workload = read_scope_json("server-cgroup-post-workload.json")
scope_unit_policy = read_scope_json("unit-policy.json")
scope_unit_final = read_scope_json("unit-final.json")
scope_launcher = read_scope_json("scope-launcher.json")
scope_delegation = read_scope_json("cgroup-delegation-paths.json")
pre_host = scope_preexec_binding.get("host_process", {})
app_host = scope_application_binding.get("host_process", {})
scope_event_keys = ("high", "max", "oom", "oom_kill", "oom_group_kill")
expected_scope_path = scope_preexec_binding.get("scope", {}).get("path")
expected_scope_unified_path = scope_preexec_binding.get("state", {}).get("expected_cgroup")
delegation_destination = scope_delegation.get("destination", {})
delegation_procs = scope_delegation.get("destination_cgroup_procs", {})
delegation_common = scope_delegation.get("common_ancestor", {})
delegation_common_procs = scope_delegation.get("common_ancestor_cgroup_procs", {})
scope_ok = bool(
    scope_preexec_binding.get("accepted") is True and scope_application_binding.get("accepted") is True
    and all(value is True for value in scope_preexec_binding.get("checks", {}).values())
    and all(value is True for value in scope_application_binding.get("checks", {}).values())
    and isinstance(pre_host.get("host_pid"), int) and pre_host.get("host_pid") == app_host.get("host_pid")
    and pre_host.get("host_start_ticks") == app_host.get("host_start_ticks")
    and pre_host.get("host_namespaces") == app_host.get("host_namespaces")
    and scope_resource.get("accepted") is True and all(value is True for value in scope_resource.get("checks", {}).values())
    and scope_process_join == {"launcher_alive_same_identity": False, "launcher_group_joined": True,
                               "sentinel_alive_same_identity": False, "sentinel_group_joined": True}
    and scope_launcher.get("setsid_identity_stable") is True
    and scope_launcher.get("pid") == scope_launcher.get("pgid") == scope_launcher.get("sid")
    and isinstance(scope_launcher.get("start_ticks"), int) and scope_launcher["start_ticks"] > 0
    and scope_unit_policy.get("accepted") is True
    and all(value is True for value in scope_unit_policy.get("checks", {}).values())
    and scope_unit_final.get("query_status") == 0 and scope_unit_final.get("joined") is True
    and scope_unit_final.get("failed_state_rejected") is True
    and scope_unit_final.get("confirmation") == "show" and scope_unit_final.get("show_status") == 0
    and scope_unit_final.get("load_state") in ("not-found", "loaded")
    and scope_unit_final.get("active_state") == "inactive" and scope_unit_final.get("sub_state") == "dead"
    and isinstance(scope_delegation.get("source"), dict) and isinstance(scope_delegation.get("common_ancestor"), dict)
    and scope_delegation.get("source_cgroup") == scope_delegation.get("source", {}).get("path")
    and scope_delegation.get("destination_cgroup") == delegation_destination.get("path")
    and scope_delegation.get("nearest_common_ancestor") == delegation_common.get("path")
    and delegation_destination.get("path") == expected_scope_path
    and delegation_procs.get("path") == str(pathlib.Path(expected_scope_path or "") / "cgroup.procs")
    and delegation_procs.get("writable") is True
    and delegation_common_procs.get("path") == str(pathlib.Path(delegation_common.get("path", "")) / "cgroup.procs")
    and delegation_common_procs.get("writable") is True
    and scope_pre_traffic.get("path") == expected_scope_path
    and scope_post_workload.get("cgroup_path") == expected_scope_unified_path
    and scope_pre_traffic.get("memory_max_bytes") == scope_post_workload.get("memory_max_bytes") == 4294967296
    and scope_pre_traffic.get("memory_swap_max_bytes") == scope_post_workload.get("memory_swap_max_bytes") == 0
    and scope_pre_traffic.get("memory_high_bytes") == scope_post_workload.get("memory_high_bytes") == "max"
    and all(scope_pre_traffic.get("memory_events", {}).get(key) == scope_post_workload.get("memory_events", {}).get(key) == 0
            for key in scope_event_keys)
)
if not scope_ok:
    issues.append("delegated server-only cgroup identity, policy, or memory-event contract failed")
supervisor_joined_ok = (supervisor.get("group_joined") is True and supervisor.get("forced") is False
                        and supervisor.get("server_scope", {}).get("systemd_run_status") == 0
                        and supervisor.get("server_scope", {}).get("resource_status") == 0
                        and supervisor.get("server_scope", {}).get("unit_joined") is True
                        and supervisor.get("server_scope", {}).get("unit_query_status") == 0
                        and supervisor.get("server_scope", {}).get("unit_active_state") != "failed"
                        and supervisor.get("trace_reduction", {}).get("adapter_status") == 0
                        and supervisor.get("trace_reduction", {}).get("parser_status") == 0
                        and supervisor.get("trace_reduction", {}).get("canonicalizer_status") == 0
                        and supervisor.get("trace_reduction", {}).get("analyzer_status") == 0)
if not supervisor_joined_ok:
    issues.append(f"outer runner process group did not join naturally: {supervisor!r}")
runner_status_ok = supervisor.get("runner_status") == 0
expected_supervisor_invalidation = not (runner_status_ok and supervisor_joined_ok)
if supervisor.get("invalidates_success") is not expected_supervisor_invalidation:
    issues.append("supervisor invalidates_success does not match recorded runner/resource/parser statuses")
if not runner_status_ok:
    issues.append(f"runner exited nonzero after preserving evidence: {supervisor.get('runner_status')!r}")

attestation = {
    "schema_version": 1,
    "accepted": not issues,
    "diagnostic_only": True,
    "protocol": protocol,
    "smoke": smoke,
    "row_count": len(rows),
    "classification": summary.get("classification"),
    "validation_issues": issues,
    "download_floor_bytes_per_second": floor,
    "download_phase_classification": phase_classification,
    "reuse_expectations": reuse_expectations,
    "copy_attribution_records": copy_records,
    "copy_attribution_issues_by_sequence": copy_issues_by_sequence,
    "copy_attribution_interpretation": expected_copy_interpretation,
    "read_wait_trace_artifacts": trace_artifacts,
    "read_wait_trace_reductions": trace_reductions,
    "read_wait_duration_relation_to_copy_observer": "descriptive-only",
    "all_observed_http_major": expected_major if rows and all(row.get("observed_http_major") == expected_major for row in rows) else None,
    "verified_alpn": expected_alpn if rows and all(len(row.get("wan_response_tls", [])) == 1 and row["wan_response_tls"][0].get("negotiated_alpn") == expected_alpn for row in rows) else None,
    "natural_endpoint_and_namespace_statuses": lifecycle_identity_ok and lifecycle_status_ok and lifecycle_no_invalidation and namespace_ok,
    "outer_private_user_pid_mount_net": outer_ok,
    "server_only_delegated_cgroup": scope_ok,
    "outer_runner_group_joined": supervisor_joined_ok,
    "runner_status": supervisor.get("runner_status"),
    "results_sha256": sha(results_path),
    "lifecycle_sha256": sha(lifecycle_path),
    "summary_sha256": sha(summary_path),
}
attestation_path.write_text(json.dumps(attestation, indent=2, sort_keys=True) + "\n")
if issues:
    raise SystemExit("WAN protocol arm validation failed: " + "; ".join(issues))

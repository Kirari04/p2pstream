#!/usr/bin/env python3
import hashlib
import importlib.util
import json
import math
import pathlib
import sys

if len(sys.argv) != 11:
    raise SystemExit("usage: validate-wan-sender-wait-arm.py RESULTS LIFECYCLE SUMMARY OUTER SUPERVISOR PRE_HASHES POST_HASHES ATTESTATION PROTOCOL SIZE_MODE")

integration_directory = pathlib.Path(__file__).resolve().parent
copy_validator_path = integration_directory / "validate-copy-record.py"
spec = importlib.util.spec_from_file_location("copy_record_validator", copy_validator_path)
copy_record_validator = importlib.util.module_from_spec(spec)
spec.loader.exec_module(copy_record_validator)
sender_validator_path = integration_directory / "validate-sender-wait-snapshot.py"
sender_spec = importlib.util.spec_from_file_location("sender_wait_validator", sender_validator_path)
sender_wait_validator = importlib.util.module_from_spec(sender_spec)
sender_spec.loader.exec_module(sender_wait_validator)
growth_validator_path = integration_directory / "validate-server-receive-growth.py"
growth_spec = importlib.util.spec_from_file_location("server_receive_growth_validator", growth_validator_path)
server_receive_growth_validator = importlib.util.module_from_spec(growth_spec)
growth_spec.loader.exec_module(server_receive_growth_validator)
byte_credit_validator_path = integration_directory / "validate-byte-credit-recycle.py"
byte_credit_spec = importlib.util.spec_from_file_location("byte_credit_recycle_validator", byte_credit_validator_path)
byte_credit_recycle_validator = importlib.util.module_from_spec(byte_credit_spec)
byte_credit_spec.loader.exec_module(byte_credit_recycle_validator)
tcp_state_validator_path = integration_directory / "validate-tcp-state.py"
tcp_state_spec = importlib.util.spec_from_file_location("tcp_state_validator", tcp_state_validator_path)
tcp_state_validator = importlib.util.module_from_spec(tcp_state_spec)
tcp_state_spec.loader.exec_module(tcp_state_validator)
raw_closure_validator_path = integration_directory / "validate-post-join-raw-closure.py"
raw_closure_spec = importlib.util.spec_from_file_location("post_join_raw_closure_validator", raw_closure_validator_path)
post_join_raw_closure_validator = importlib.util.module_from_spec(raw_closure_spec)
raw_closure_spec.loader.exec_module(post_join_raw_closure_validator)
terminal_validator_path = integration_directory / "validate-terminal-attribution.py"
terminal_spec = importlib.util.spec_from_file_location("terminal_attribution_validator", terminal_validator_path)
terminal_attribution_validator = importlib.util.module_from_spec(terminal_spec)
terminal_spec.loader.exec_module(terminal_attribution_validator)
outcome_validator_path = integration_directory / "validate-arm-outcome.py"
outcome_spec = importlib.util.spec_from_file_location("wan_below_tls_arm_outcome", outcome_validator_path)
wan_below_tls_arm_outcome = importlib.util.module_from_spec(outcome_spec)
outcome_spec.loader.exec_module(wan_below_tls_arm_outcome)

(results_path, lifecycle_path, summary_path, outer_path, supervisor_path,
 pre_path, post_path, attestation_path) = map(pathlib.Path, sys.argv[1:9])
protocol, size_mode = sys.argv[9:]
if protocol not in ("h1", "h2") or size_mode not in ("small", "sustained"):
    raise SystemExit("invalid protocol or size-mode argument")
small = size_mode == "small"
rows = [json.loads(line) for line in results_path.read_text().splitlines() if line.strip()]
lifecycle = [json.loads(line) for line in lifecycle_path.read_text().splitlines() if line.strip()]
summary = json.loads(summary_path.read_text())
outer = json.loads(outer_path.read_text())
supervisor = json.loads(supervisor_path.read_text())
pre = json.loads(pre_path.read_text())
post = json.loads(post_path.read_text())
integrity_issues = []
performance_issues = []
conditioning_issues = []
# All existing validation failures are integrity failures unless the call site
# explicitly records a fixed diagnostic throughput-floor miss below.
issues = integrity_issues

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

def status_zero(value):
    return type(value) is int and value == 0

def sender_interval_wall_contained(raw_pre, raw_post, copy_record):
    """Coarsely correlate one selected request on the shared host wall clock."""
    values = (
        raw_pre.get("request_finished_unix_ns"),
        copy_record.get("started_unix_ns"),
        copy_record.get("handler_return_unix_ns"),
        raw_post.get("request_started_unix_ns"),
    )
    return (all(type(value) is int and 0 < value <= (1 << 64) - 1 for value in values)
            and values[0] <= values[1] <= values[2] <= values[3])

def sender_record_order_valid(order):
    return order == [9, 11, 14, 15, "terminal"]

def sender_terminal_record_cardinality_valid(records):
    return len(records) == 1

def sender_terminal_closure_valid(record, raw_terminal, saved_validation, independent_validation):
    return bool(
        record.get("diagnostic") == "sender-duration" and record.get("action") == "terminal"
        and status_zero(record.get("status")) and status_zero(record.get("validation_status"))
        and record.get("valid") is True and record.get("response") == raw_terminal
        and record.get("validation") == saved_validation == independent_validation
        and independent_validation.get("accepted") is True
        and independent_validation.get("closure_valid") is True
    )

expected = [("warmup", "download", n) for n in range(1, 5)]
expected += [("warmup", "upload", n) for n in range(1, 5)]
expected += [("retained-warmup", "download", 1), ("retained-warmup", "upload", 1)]
expected += [
    ("steady", "download", 1), ("steady", "upload", 1),
    ("steady", "upload", 2), ("steady", "download", 2),
    ("steady", "download", 3), ("steady", "upload", 3),
]
actual = [(row.get("measurement_stage"), row.get("transfer_direction"), row.get("repetition")) for row in rows]
if actual != expected:
    issues.append(f"result order/shape changed: {actual!r}")
required_hashes = {
    "terminal_attribution_validator", "terminal_legacy_references",
    "terminal_build_receipt", "terminal_build_plan", "terminal_build_verifier", "terminal_build_source_inventory",
    "supervisor", "runner", "validator", "arm_outcome_validator", "approved_lifecycle_runner", "frozen_protocol_runner", "frozen_copy_runner",
    "wan_protocol_runner_patch", "copy_runner_patch", "monotonic_runner_patch", "read_wait_runner_patch", "server_capacity_snapshot_patch", "cleanup_status_patch", "go_base_patch", "go_helper",
    "copy_record_validator", "copy_attribution_overlay_patch", "copy_attribution_design",
    "monotonic_observer_patch", "monotonic_validator_patch", "monotonic_arm_validator_patch",
    "final_source_attestation", "integration_notes", "copy_timing_decision_map",
    "runner_reconstruction_attestation", "monotonic_build_manifest", "monotonic_audited_build_manifest",
    "read_wait_observer_patch", "read_wait_observer_checkpoint", "read_wait_focused_checkpoint",
    "read_wait_audited_build_manifest", "capacity_source_checkpoint", "capacity_source_checkpoint_manifest",
    "capacity_focused_checkpoint", "capacity_audited_build_manifest", "server_scope_entrypoint", "server_scope_sentinel",
    "server_scope_validator", "server_scope_resource_validator", "delegated_scope_probe_attestation",
    "sender_observer_checkpoint", "sender_observer_patch", "sender_integration_checkpoint",
    "reviewed_integration_manifest", "reviewed_integration_verifier",
    "sender_snapshot_validator", "sender_snapshot_validator_current", "raw_write_validator", "raw_closure_validator",
    "sender_build_manifest", "sender_harness_receipt",
    "base_cp7_checkpoint", "base_cp7_reviewed_manifest", "base_cp7_runner",
    "sender_runner_patch", "sender_control_patch",
    "server_growth_checkpoint", "server_growth_overlay_patch", "server_growth_runner_patch",
    "server_growth_validator", "server_growth_focused_receipt",
    "combined_checkpoint_manifest", "combined_contract_ledger",
    "raw_ordinary_proof_receipt", "byte_credit_ordinary_proof_receipt", "combined_build_receipt",
    "byte_credit_validator", "byte_credit_adapter_checkpoint", "byte_credit_adapter_contract_ledger",
    "byte_credit_protocol_checkpoint", "byte_credit_protocol_focused_ledger", "byte_credit_protocol_focused_outer",
    "byte_credit_runner_contract",
    "tcp_state_validator", "tcp_producer_checkpoint", "tcp_source_map",
    "tcp_build_records_ledger", "tcp_build_outer_ledger",
    "tcp_validator_contract_ledger", "tcp_validator_contract_outer",
    "history_producer_checkpoint", "history_source_map", "history_build_records_ledger",
    "history_build_outer_ledger", "history_validator_checkpoint",
    "history_validator_contract_ledger", "history_validator_contract_outer",
    "runner_integration_design", "static_contract", "resource_contract", "sender_wrapper_contract",
    "arm_validator_contract", "cleanup_status_contract", "cleanup_status_body", "cleanup_status_holder", "all_contracts",
    "server_binary", "aggregate_agent_binary", "certificate_pem", "private_key_pem",
}
hash_manifest_result = wan_below_tls_arm_outcome.validate_hash_manifests(
    pre, post, size_mode, required_hashes)
issues.extend(hash_manifest_result["issues"])
hashes = hash_manifest_result["hashes"]
expected_major = 1 if protocol == "h1" else 2
expected_alpn = "http/1.1" if protocol == "h1" else "h2"
payload_bytes = (4 if small else 256) << 20
required_pool_bytes = (2 if small else 8) << 20
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
            and isinstance(tls[0].get("origin_connection_id"), str)
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
        "started_unix_ns": started_ns,
        "handler_return_unix_ns": returned_ns,
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
        conditioning_issues.append(f"row {index} did not retain required aggregate receive-pool evidence")

phase_classification = {}
for stage in ("warmup", "retained-warmup", "steady"):
    rates = phase_download_rates.get(stage, [])
    expected_samples = 4 if stage == "warmup" else (1 if stage == "retained-warmup" else 3)
    at_floor = len(rates) == expected_samples and all(rate >= floor for rate in rates)
    phase_classification[stage] = {
        "samples": len(rates),
        "minimum_bytes_per_second": min(rates) if rates else None,
        "all_at_or_above_floor": at_floor,
        "gating": not small,
    }

reuse_expectations = []
reuse_issues = []
reuse_integrity_issues = []
reuse_conditioning_issues = []
first_visits = trace_records[8:12]
if len(first_visits) != 4:
    reuse_integrity_issues.append("missing four first post-close lane-key visits")
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
        reuse_integrity_issues.append(f"sequence {sequence} is not a distinct expected-fresh post-close key with successful PutIdleConn")
    if key:
        first_keys.add(key)
    reuse_expectations.append({"sequence": sequence, "kind": "first-post-close-visit", "lane_position": offset + 1,
                               "connection_key": key, "satisfied": satisfied})
revisits = trace_records[12:]
expected_revisit_count = 4
if len(revisits) != expected_revisit_count:
    reuse_integrity_issues.append(f"same-key revisit count {len(revisits)} changed")
for offset, trace in enumerate(revisits):
    first = first_visits[offset % 4] if len(first_visits) == 4 else {}
    prior_put = first.get("put_idle_conn", [])
    prior_put_ns = prior_put[0].get("observed_unix_ns") if len(prior_put) == 1 else None
    got_ns = trace.get("got_conn_unix_ns")
    elapsed = got_ns - prior_put_ns if isinstance(got_ns, int) and isinstance(prior_put_ns, int) else None
    evidence_complete = bool(
        isinstance(first.get("connection_key"), str) and first.get("connection_key")
        and isinstance(trace.get("connection_key"), str) and trace.get("connection_key")
        and type(trace.get("reused")) is bool and type(trace.get("was_idle")) is bool
        and type(trace.get("idle_time_ns")) is int and trace.get("idle_time_ns") >= 0
        and type(prior_put_ns) is int and prior_put_ns > 0
        and type(got_ns) is int and got_ns > 0
        and isinstance(first.get("origin_connection_id"), str) and first.get("origin_connection_id")
        and isinstance(trace.get("origin_connection_id"), str) and trace.get("origin_connection_id")
    )
    satisfied = bool(evidence_complete
                     and trace.get("connection_key") == first.get("connection_key")
                     and trace.get("reused") is True and trace.get("was_idle") is True
                     and type(elapsed) is int and 0 <= elapsed < 30_000_000_000
                     and trace.get("origin_connection_id") == first.get("origin_connection_id"))
    if not evidence_complete:
        reuse_integrity_issues.append(f"sequence {offset + 13} revisit evidence is missing or malformed")
    elif type(elapsed) is int and elapsed < 0:
        reuse_integrity_issues.append(f"sequence {offset + 13} revisit time precedes its prior PutIdleConn")
    elif not satisfied:
        reuse_conditioning_issues.append(f"sequence {offset + 13} fully evidenced reuse/idle/origin conditioning was not retained")
    reuse_expectations.append({
        "sequence": offset + 13, "kind": "same-key-revisit", "lane_position": offset % 4 + 1,
        "connection_key": trace.get("connection_key"),
        "expected_connection_key": first.get("connection_key"), "prior_put_idle_unix_ns": prior_put_ns,
        "got_conn_unix_ns": got_ns, "elapsed_idle_ns": elapsed, "httptrace_idle_time_ns": trace.get("idle_time_ns"),
        "prior_origin_connection_id": first.get("origin_connection_id"),
        "observed_origin_connection_id": trace.get("origin_connection_id"), "satisfied": satisfied,
    })
reuse_issues = reuse_integrity_issues + reuse_conditioning_issues
issues.extend(reuse_integrity_issues)

sender_records_path = pathlib.Path(str(results_path).removesuffix(".jsonl") + ".sender-wait-records.jsonl")
sender_control_directory = pathlib.Path(str(results_path).removesuffix(".jsonl") + ".sender-wait-controls")
expected_sender_specs = {9: ("retained-warmup", 1), 11: ("steady", 1), 14: ("steady", 2), 15: ("steady", 3)}
copy_records_by_sequence = {record["sequence"]: record for record in copy_records}
try:
    sender_records = [json.loads(line) for line in sender_records_path.read_text().splitlines() if line.strip()]
except (OSError, json.JSONDecodeError) as error:
    sender_records = []
    issues.append(f"sender-duration records are unreadable: {error}")
if len(sender_records) != len(expected_sender_specs) + 1:
    issues.append(f"sender-duration record count {len(sender_records)} changed")
seen_sender_sequences = set()
sender_intervals = []
server_growth_intervals = []
byte_credit_intervals = []
tcp_state_intervals = []
tcp_threshold_history_intervals = []
terminal_records = []
sender_record_order = []
rows_by_sequence = {row.get("diagnostic_sequence"): row for row in rows
                    if type(row.get("diagnostic_sequence")) is int}
for record in sender_records:
    if record.get("action") == "terminal":
        terminal_records.append(record)
        sender_record_order.append("terminal")
        continue
    sequence = record.get("sequence")
    sender_record_order.append(sequence)
    if type(sequence) is not int or sequence in seen_sender_sequences:
        issues.append(f"sender-duration sequence invalid or repeated: {sequence!r}")
        continue
    seen_sender_sequences.add(sequence)
    expected_spec = expected_sender_specs.get(sequence)
    transfer_row = rows_by_sequence.get(sequence, {})
    stage_repetition_ok = bool(expected_spec and record.get("stage") == expected_spec[0]
                               and record.get("direction") == "download"
                               and record.get("repetition") == expected_spec[1]
                               and transfer_row.get("diagnostic_sequence") == sequence
                               and transfer_row.get("measurement_stage") == expected_spec[0]
                               and transfer_row.get("transfer_direction") == "download"
                               and transfer_row.get("repetition") == expected_spec[1])
    pre_path = sender_control_directory / f"sequence-{sequence}-pre-sender.json"
    post_path = sender_control_directory / f"sequence-{sequence}-post-sender.json"
    pre_capacity_path = sender_control_directory / f"sequence-{sequence}-pre-capacity.json"
    post_capacity_path = sender_control_directory / f"sequence-{sequence}-post-capacity.json"
    pre_validation_path = sender_control_directory / f"sequence-{sequence}-pre-validation.json"
    validation_path = sender_control_directory / f"sequence-{sequence}-validation.json"
    try:
        raw_pre = json.loads(pre_path.read_text())
        raw_post = json.loads(post_path.read_text())
        raw_pre_capacity = json.loads(pre_capacity_path.read_text())
        raw_post_capacity = json.loads(post_capacity_path.read_text())
        saved_pre_validation = json.loads(pre_validation_path.read_text())
        saved_validation = json.loads(validation_path.read_text())
        independent_pre = sender_wait_validator.validate_pre(raw_pre, sequence)
        independent_pair = sender_wait_validator.validate_pair(raw_pre, raw_post, sequence)
        independent_growth = server_receive_growth_validator.validate_pair(
            raw_pre_capacity, raw_post_capacity, raw_post, independent_pair,
            copy_records_by_sequence.get(sequence, {}), sequence)
    except (OSError, json.JSONDecodeError, TypeError, ValueError) as error:
        raw_pre = raw_post = raw_pre_capacity = raw_post_capacity = saved_pre_validation = saved_validation = {}
        independent_pre = independent_pair = {"accepted": False}
        independent_growth = {"accepted": False, "issues": [f"server growth validation setup failed: {error!r}"]}
        issues.append(f"sender-duration sequence {sequence!r} artifacts are unreadable: {error}")
    wall_contained = sender_interval_wall_contained(
        raw_pre, raw_post, copy_records_by_sequence.get(sequence, {}))
    boundary_valid = bool(
        expected_spec is not None and stage_repetition_ok
        and record.get("diagnostic") == "sender-duration" and record.get("qualification_result") is False
        and record.get("interval_boundary") == "pre-complete-drain-to-immediate-post-drain"
        and record.get("attribution_limit") == "one-request-concurrency-one-full-lane-set-no-direct-request-stream-marker"
        and record.get("complete") is True and record.get("valid") is True
        and record.get("measurement_start_valid") is True
        and status_zero(record.get("transfer_status")) and status_zero(record.get("sender_pre_status"))
        and status_zero(record.get("sender_pre_validation_status")) and status_zero(record.get("sender_post_status"))
        and status_zero(record.get("sender_validation_status"))
        and status_zero(record.get("pre_resource_status")) and status_zero(record.get("pre_healthy_status"))
        and status_zero(record.get("post_runtime_status")) and status_zero(record.get("post_cgroup_status"))
        and status_zero(record.get("post_capacity_query_status")) and status_zero(record.get("post_healthy_status"))
        and status_zero(record.get("resource_status"))
        and record.get("pre_sender") == raw_pre and record.get("post_sender") == raw_post
        and record.get("sender_pre_validation") == saved_pre_validation == independent_pre
        and record.get("sender_validation") == saved_validation == independent_pair
        and independent_pre.get("accepted") is True and independent_pre.get("measurement_start_valid") is True
        and independent_pair.get("accepted") is True
        and record.get("pre_capacity_request") == raw_pre_capacity
        and record.get("post_capacity_request") == raw_post_capacity
        and record.get("pre_server_capacity") == raw_pre_capacity.get("response")
        and record.get("post_server_capacity") == raw_post_capacity.get("response")
        and independent_growth.get("accepted") is True
        and wall_contained
        and type(independent_pair.get("selected_lane")) is int and independent_pair["selected_lane"] in (1, 2, 3, 4)
        and type(independent_pair.get("selected", {}).get("stream_id")) is int
        and independent_pair["selected"]["stream_id"] > 0
    )
    if independent_growth.get("accepted") is not True:
        issues.append(f"server receive-growth sequence {sequence!r} failed: {'; '.join(independent_growth.get('issues', []))}")
    selected_lane = independent_pair.get("selected_lane")
    selected_stream = independent_pair.get("selected", {}).get("stream_id")
    pre_response = raw_pre.get("response", {}) if type(raw_pre) is dict else {}
    post_response = raw_post.get("response", {}) if type(raw_post) is dict else {}
    selected_pre_sender_rows = [row for row in pre_response.get("send_counters", [])
                                if type(row) is dict and row.get("lane") == selected_lane]
    selected_sender_rows = [row for row in post_response.get("send_counters", [])
                            if type(row) is dict and row.get("lane") == selected_lane]
    selected_pre_sender_row = selected_pre_sender_rows[0] if len(selected_pre_sender_rows) == 1 else {}
    selected_sender_row = selected_sender_rows[0] if len(selected_sender_rows) == 1 else {}
    selected_server_identity = independent_growth.get("selected_identity")
    compound_tuple_join = bool(
        len(selected_sender_rows) == 1 and type(selected_server_identity) is dict
        and selected_server_identity.get("lane") == selected_lane
        and selected_server_identity.get("stream_id") == selected_stream
        and selected_server_identity.get("local_address") == selected_sender_row.get("remote_address")
        and selected_server_identity.get("remote_address") == selected_sender_row.get("local_address")
    )
    selected_raw = independent_pair.get("selected", {}).get("raw_write_activity", {})
    selected_sender_evidence = byte_credit_recycle_validator.selected_sender_evidence(
        selected_sender_row, selected_raw)
    if selected_sender_evidence.get("accepted") is not True:
        issues.append(f"sequence {sequence!r} selected sender byte-credit evidence failed: {'; '.join(selected_sender_evidence.get('issues', []))}")
    selected_byte_credit_identity = {
        "lane": selected_lane,
        "generation": selected_sender_evidence.get("generation"),
        "stream_id": selected_stream,
        "agent_session_identity": selected_sender_row.get("session_identity"),
        "agent_local_address": selected_sender_row.get("local_address"),
        "agent_remote_address": selected_sender_row.get("remote_address"),
        "server_session_identity": selected_server_identity.get("session_identity") if type(selected_server_identity) is dict else None,
        "server_local_address": selected_server_identity.get("local_address") if type(selected_server_identity) is dict else None,
        "server_remote_address": selected_server_identity.get("remote_address") if type(selected_server_identity) is dict else None,
        "agent_id": selected_server_identity.get("agent_id") if type(selected_server_identity) is dict else None,
        "agent_public_id": selected_server_identity.get("agent_public_id") if type(selected_server_identity) is dict else None,
        "sender_byte_zero_wait_count": selected_sender_evidence.get("byte_zero_wait_count"),
    }
    independent_byte_credit = byte_credit_recycle_validator.validate_pair(
        raw_pre_capacity, raw_post_capacity, raw_pre, raw_post,
        selected_byte_credit_identity, sequence)
    selected_agent_trace_rows = [row for row in post_response.get("byte_credit_traces", [])
                                 if type(row) is dict and row.get("lane") == selected_lane
                                 and row.get("session_identity") == selected_sender_row.get("session_identity")
                                 and row.get("local_address") == selected_sender_row.get("local_address")
                                 and row.get("remote_address") == selected_sender_row.get("remote_address")]
    selected_agent_trace = selected_agent_trace_rows[0].get("trace", {}) if len(selected_agent_trace_rows) == 1 else {}
    selected_byte_waits = [row for row in selected_agent_trace.get("wait_records", [])
                           if type(row) is dict and row.get("stream_id") == selected_stream]
    selected_raw_value = selected_sender_row.get("raw_write_activity", {})
    selected_pre_raw_value = selected_pre_sender_row.get("raw_write_activity", {})
    selected_tcp_value = selected_raw_value.get("tcp_state", {}) if type(selected_raw_value) is dict else {}
    selected_pre_tcp_value = selected_pre_raw_value.get("tcp_state", {}) if type(selected_pre_raw_value) is dict else {}
    independent_tcp_state = tcp_state_validator.validate_live(
        selected_tcp_value, sequence=sequence, selected_stream_id=selected_stream,
        selected_waits=selected_byte_waits, owner_identity={
            "lane": selected_raw_value.get("lane") if type(selected_raw_value) is dict else None,
            "generation": selected_raw_value.get("generation") if type(selected_raw_value) is dict else None,
            "local_address": selected_raw_value.get("local_address") if type(selected_raw_value) is dict else None,
            "remote_address": selected_raw_value.get("remote_address") if type(selected_raw_value) is dict else None,
        }, path=f"sequence {sequence} selected tcp_state")
    independent_tcp_history_progression = tcp_state_validator.validate_threshold_history_progression(
        selected_pre_tcp_value, selected_tcp_value, sequence=sequence,
        path=f"sequence {sequence} selected threshold history")
    valid = (boundary_valid and compound_tuple_join
             and selected_sender_evidence.get("accepted") is True
             and independent_byte_credit.get("accepted") is True
             and independent_tcp_state.get("accepted") is True
             and independent_tcp_history_progression.get("accepted") is True)
    if not boundary_valid:
        issues.append(f"sender-duration sequence {sequence!r} failed exact boundary/status/schema/conservation join")
    if not compound_tuple_join:
        issues.append(f"sequence {sequence!r} agent raw/server growth physical tuple join failed")
    if independent_byte_credit.get("accepted") is not True:
        issues.append(f"sequence {sequence!r} byte-credit recycle join failed: {'; '.join(independent_byte_credit.get('issues', []))}")
    if independent_tcp_state.get("accepted") is not True:
        issues.append(f"sequence {sequence!r} TCP-state join failed: {'; '.join(independent_tcp_state.get('issues', []))}")
    if independent_tcp_history_progression.get("accepted") is not True:
        issues.append(f"sequence {sequence!r} TCP threshold-history progression failed: {'; '.join(independent_tcp_history_progression.get('issues', []))}")
    sender_intervals.append({
        "sequence": sequence, "stage": record.get("stage"), "repetition": record.get("repetition"),
        "selected_lane": selected_lane,
        "stream_id": selected_stream,
        "agent_raw_local_address": selected_sender_row.get("local_address"),
        "agent_raw_remote_address": selected_sender_row.get("remote_address"),
        "server_local_address": selected_server_identity.get("local_address") if type(selected_server_identity) is dict else None,
        "server_remote_address": selected_server_identity.get("remote_address") if type(selected_server_identity) is dict else None,
        "compound_tuple_join": compound_tuple_join,
        "raw_write_activity": independent_pair.get("selected", {}).get("raw_write_activity"),
        "measurement_start_valid": record.get("measurement_start_valid"),
        "request_wall_interval_contained": wall_contained, "valid": valid,
        "pre_request_finished_unix_ns": raw_pre.get("request_finished_unix_ns"),
        "copy_started_unix_ns": copy_records_by_sequence.get(sequence, {}).get("started_unix_ns"),
        "copy_handler_return_unix_ns": copy_records_by_sequence.get(sequence, {}).get("handler_return_unix_ns"),
        "post_request_started_unix_ns": raw_post.get("request_started_unix_ns"),
        "pre_sha256": safe_sha(pre_path), "post_sha256": safe_sha(post_path),
        "pre_validation_sha256": safe_sha(pre_validation_path), "validation_sha256": safe_sha(validation_path),
    })
    server_growth_intervals.append({
        "sequence": sequence, "accepted": independent_growth.get("accepted"),
        "selected_identity": independent_growth.get("selected_identity"),
        "selected_event_count": independent_growth.get("selected_event_count"),
        "selected_events": independent_growth.get("selected_events"),
        "pre_prior_event_count": independent_growth.get("pre_prior_event_count"),
        "pre_prior_dropped_events": independent_growth.get("pre_prior_dropped_events"),
        "post_dropped_events": independent_growth.get("post_dropped_events"),
        "timestamp_scope": independent_growth.get("timestamp_scope"),
        "sample_timestamp_semantics": independent_growth.get("sample_timestamp_semantics"),
        "pre_capacity_sha256": safe_sha(pre_capacity_path),
        "post_capacity_sha256": safe_sha(post_capacity_path),
    })
    byte_credit_intervals.append(independent_byte_credit)
    tcp_state_intervals.append(independent_tcp_state)
    threshold_history = independent_tcp_state.get("threshold_history", {})
    tcp_threshold_history_intervals.append({
        "sequence": sequence,
        "selected_lane": selected_lane,
        "stream_id": selected_stream,
        "structural_accepted": threshold_history.get("accepted") is True,
        "explanation_available": threshold_history.get("explanation_available") is True,
        "explanation_inconclusive": threshold_history.get("explanation_inconclusive") is True,
        "persistent_history_inconclusive": threshold_history.get("persistent_inconclusive") is True,
        "explanation_status": threshold_history.get("explanation_status"),
        "selected_pre_sequence": threshold_history.get("selected_pre_sequence"),
        "selected_pre_arm_ordinal": threshold_history.get("selected_pre_arm_ordinal"),
        "provider_capture_calls": threshold_history.get("provider_capture_calls"),
        "provider_capture_errors": threshold_history.get("provider_capture_errors"),
        "provider_capture_duration": threshold_history.get("provider_capture_duration"),
        "shared_provider_capture_calls": threshold_history.get("shared_provider_capture_calls"),
        "shared_provider_capture_duration": threshold_history.get("shared_provider_capture_duration"),
        "pre_post_progression_accepted": independent_tcp_history_progression.get("accepted") is True,
    })
if seen_sender_sequences != set(expected_sender_specs):
    issues.append(f"sender-duration sequence set {sorted(v for v in seen_sender_sequences if type(v) is int)!r} changed")
if not sender_record_order_valid(sender_record_order):
    issues.append(f"sender-duration record order changed: {sender_record_order!r}")
terminal_valid = False
if not sender_terminal_record_cardinality_valid(terminal_records):
    issues.append(f"terminal sender-duration record cardinality is {len(terminal_records)}, want one")
else:
    terminal_record = terminal_records[0]
    terminal_path = sender_control_directory / "terminal-sender.json"
    terminal_validation_path = sender_control_directory / "terminal-validation.json"
    try:
        raw_terminal = json.loads(terminal_path.read_text())
        saved_terminal_validation = json.loads(terminal_validation_path.read_text())
        independent_terminal = sender_wait_validator.validate_terminal(raw_terminal, 15)
    except (OSError, json.JSONDecodeError, TypeError, ValueError) as error:
        raw_terminal = saved_terminal_validation = {}
        independent_terminal = {"accepted": False}
        issues.append(f"terminal sender-duration artifacts are unreadable: {error}")
    terminal_valid = sender_terminal_closure_valid(
        terminal_record, raw_terminal, saved_terminal_validation, independent_terminal)
    if not terminal_valid:
        issues.append("terminal sender-duration ledger closure failed")
raw_final_result = post_join_raw_closure_validator.validate_post_join_raw_closure(
    sender_control_directory, sender_wait_validator)
raw_final_closure_valid = raw_final_result.get("accepted") is True
raw_final_record = raw_final_result.get("record", {})
raw_final_terminal = raw_final_result.get("terminal", {})
raw_final_saved_validation = raw_final_result.get("saved_validation", {})
raw_final_independent_validation = raw_final_result.get("independent_validation", {})
# The old verdict and record remain exported unchanged. Only this prospective
# adapter gates on the independently proven terminal attribution contract.
prospective_terminal_result = {}
prospective_terminal_valid = False
try:
    prospective_terminal_result = terminal_attribution_validator.evaluate_files(
        sender_control_directory / "final-raw-write-terminal.json",
        sender_control_directory / "final-raw-write-terminal.json.attribution.json",
        sender_control_directory)
    saved_prospective = json.loads((supervisor_path.parent / "prospective-terminal-attribution.json").read_text())
    saved_raw_supervisor = json.loads((supervisor_path.parent / "raw-post-join-supervisor-validation.json").read_text())
    raw_supervisor = supervisor.get("raw_write_evidence", {})
    expected_raw_status = 0 if raw_final_result.get("accepted") is True else 1
    prospective_terminal_valid = (prospective_terminal_result == saved_prospective
        and saved_raw_supervisor == raw_final_result
        and isinstance(raw_supervisor, dict)
        and type(raw_supervisor.get("finalization_status")) is int
        and raw_supervisor["finalization_status"] == expected_raw_status
        and raw_supervisor.get("final_closure_valid") is raw_final_result.get("accepted")
        and prospective_terminal_result.get("attribution_accepted") is True
        and prospective_terminal_result.get("legacy_final_verdict") == raw_final_independent_validation
        and prospective_terminal_result.get("legacy_final_record") == raw_final_record)
except (OSError, ValueError, TypeError, KeyError) as error:
    issues.append(f"prospective terminal attribution unreadable or invalid: {error}")
if not prospective_terminal_valid:
    issues.extend(raw_final_result.get("issues", []))
    issues.append("prospective terminal attribution did not independently validate")

expected_control_names = {
    "terminal-sender.json", "terminal-validation.json", "final-raw-write-terminal.json",
    "final-raw-write-validation.json", "final-raw-write-record.json",
    "final-raw-write-terminal.json.attribution.json",
}
for sequence in expected_sender_specs:
    expected_control_names.update({
        f"sequence-{sequence}-pre-capacity.json", f"sequence-{sequence}-pre-sender.json",
        f"sequence-{sequence}-pre-validation.json", f"sequence-{sequence}-post-sender.json",
        f"sequence-{sequence}-post-capacity.json", f"sequence-{sequence}-validation.json",
    })
try:
    actual_control_names = {path.name for path in sender_control_directory.iterdir() if path.is_file()}
except OSError as error:
    actual_control_names = set()
    issues.append(f"sender-duration control directory is unreadable: {error}")
if actual_control_names != expected_control_names:
    issues.append(f"sender-duration control file set changed: {sorted(actual_control_names)!r}")

summary_outcome = wan_below_tls_arm_outcome.classify_summary(
    size_mode, phase_classification, summary, reuse_integrity_issues,
    reuse_conditioning_issues, floor)
issues.extend(summary_outcome["integrity_issues"])
conditioning_issues.extend(summary_outcome["conditioning_issues"])
performance_issues.extend(summary_outcome["performance_issues"])
observed_floor_pass = summary_outcome["observed_floor_pass"]
floor_gate_pass = summary_outcome["floor_gate_pass"]
if (summary.get("public_protocol") != protocol or summary.get("diagnostic_only") is not True
        or summary.get("size_mode") != size_mode):
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
lifecycle_status_ok = not any(not (row.get("forced") is False and status_zero(row.get("status")) and row.get("group_joined") is True
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
    and status_zero(scope_unit_final.get("query_status")) and scope_unit_final.get("joined") is True
    and scope_unit_final.get("failed_state_rejected") is True
    and scope_unit_final.get("confirmation") == "show" and status_zero(scope_unit_final.get("show_status"))
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
                        and status_zero(supervisor.get("server_scope", {}).get("systemd_run_status"))
                        and status_zero(supervisor.get("server_scope", {}).get("resource_status"))
                        and supervisor.get("server_scope", {}).get("unit_joined") is True
                        and status_zero(supervisor.get("server_scope", {}).get("unit_query_status"))
                        and supervisor.get("server_scope", {}).get("unit_active_state") != "failed"
                        and status_zero(supervisor.get("sender_evidence", {}).get("terminal_status"))
                        and status_zero(supervisor.get("sender_evidence", {}).get("terminal_validation_status"))
                        and supervisor.get("sender_evidence", {}).get("terminal_valid") is True
                        and prospective_terminal_valid
                        and status_zero(supervisor.get("prospective_terminal_attribution", {}).get("validation_status"))
                        and supervisor.get("prospective_terminal_attribution", {}).get("accepted") is True)
if not supervisor_joined_ok:
    issues.append(f"outer runner process group did not join naturally: {supervisor!r}")
runner_outcome = wan_below_tls_arm_outcome.classify_runner(
    supervisor, supervisor_joined_ok, integrity_issues,
    conditioning_issues, performance_issues)
issues.extend(runner_outcome["integrity_issues"])

outcome_fields = wan_below_tls_arm_outcome.attestation_outcome(
    integrity_issues, conditioning_issues, performance_issues)
all_issues = outcome_fields["validation_issues"]
attestation = {
    "schema_version": 1,
    **outcome_fields,
    "diagnostic_only": True,
    "protocol": protocol,
    "size_mode": size_mode,
    "performance_gate_enabled": not small,
    "row_count": len(rows),
    "classification": summary.get("classification"),
    "download_floor_bytes_per_second": floor,
    "download_phase_classification": phase_classification,
    "reuse_expectations": reuse_expectations,
    "copy_attribution_records": copy_records,
    "copy_attribution_issues_by_sequence": copy_issues_by_sequence,
    "copy_attribution_interpretation": expected_copy_interpretation,
    "sender_duration_intervals": sender_intervals,
    "server_receive_growth_intervals": server_growth_intervals,
    "server_receive_growth_interpretation": "The process-global ring is retained in serialized order. Timestamp ordering and control-envelope bounds gate only events matching the exact selected server agent/lane/session/tuple/stream identity. Sample timestamps mark controller sample start, not growth publication completion, and are never subtracted from agent monotonic durations.",
    "byte_credit_recycle_intervals": byte_credit_intervals,
    "byte_credit_recycle_interpretation": "The exact ordered positive byte-window ledger is joined across the inverse physical tuple, lane, positive raw-owner generation, session identities and stream. Receiver, peer and wait durations are monotonic same-process observations; Unix timestamps provide local order only, and no cross-endpoint clock subtraction or causal credit is claimed.",
    "tcp_state_intervals": tcp_state_intervals,
    "tcp_state_interpretation": "TCP_INFO samples are synchronous non-atomic views of the exact agent-owned socket. Owner-relative monotonic coordinates validate only same-owner ordering and brackets; Unix timestamps are retained for local ordering and are never subtracted across endpoints. A valid sampled state can support or contradict a narrow hypothesis but cannot exclude an unsampled transient or establish a causal runtime defect.",
    "tcp_threshold_history_intervals": tcp_threshold_history_intervals,
    "tcp_threshold_history_interpretation": "The retained history brackets only the first sampled snd_ssthresh difference around raw writes on the exact owned socket. The current interval comparison is joined to that interval's pre TCP_INFO and remains distinct from the persistent first-transition record. A declared missing, later, or overlapping transition makes the explanation unavailable without rewriting mature raw-write, byte-credit, or TCP-state integrity. Provider durations cover only provider capture calls and are not total instrumentation overhead, an atomic kernel trace, or causal evidence.",
    "sender_terminal_closure_valid": terminal_valid,
    "raw_write_final_closure_valid": raw_final_closure_valid,
    "prospective_terminal_attribution_valid": prospective_terminal_valid,
    "prospective_terminal_attribution": prospective_terminal_result,
    "raw_write_final_closure": raw_final_independent_validation,
    "raw_write_final_record": raw_final_record,
    "raw_write_interpretation": "Raw occupancy measures time inside the bound raw socket Write wrapper, including Go TCP and poller work, and counts encrypted buffers passed below tls.Conn. It does not decode TLS records or attribute ciphertext bytes to DATA. Residual DATA-envelope time is outside that wrapper but is not uniquely attributable to the TLS output lock; concurrent TLS protocol writes are classified only by temporal occupancy.",
    "sender_attribution_limit": "one-request-concurrency-one-full-lane-set-no-direct-request-stream-marker",
    "sender_wall_clock_interpretation": "Common-host Unix timestamps prove only coarse enclosure of the selected public handler by the pre/post sender-control requests; monotonic durations are not compared across observers and the enclosure does not prove cause.",
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
if all_issues:
    raise SystemExit("WAN protocol arm validation failed: " + "; ".join(all_issues))

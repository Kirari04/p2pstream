#!/usr/bin/env python3
import hashlib
import importlib.util
import json
import math
import pathlib
import sys

if len(sys.argv) != 11:
    raise SystemExit("usage: validate-wan-copy-attribution-arm.py RESULTS LIFECYCLE SUMMARY OUTER SUPERVISOR PRE_HASHES POST_HASHES ATTESTATION PROTOCOL SMOKE")

copy_validator_path = pathlib.Path(__file__).with_name("validate-copy-record.py")
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
    "wan_protocol_runner_patch", "copy_runner_patch", "monotonic_runner_patch", "go_base_patch", "go_helper", "diagnostic_build_manifest",
    "copy_record_validator", "copy_attribution_overlay_patch", "copy_attribution_design",
    "monotonic_observer_patch", "monotonic_validator_patch", "monotonic_arm_validator_patch",
    "final_source_attestation", "integration_notes", "copy_timing_decision_map",
    "runner_reconstruction_attestation",
    "audited_build_manifest",
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
            and outer.get("python_parent_is_one") is True)
if not outer_ok:
    issues.append(f"outer user/PID/mount/network namespace ownership missing: {outer!r}")
supervisor_joined_ok = (supervisor.get("group_joined") is True and supervisor.get("forced") is False
                        and supervisor.get("invalidates_success") is False)
if not supervisor_joined_ok:
    issues.append(f"outer runner process group did not join naturally: {supervisor!r}")
runner_status_ok = supervisor.get("runner_status") == 0
if not runner_status_ok:
    issues.append(f"runner exited nonzero after preserving evidence: {supervisor.get('runner_status')!r}")

def sha(path: pathlib.Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()

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
    "all_observed_http_major": expected_major if rows and all(row.get("observed_http_major") == expected_major for row in rows) else None,
    "verified_alpn": expected_alpn if rows and all(len(row.get("wan_response_tls", [])) == 1 and row["wan_response_tls"][0].get("negotiated_alpn") == expected_alpn for row in rows) else None,
    "natural_endpoint_and_namespace_statuses": lifecycle_identity_ok and lifecycle_status_ok and lifecycle_no_invalidation and namespace_ok,
    "outer_private_user_pid_mount_net": outer_ok,
    "outer_runner_group_joined": supervisor_joined_ok,
    "runner_status": supervisor.get("runner_status"),
    "results_sha256": sha(results_path),
    "lifecycle_sha256": sha(lifecycle_path),
    "summary_sha256": sha(summary_path),
}
attestation_path.write_text(json.dumps(attestation, indent=2, sort_keys=True) + "\n")
if issues:
    raise SystemExit("WAN protocol arm validation failed: " + "; ".join(issues))

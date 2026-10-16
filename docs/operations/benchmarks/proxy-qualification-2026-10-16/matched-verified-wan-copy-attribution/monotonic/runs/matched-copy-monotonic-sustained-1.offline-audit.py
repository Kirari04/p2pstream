#!/usr/bin/env python3
import hashlib
import json
import pathlib
import subprocess
import sys


run = pathlib.Path(sys.argv[1])
output = pathlib.Path(sys.argv[2])
issues = []


def sha(path):
    digest = hashlib.sha256()
    with pathlib.Path(path).open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


certificate_der = subprocess.run(
    ["openssl", "x509", "-in", str(run / "tls/public.test.crt.pem"), "-outform", "DER"],
    check=True, capture_output=True,
).stdout
certificate_fingerprint = hashlib.sha256(certificate_der).hexdigest()
expected_schedule = [
    *(('warmup', 'download', n) for n in range(1, 5)),
    *(('warmup', 'upload', n) for n in range(1, 5)),
    ('retained-warmup', 'download', 1), ('retained-warmup', 'upload', 1),
    ('steady', 'download', 1), ('steady', 'upload', 1),
    ('steady', 'upload', 2), ('steady', 'download', 2),
    ('steady', 'download', 3), ('steady', 'upload', 3),
]
arms = {}
for protocol, major, alpn in (("h1", 1, "http/1.1"), ("h2", 2, "h2")):
    arm = run / protocol
    rows = [json.loads(line) for line in (arm / "results.jsonl").read_text().splitlines() if line]
    lifecycle = [json.loads(line) for line in (arm / "results.lifecycle.jsonl").read_text().splitlines() if line]
    if [(r.get("measurement_stage"), r.get("transfer_direction"), r.get("repetition")) for r in rows] != expected_schedule:
        issues.append(f"{protocol}: schedule differs")
    row_evidence = []
    traces = {}
    for sequence, row in enumerate(rows, 1):
        direction = row.get("transfer_direction")
        expected_bytes = 256 << 20 if direction == "download" else 32
        expected_method = "GET" if direction == "download" else "POST"
        expected_path = "/agent/bulk" if direction == "download" else "/agent/bulk-upload"
        if not (row.get("diagnostic_sequence") == sequence and row.get("protocol") == protocol and
                row.get("public_protocol") == protocol and row.get("observed_http_major") == major and
                row.get("attempted") == row.get("useful_completed") == 1 and row.get("errors") == 0 and
                row.get("status_codes") == {"200": 1} and row.get("configured_payload_bytes") == 256 << 20 and
                row.get("response_bytes") == expected_bytes and row.get("persisted_event_delta") == 1 and
                row.get("event_stats_captured") is True):
            issues.append(f"{protocol} sequence {sequence}: payload/status/event/protocol changed")
        tls = row.get("wan_response_tls", [])
        if not (len(tls) == 1 and tls[0].get("http_major") == major and tls[0].get("negotiated_alpn") == alpn and
                tls[0].get("peer_leaf_der_sha256") == certificate_fingerprint and tls[0].get("origin_connection_id")):
            issues.append(f"{protocol} sequence {sequence}: TLS/certificate/origin changed")
        envelope = row.get("response_copy_attribution", {})
        matches = envelope.get("matches", []) if isinstance(envelope, dict) else []
        if not (envelope.get("overflow") is False and envelope.get("store_count") == sequence and
                envelope.get("sequence") == str(sequence) and len(matches) == 1):
            issues.append(f"{protocol} sequence {sequence}: copy envelope changed")
            continue
        record = matches[0]
        copy = record.get("copy", {})
        handler = record.get("handler_elapsed_ns")
        if not (record.get("sequence") == str(sequence) and record.get("method") == expected_method and
                record.get("path") == expected_path and record.get("scope") == "response-body-copy" and
                isinstance(handler, int) and handler >= 0 and copy.get("complete") is True and
                copy.get("body_wraps") == 1 and copy.get("downstream_read_from_calls") == 0 and
                copy.get("read_errors") == copy.get("write_errors") == copy.get("short_writes") == 0 and
                copy.get("read_eof") in (0, 1)):
            issues.append(f"{protocol} sequence {sequence}: copy fixed contract changed")
        metrics = {name: copy.get(name, {}) for name in ("upstream_read", "downstream_write", "inter_read_gap")}
        for name, metric in metrics.items():
            calls, total, maximum, span = (metric.get(key) for key in ("calls", "total_ns", "max_ns", "span_ns"))
            expected_metric_bytes = 0 if name == "inter_read_gap" else expected_bytes
            buckets, stalls = metric.get("duration_bucket_counts"), metric.get("longest_stalls")
            positive = [s for s in stalls if isinstance(s, dict) and s.get("duration_ns", 0) > 0] if isinstance(stalls, list) else []
            valid_metric = (isinstance(calls, int) and calls >= 0 and metric.get("bytes") == expected_metric_bytes and
                            isinstance(total, int) and isinstance(maximum, int) and isinstance(span, int) and
                            0 <= maximum <= total <= span <= handler and isinstance(buckets, list) and len(buckets) == 8 and
                            sum(buckets) == calls and isinstance(stalls, list) and len(stalls) == 8 and
                            [s["duration_ns"] for s in positive] == sorted((s["duration_ns"] for s in positive), reverse=True) and
                            (positive[0]["duration_ns"] if positive else 0) == maximum)
            if not valid_metric:
                issues.append(f"{protocol} sequence {sequence}: {name} conservation/bounds changed")
        read, write, gap = metrics["upstream_read"], metrics["downstream_write"], metrics["inter_read_gap"]
        if not (gap.get("calls") == read.get("calls") - 1 and
                read.get("span_ns") == read.get("total_ns") + gap.get("total_ns") and
                read.get("total_ns") + write.get("total_ns") <= handler and
                read.get("total_ns") + gap.get("total_ns") <= handler):
            issues.append(f"{protocol} sequence {sequence}: monotonic telescope/handler budget changed")
        trace_envelope = row.get("upstream_transport_trace", {})
        trace_matches = trace_envelope.get("matches", []) if isinstance(trace_envelope, dict) else []
        if not (trace_envelope.get("overflow") is False and trace_envelope.get("store_count") == sequence and
                trace_envelope.get("sequence") == str(sequence) and len(trace_matches) == 1 and
                trace_matches[0].get("sequence") == str(sequence) and trace_matches[0].get("method") == expected_method and
                trace_matches[0].get("path") == expected_path and trace_matches[0].get("callback_overflow") is False and
                len(trace_matches[0].get("got_conn", [])) == 1):
            issues.append(f"{protocol} sequence {sequence}: transport trace changed")
            continue
        trace = trace_matches[0]
        got = trace["got_conn"][0]
        key = "|".join((got.get("connection_type", ""), got.get("local_address", ""), got.get("remote_address", "")))
        traces[sequence] = {
            "key": key, "origin": tls[0]["origin_connection_id"], "got_unix_ns": got.get("observed_unix_ns"),
            "reused": got.get("reused"), "was_idle": got.get("was_idle"), "idle_time_ns": got.get("idle_time_ns"),
            "put_unix_ns": trace.get("put_idle_conn", [{}])[0].get("observed_unix_ns") if len(trace.get("put_idle_conn", [])) == 1 else None,
        }
        row_evidence.append({
            "sequence": sequence, "stage": row.get("measurement_stage"), "direction": direction,
            "rate_bytes_per_second": row.get("useful_transfer_bytes_per_second"), "handler_elapsed_ns": handler,
            "read_calls": read.get("calls"), "read_total_ns": read.get("total_ns"), "read_span_ns": read.get("span_ns"),
            "write_total_ns": write.get("total_ns"), "write_span_ns": write.get("span_ns"),
            "gap_total_ns": gap.get("total_ns"), "gap_span_ns": gap.get("span_ns"),
            "connection_key": key, "origin_connection_id": traces[sequence]["origin"],
            "reused": got.get("reused"), "was_idle": got.get("was_idle"), "idle_time_ns": got.get("idle_time_ns"),
        })
    reuse = []
    for sequence in range(13, 17):
        prior = traces[sequence - 4]
        current = traces[sequence]
        elapsed = current["got_unix_ns"] - prior["put_unix_ns"] if prior["put_unix_ns"] is not None else None
        satisfied = (current["key"] == prior["key"] and current["reused"] is True and current["was_idle"] is True and
                     current["origin"] == prior["origin"] and isinstance(elapsed, int) and elapsed < 30_000_000_000)
        expiry_confounded = (current["key"] == prior["key"] and current["reused"] is False and current["was_idle"] is False and
                             current["origin"] != prior["origin"] and isinstance(elapsed, int) and elapsed >= 30_000_000_000)
        if not (satisfied or expiry_confounded):
            issues.append(f"{protocol} sequence {sequence}: unexplained revisit outcome")
        reuse.append({"sequence": sequence, "prior_sequence": sequence - 4, "elapsed_since_prior_put_idle_ns": elapsed,
                      "same_physical_key": current["key"] == prior["key"], "same_origin_connection_id": current["origin"] == prior["origin"],
                      "reused": current["reused"], "was_idle": current["was_idle"], "satisfied": satisfied,
                      "expiry_confounded": expiry_confounded})
    exits = [event for event in lifecycle if event.get("event") == "process-exit"]
    supervisor = json.loads((arm / "supervisor-lifecycle.json").read_text())
    outer = json.loads((arm / "outer-namespaces.json").read_text())
    natural = (len(exits) == 5 and len({event.get("label") for event in exits}) == 5 and
               all(event.get("status") == 0 and event.get("forced") is False and event.get("group_joined") is True and
                   event.get("invalidates_success") is False for event in exits) and
               supervisor.get("group_joined") is True and supervisor.get("forced") is False and supervisor.get("invalidates_success") is False and
               outer.get("all_private") is True and outer.get("shell_pid_is_one") is True and outer.get("python_parent_is_one") is True)
    if not natural:
        issues.append(f"{protocol}: lifecycle/outer topology not natural")
    arm_hashes_equal = (arm / "pre-hashes.json").read_bytes() == (arm / "post-hashes.json").read_bytes()
    if not arm_hashes_equal:
        issues.append(f"{protocol}: arm pre/post hashes differ")
    download_phases = {}
    for item in row_evidence:
        if item["direction"] == "download":
            download_phases.setdefault(item["stage"], []).append(item)
    arms[protocol] = {
        "rows": len(rows), "http_major": major, "alpn": alpn, "certificate_der_sha256": certificate_fingerprint,
        "monotonic_conservation_complete": not any(issue.startswith(f"{protocol} sequence") and "monotonic" in issue for issue in issues),
        "natural_cleanup": natural, "pre_post_hashes_equal": arm_hashes_equal,
        "runner_status": int((arm / "runner.status").read_text()), "validation_status": int((arm / "validation.status").read_text()),
        "summary_classification": json.loads((arm / "results.summary.json").read_text()).get("classification"),
        "reuse": reuse, "download_phases": download_phases, "row_evidence": row_evidence,
        "raw_results_sha256": sha(arm / "results.jsonl"), "lifecycle_sha256": sha(arm / "results.lifecycle.jsonl"),
        "summary_sha256": sha(arm / "results.summary.json"), "attestation_sha256": sha(arm / "attestation.json"),
    }

pair_hashes_equal = (run / "pre-hashes.json").read_bytes() == (run / "post-hashes.json").read_bytes()
if not pair_hashes_equal:
    issues.append("pair pre/post hashes differ")
result = {
    "schema_version": 1, "audit_complete": not issues, "diagnostic_only": True, "qualification_claim": False,
    "issues": issues, "certificate_der_sha256": certificate_fingerprint, "arms": arms,
    "pair_pre_post_hashes_equal": pair_hashes_equal,
    "wrapper_status": int((run.parent / f"{run.name}.wrapper.status").read_text()),
    "interpretation": "Read and Write monotonic durations are compared separately with handler elapsed. Inter-read gap overlaps Write and bookkeeping and is not additive with Read plus Write. POST timing covers only the 32-byte response copy. Throughput is diagnostic and not qualification evidence.",
    "limitations": "A revisit at or beyond the unchanged 30-second idle deadline is expiry-confounded and is not classified healthy. agent_tunnels_initial nested lane values may be later maxima; high-water fields are not coherent snapshots.",
}
output.write_text(json.dumps(result, indent=2, sort_keys=True) + "\n")
if issues:
    raise SystemExit("; ".join(issues))

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
    ('steady', 'download', 1), ('steady', 'upload', 1), ('reuse-probe', 'download', 1),
]
arms = {}
for protocol, major, alpn in (("h1", 1, "http/1.1"), ("h2", 2, "h2")):
    arm = run / protocol
    rows = [json.loads(line) for line in (arm / "results.jsonl").read_text().splitlines() if line]
    lifecycle = [json.loads(line) for line in (arm / "results.lifecycle.jsonl").read_text().splitlines() if line]
    row_evidence = []
    observed_schedule = [(r.get("measurement_stage"), r.get("transfer_direction"), r.get("repetition")) for r in rows]
    if observed_schedule != expected_schedule:
        issues.append(f"{protocol}: schedule differs")
    keys = {}
    origins = {}
    puts = {}
    for sequence, row in enumerate(rows, 1):
        direction = row.get("transfer_direction")
        expected_bytes = 4 << 20 if direction == "download" else 32
        if not (row.get("diagnostic_sequence") == sequence and row.get("protocol") == protocol and
                row.get("public_protocol") == protocol and row.get("observed_http_major") == major and
                row.get("attempted") == row.get("useful_completed") == 1 and row.get("errors") == 0 and
                row.get("status_codes") == {"200": 1} and row.get("response_bytes") == expected_bytes):
            issues.append(f"{protocol} sequence {sequence}: result correctness/protocol changed")
        tls = row.get("wan_response_tls", [])
        if not (len(tls) == 1 and tls[0].get("http_major") == major and tls[0].get("negotiated_alpn") == alpn and
                tls[0].get("peer_leaf_der_sha256") == certificate_fingerprint and tls[0].get("origin_connection_id")):
            issues.append(f"{protocol} sequence {sequence}: TLS/certificate/origin evidence changed")
        copy_envelope = row.get("response_copy_attribution", {})
        matches = copy_envelope.get("matches", []) if isinstance(copy_envelope, dict) else []
        if not (copy_envelope.get("overflow") is False and copy_envelope.get("store_count") == sequence and
                copy_envelope.get("sequence") == str(sequence) and len(matches) == 1):
            issues.append(f"{protocol} sequence {sequence}: copy envelope cardinality changed")
            continue
        record = matches[0]
        copy = record.get("copy", {})
        handler_elapsed = record.get("handler_elapsed_ns")
        if not (record.get("sequence") == str(sequence) and record.get("scope") == "response-body-copy" and
                record.get("method") == ("GET" if direction == "download" else "POST") and
                record.get("path") == ("/agent/bulk" if direction == "download" else "/agent/bulk-upload") and
                isinstance(handler_elapsed, int) and handler_elapsed >= 0 and copy.get("complete") is True and
                copy.get("body_wraps") == 1 and copy.get("downstream_read_from_calls") == 0 and
                copy.get("read_errors") == copy.get("write_errors") == copy.get("short_writes") == 0 and
                copy.get("read_eof") in (0, 1)):
            issues.append(f"{protocol} sequence {sequence}: copy record fixed contract changed")
        metrics = {name: copy.get(name, {}) for name in ("upstream_read", "downstream_write", "inter_read_gap")}
        for name, metric in metrics.items():
            expected_metric_bytes = 0 if name == "inter_read_gap" else expected_bytes
            calls, total, maximum, span = (metric.get(key) for key in ("calls", "total_ns", "max_ns", "span_ns"))
            buckets, stalls = metric.get("duration_bucket_counts"), metric.get("longest_stalls")
            positive = [stall for stall in stalls if isinstance(stall, dict) and stall.get("duration_ns", 0) > 0] if isinstance(stalls, list) else []
            if not (isinstance(calls, int) and calls >= 0 and metric.get("bytes") == expected_metric_bytes and
                    isinstance(total, int) and isinstance(maximum, int) and isinstance(span, int) and
                    0 <= maximum <= total <= span <= handler_elapsed and isinstance(buckets, list) and len(buckets) == 8 and
                    sum(buckets) == calls and isinstance(stalls, list) and len(stalls) == 8 and
                    [s["duration_ns"] for s in positive] == sorted((s["duration_ns"] for s in positive), reverse=True) and
                    (positive[0]["duration_ns"] if positive else 0) == maximum):
                issues.append(f"{protocol} sequence {sequence}: {name} conservation/bounds changed")
        read, write, gap = metrics["upstream_read"], metrics["downstream_write"], metrics["inter_read_gap"]
        if not (gap.get("calls") == read.get("calls") - 1 and
                read.get("span_ns") == read.get("total_ns") + gap.get("total_ns") and
                read.get("total_ns") + write.get("total_ns") <= handler_elapsed and
                read.get("total_ns") + gap.get("total_ns") <= handler_elapsed):
            issues.append(f"{protocol} sequence {sequence}: monotonic telescope/handler budget changed")
        trace_envelope = row.get("upstream_transport_trace", {})
        traces = trace_envelope.get("matches", []) if isinstance(trace_envelope, dict) else []
        if not (trace_envelope.get("overflow") is False and trace_envelope.get("store_count") == sequence and len(traces) == 1 and
                traces[0].get("callback_overflow") is False and len(traces[0].get("got_conn", [])) == 1):
            issues.append(f"{protocol} sequence {sequence}: transport trace cardinality changed")
            continue
        trace = traces[0]
        got = trace["got_conn"][0]
        key = "|".join((got.get("connection_type", ""), got.get("local_address", ""), got.get("remote_address", "")))
        keys[sequence] = key
        origins[sequence] = tls[0]["origin_connection_id"]
        put = trace.get("put_idle_conn", [])
        puts[sequence] = put[0].get("observed_unix_ns") if len(put) == 1 else None
        row_evidence.append({
            "sequence": sequence, "stage": row.get("measurement_stage"), "direction": direction,
            "rate_bytes_per_second": row.get("useful_transfer_bytes_per_second"),
            "handler_elapsed_ns": handler_elapsed, "read_calls": read.get("calls"),
            "read_total_ns": read.get("total_ns"), "read_span_ns": read.get("span_ns"),
            "write_total_ns": write.get("total_ns"), "write_span_ns": write.get("span_ns"),
            "gap_total_ns": gap.get("total_ns"), "gap_span_ns": gap.get("span_ns"),
            "connection_key": key, "origin_connection_id": origins[sequence], "reused": got.get("reused"),
            "was_idle": got.get("was_idle"), "idle_time_ns": got.get("idle_time_ns"),
        })
    if not (len(rows) == 13 and len(set(keys.get(i) for i in range(9, 13))) == 4 and
            keys.get(13) == keys.get(9) and origins.get(13) == origins.get(9)):
        issues.append(f"{protocol}: seq13 key/origin reuse binding changed")
    seq13_trace = rows[12]["upstream_transport_trace"]["matches"][0]["got_conn"][0]
    if not (seq13_trace.get("reused") is True and seq13_trace.get("was_idle") is True and puts.get(9) is not None and
            seq13_trace.get("observed_unix_ns") > puts[9] and
            seq13_trace.get("observed_unix_ns") - puts[9] < 30_000_000_000):
        issues.append(f"{protocol}: seq13 actual reuse/idle age changed")
    exits = [event for event in lifecycle if event.get("event") == "process-exit"]
    supervisor = json.loads((arm / "supervisor-lifecycle.json").read_text())
    outer = json.loads((arm / "outer-namespaces.json").read_text())
    natural = (len(exits) == 5 and len({event.get("label") for event in exits}) == 5 and
               all(event.get("status") == 0 and event.get("forced") is False and event.get("group_joined") is True and
                   event.get("invalidates_success") is False for event in exits) and
               supervisor == {"runner_status": 0, "group_joined": True, "forced": False, "invalidates_success": False} and
               outer.get("all_private") is True and outer.get("shell_pid_is_one") is True and outer.get("python_parent_is_one") is True)
    if not natural:
        issues.append(f"{protocol}: lifecycle/outer topology not natural")
    if (arm / "pre-hashes.json").read_bytes() != (arm / "post-hashes.json").read_bytes():
        issues.append(f"{protocol}: arm pre/post hashes differ")
    arms[protocol] = {
        "rows": len(rows), "http_major": major, "alpn": alpn, "certificate_der_sha256": certificate_fingerprint,
        "all_monotonic_conservation": not any(issue.startswith(f"{protocol} sequence") for issue in issues),
        "natural_cleanup": natural, "pre_post_hashes_equal": (arm / "pre-hashes.json").read_bytes() == (arm / "post-hashes.json").read_bytes(),
        "seq13": row_evidence[-1], "seq13_elapsed_since_seq9_put_idle_ns": seq13_trace["observed_unix_ns"] - puts[9],
        "download_rates_bytes_per_second": [r["useful_transfer_bytes_per_second"] for r in rows if r["transfer_direction"] == "download"],
        "row_evidence": row_evidence,
        "raw_results_sha256": sha(arm / "results.jsonl"), "lifecycle_sha256": sha(arm / "results.lifecycle.jsonl"),
        "summary_sha256": sha(arm / "results.summary.json"), "attestation_sha256": sha(arm / "attestation.json"),
    }

if (run / "pre-hashes.json").read_bytes() != (run / "post-hashes.json").read_bytes():
    issues.append("pair pre/post hashes differ")
result = {
    "schema_version": 1, "accepted": not issues, "diagnostic_only": True, "traffic_kind": "tiny-smoke",
    "issues": issues, "certificate_der_sha256": certificate_fingerprint, "arms": arms,
    "pair_pre_post_hashes_equal": (run / "pre-hashes.json").read_bytes() == (run / "post-hashes.json").read_bytes(),
    "wrapper_status": int((run.parent / f"{run.name}.wrapper.status").read_text()),
    "final_status_sha256": sha(run / "final-status.json"),
}
output.write_text(json.dumps(result, indent=2, sort_keys=True) + "\n")
if issues:
    raise SystemExit("; ".join(issues))

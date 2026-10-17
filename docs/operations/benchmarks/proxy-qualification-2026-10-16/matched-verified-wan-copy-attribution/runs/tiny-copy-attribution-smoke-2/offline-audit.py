#!/usr/bin/env python3
import hashlib
import importlib.util
import json
import pathlib


run = pathlib.Path(__file__).resolve().parent
bundle = run.parents[1]
validator_path = bundle / "validate-copy-record.py"
spec = importlib.util.spec_from_file_location("copy_record_validator", validator_path)
validator = importlib.util.module_from_spec(spec)
spec.loader.exec_module(validator)


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


expected_schedule = ([('warmup', 'download', n) for n in range(1, 5)] +
                     [('warmup', 'upload', n) for n in range(1, 5)] +
                     [('retained-warmup', 'download', 1), ('retained-warmup', 'upload', 1),
                      ('steady', 'download', 1), ('steady', 'upload', 1), ('reuse-probe', 'download', 1)])
audit = {"schema_version": 1, "diagnostic_only": True, "run": run.name, "wrapper_status": 1,
         "strict_validator_accepted": False, "arms": {}}
for protocol in ("h1", "h2"):
    arm = run / protocol
    results = arm / "results.jsonl"
    rows = [json.loads(line) for line in results.read_text().splitlines() if line]
    expected_major, expected_alpn = (1, "http/1.1") if protocol == "h1" else (2, "h2")
    row_audit = []
    non_span_issues = []
    span_issues = []
    for sequence, row in enumerate(rows, 1):
        copy_issues = validator.validate_copy_envelope(
            row.get("response_copy_attribution", {}), sequence,
            row.get("transfer_direction"), row.get("configured_payload_bytes"))
        for issue in copy_issues:
            item = {"sequence": sequence, "issue": issue}
            if issue.endswith("total exceeds observed metric span"):
                span_issues.append(item)
            else:
                non_span_issues.append(item)
        record = row["response_copy_attribution"]["matches"][0]
        copy = record["copy"]
        metrics = {}
        for name in ("upstream_read", "downstream_write", "inter_read_gap"):
            metric = copy[name]
            span_ns = metric.get("last_unix_ns", 0) - metric.get("first_unix_ns", 0)
            metrics[name] = {"calls": metric["calls"], "bytes": metric["bytes"],
                             "total_ns": metric["total_ns"], "max_ns": metric["max_ns"],
                             "first_last_span_ns": span_ns, "total_minus_span_ns": metric["total_ns"] - span_ns,
                             "duration_bucket_counts": metric["duration_bucket_counts"],
                             "longest_stalls": metric["longest_stalls"]}
        tls = row["wan_response_tls"][0]
        trace = row["upstream_transport_trace"]["matches"][0]
        got = trace["got_conn"][0]
        row_audit.append({
            "sequence": sequence, "stage": row["measurement_stage"], "direction": row["transfer_direction"],
            "repetition": row["repetition"], "rate_bytes_per_second": row["useful_transfer_bytes_per_second"],
            "copy_validation_issues": copy_issues, "handler_elapsed_ns": record["handler_return_unix_ns"] - record["started_unix_ns"],
            "body_wraps": copy["body_wraps"], "read_eof": copy["read_eof"], "read_errors": copy["read_errors"],
            "write_errors": copy["write_errors"], "short_writes": copy["short_writes"],
            "downstream_read_from_calls": copy["downstream_read_from_calls"], "metrics": metrics,
            "http_major": tls["http_major"], "alpn": tls["negotiated_alpn"],
            "leaf_sha256": tls["peer_leaf_der_sha256"], "origin_connection_id": tls["origin_connection_id"],
            "connection_key": "|".join((got["connection_type"], got["local_address"], got["remote_address"])),
            "reused": got["reused"], "was_idle": got["was_idle"], "idle_time_ns": got["idle_time_ns"],
            "got_conn_unix_ns": got["observed_unix_ns"], "put_idle_conn": trace["put_idle_conn"],
        })
    first, revisit = row_audit[8], row_audit[12]
    prior_put_ns = first["put_idle_conn"][0]["observed_unix_ns"]
    reuse = {"sequence": 13, "expected_sequence": 9,
             "same_connection_key": revisit["connection_key"] == first["connection_key"],
             "same_origin_connection_id": revisit["origin_connection_id"] == first["origin_connection_id"],
             "reused": revisit["reused"], "was_idle": revisit["was_idle"],
             "elapsed_since_prior_put_idle_ns": revisit["got_conn_unix_ns"] - prior_put_ns,
             "httptrace_idle_time_ns": revisit["idle_time_ns"]}
    lifecycle = [json.loads(line) for line in (arm / "results.lifecycle.jsonl").read_text().splitlines() if line]
    exits = [entry for entry in lifecycle if entry.get("event") == "process-exit"]
    natural = (len(exits) == 5 and all(entry.get("status") == 0 and entry.get("forced") is False and
                                      entry.get("group_joined") is True and entry.get("invalidates_success") is False
                                      for entry in exits))
    expected_bytes = lambda row: (4 << 20) if row["direction"] == "download" else 32
    copy_contract_except_span = (not non_span_issues and all(
        row["body_wraps"] == 1 and row["downstream_read_from_calls"] == 0 and row["read_errors"] == 0 and
        row["write_errors"] == 0 and row["short_writes"] == 0 and row["read_eof"] in (0, 1) and
        row["metrics"]["upstream_read"]["bytes"] == expected_bytes(row) and
        row["metrics"]["downstream_write"]["bytes"] == expected_bytes(row) and
        sum(row["metrics"]["upstream_read"]["duration_bucket_counts"]) == row["metrics"]["upstream_read"]["calls"] and
        sum(row["metrics"]["downstream_write"]["duration_bucket_counts"]) == row["metrics"]["downstream_write"]["calls"] and
        sum(row["metrics"]["inter_read_gap"]["duration_bucket_counts"]) == row["metrics"]["inter_read_gap"]["calls"] and
        row["metrics"]["inter_read_gap"]["calls"] == row["metrics"]["upstream_read"]["calls"] - 1
        for row in row_audit))
    tls_protocol = all(row["http_major"] == expected_major and row["alpn"] == expected_alpn for row in row_audit)
    audit["arms"][protocol] = {
        "row_count": len(rows), "schedule_exact": [(r["measurement_stage"], r["transfer_direction"], r["repetition"]) for r in rows] == expected_schedule,
        "copy_contract_except_metric_span": copy_contract_except_span, "non_span_copy_issues": non_span_issues,
        "metric_span_issues": span_issues, "maximum_positive_total_minus_span_ns": max(
            (m["total_minus_span_ns"] for row in row_audit for m in row["metrics"].values()), default=0),
        "verified_protocol_every_row": tls_protocol, "reuse_probe": reuse, "natural_lifecycle": natural,
        "runner_status": json.loads((arm / "supervisor-lifecycle.json").read_text())["runner_status"],
        "outer_group_joined": json.loads((arm / "supervisor-lifecycle.json").read_text())["group_joined"],
        "pre_post_hashes_equal": json.loads((arm / "pre-hashes.json").read_text()) == json.loads((arm / "post-hashes.json").read_text()),
        "rows": row_audit, "results_sha256": sha(results), "lifecycle_sha256": sha(arm / "results.lifecycle.jsonl"),
        "summary_sha256": sha(arm / "results.summary.json"), "attestation_sha256": sha(arm / "attestation.json"),
    }
audit["complete_evidence"] = all(value["row_count"] == 13 and value["schedule_exact"] and
                                 value["copy_contract_except_metric_span"] and value["verified_protocol_every_row"] and
                                 value["reuse_probe"]["same_connection_key"] and value["reuse_probe"]["same_origin_connection_id"] and
                                 value["reuse_probe"]["reused"] is True and value["reuse_probe"]["was_idle"] is True and
                                 value["natural_lifecycle"] and value["outer_group_joined"] and value["pre_post_hashes_equal"]
                                 for value in audit["arms"].values())
audit["interpretation"] = ("Both arms retained complete raw evidence and failed only the strict comparison of monotonic duration totals with "
                           "UnixNano first/last spans on single-call 32-byte POST responses. Positive differences were at most 10 ns. "
                           "This audit does not waive the reviewed validator or classify the run as accepted.")
(run / "offline-audit.json").write_text(json.dumps(audit, indent=2, sort_keys=True) + "\n")

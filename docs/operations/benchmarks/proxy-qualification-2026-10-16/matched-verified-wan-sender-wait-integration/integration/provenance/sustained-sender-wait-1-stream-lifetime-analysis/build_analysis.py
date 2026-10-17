#!/usr/bin/env python3
import glob
import hashlib
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
RUN = ROOT / "integration-source/runs/sustained-sender-wait-1"
REVIEW = ROOT / "provenance/sustained-sender-wait-1-result-review"
CURRENT = ROOT.parents[0] / "wan-sender-wait-source/source"
LEGACY = ROOT.parents[0] / "current-wan-prep/legacy-source"
OUT = Path(__file__).resolve().parent
SELECTED = (9, 11, 14, 15)


def sha(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for block in iter(lambda: f.read(1024 * 1024), b""):
            h.update(block)
    return h.hexdigest()


def load_json(path):
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def rows(path):
    with open(path, encoding="utf-8") as f:
        return [json.loads(line) for line in f if line.strip()]


duration = load_json(REVIEW / "SENDER-DURATION-TABLE.json")
duration_by = {(x["protocol"], x["sequence"]): x for x in duration["records"]}
records = []
for protocol in ("h1", "h2"):
    arm = RUN / protocol
    attestation = load_json(arm / "attestation.json")
    reuse = {x["sequence"]: x for x in attestation["reuse_expectations"]}
    sender_rows = {x["sequence"]: x for x in rows(arm / "results.sender-wait-records.jsonl") if isinstance(x.get("sequence"), int)}
    raw_dir = next(arm.glob("proxy-agent-growth.*"))
    raw = {}
    for sequence in range(1, 17):
        matches = list(raw_dir.glob(f"{sequence}-*.json"))
        if len(matches) == 1:
            raw[sequence] = load_json(matches[0])
    for sequence in SELECTED:
        row = sender_rows[sequence]
        selected = row["sender_validation"]["selected"]
        timing = duration_by[(protocol, sequence)]
        lane = selected["lane"]
        stream_id = selected["stream_id"]
        post_events = row["post_sender"]["response"]["events"]
        matched_events = [e for e in post_events if e["lane"] == lane and e["stream_id"] == stream_id]
        assert len(matched_events) == 1
        event = matched_events[0]
        prior_agent_growth = []
        if protocol == "h2" and sequence == 14:
            # The seq10 bulk upload's agent-receiver events were drained by the
            # next selected pre-query at seq11. They are the opposite receive
            # direction from the seq14 download, but preserve its exact prior
            # publication history on this same stream.
            prior_agent_growth = [
                {
                    "sample_unix_nano": e["sample_unix_nano"],
                    "current": e["current"],
                    "proposed": e["proposed"],
                    "previous": e["previous"],
                    "rate_bytes_per_second": e["rate_bytes_per_second"],
                    "outcome": e["outcome"],
                }
                for e in sender_rows[11]["pre_sender"]["response"]["events"]
                if e["lane"] == 2 and e["stream_id"] == 6 and e["outcome"] == "grown"
            ]
        origin = raw[sequence]["wan_response_tls"][0]["origin_connection_id"]
        prior_sequence = {14: 10, 15: 11}.get(sequence)
        prior_origin = raw[prior_sequence]["wan_response_tls"][0]["origin_connection_id"] if prior_sequence else None
        reused = reuse[sequence].get("satisfied") if sequence in (14, 15) else False
        if sequence in (9, 11):
            lifetime = "fresh-stream-first-server-bulk-download"
        elif sequence == 14 and reused:
            lifetime = "same-stream-reused-after-opposite-direction-upload-first-server-bulk-download"
        elif sequence == 14:
            lifetime = "fresh-replacement-after-idle-expiry-first-server-bulk-download"
        else:
            lifetime = "same-stream-reused-after-prior-server-bulk-download"
        causes = timing["credit_wait_by_cause"]
        wakes = timing["credit_wait_by_wake"]
        records.append({
            "protocol": protocol,
            "sequence": sequence,
            "lane": lane,
            "stream_id": stream_id,
            "lifetime_class": lifetime,
            "origin_connection_id": origin,
            "prior_sequence": prior_sequence,
            "prior_origin_connection_id": prior_origin,
            "origin_reuse_satisfied": reused,
            "idle_age_ns": reuse[sequence].get("elapsed_idle_ns") if sequence in reuse else None,
            "wall_seconds": timing["wall_seconds"],
            "bytes_per_second": timing["useful_transfer_bytes_per_second"],
            "sender_credit_wait": {
                "total_ns": timing["durations"]["credit_wait_total"]["total_nanoseconds"],
                "byte_zero": causes["byte_zero"],
                "stream_allocation_zero": causes["stream_allocation_zero"],
                "session_allocation_zero": causes["session_allocation_zero"],
                "stream_and_session_allocation_zero": causes["stream_and_session_allocation_zero"],
                "byte_notify": wakes["byte_notify"],
                "aggregate_notify": wakes["aggregate_notify"],
            },
            "sender_data_path": {
                "data_call_ns": timing["durations"]["data_call_total"]["total_nanoseconds"],
                "physical_write_ns": timing["durations"]["physical_write_total"]["total_nanoseconds"],
                "frames": timing["frames"],
            },
            "agent_receive_growth_event": {
                "direction_warning": "This event is the agent receive side for the small download request, not the server receive side for the 256 MiB response.",
                "sample_unix_nano": event["sample_unix_nano"],
                "current": event["current"],
                "proposed": event["proposed"],
                "maximum": event["maximum"],
                "outcome": event["outcome"],
                "previous": event["previous"],
                "rate_bytes_per_second": event["rate_bytes_per_second"],
            },
            "agent_receive_prior_bulk_growth_publications": prior_agent_growth,
            "query_boundary_unix_ns": {
                "pre_request_finished": row["pre_sender"]["request_finished_unix_ns"],
                "post_request_started": row["post_sender"]["request_started_unix_ns"],
            },
        })

server_capacity_keys = sorted(sender_rows[9]["pre_server_capacity"].keys())
result = {
    "schema_version": 1,
    "scope": "closed-evidence read-only selected-stream lifetime and credit-boundary analysis",
    "records": records,
    "server_capacity_snapshot": {
        "keys": server_capacity_keys,
        "contains_per_stream_credit_or_growth": False,
        "raw_search_hits_for_credit_fields": 0,
    },
    "source_comparison": {
        "current_receive_window_growth_sha256": sha(CURRENT / "internal/tunnel/receive_window_growth.go"),
        "legacy_receive_window_growth_sha256": sha(LEGACY / "internal/tunnel/receive_window_growth.go"),
        "estimator_byte_identical": (CURRENT / "internal/tunnel/receive_window_growth.go").read_bytes() == (LEGACY / "internal/tunnel/receive_window_growth.go").read_bytes(),
        "current_growing_conn_sha256": sha(CURRENT / "internal/tunnel/growing_conn.go"),
        "legacy_growing_conn_sha256": sha(LEGACY / "internal/tunnel/growing_conn.go"),
        "aggregate_policy": "same estimator; current aggregate accepted target publishes session pool if needed, then stream allocation, then byte window; frozen legacy accepted target reserves legacy receive memory then publishes byte window",
    },
    "evidence": {
        "run_sender_records": {p: sha(RUN / p / "results.sender-wait-records.jsonl") for p in ("h1", "h2")},
        "run_attestations": {p: sha(RUN / p / "attestation.json") for p in ("h1", "h2")},
        "duration_table_sha256": sha(REVIEW / "SENDER-DURATION-TABLE.json"),
        "run_manifest_sha256": sha(REVIEW / "RUN-SHA256SUMS.txt"),
    },
    "limits": [
        "No server-side receiver growth-ring or per-stream credit snapshot was captured, so receiver target, advertised byte-credit evolution, and publication timestamps are unavailable.",
        "Agent receive growth events describe the opposite direction for downloads and cannot substitute for server receiver evidence.",
        "Wake outcome identifies the selected notification, not proof that the notification uniquely resolved the observed cause.",
    ],
}
(OUT / "STREAM-LIFETIME-CREDIT-ANALYSIS.json").write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")

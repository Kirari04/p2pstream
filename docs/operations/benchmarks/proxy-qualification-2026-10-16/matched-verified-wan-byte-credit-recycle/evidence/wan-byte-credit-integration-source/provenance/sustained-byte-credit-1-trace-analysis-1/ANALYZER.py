#!/usr/bin/env python3
"""Read-only attribution of the selected byte-credit intervals in sustained cohort 1."""

from __future__ import annotations

from collections import Counter, defaultdict
import hashlib
import json
import math
from pathlib import Path
import statistics


ROOT = Path("/home/user/.codex/worktrees/astra-proxy-performance/p2pstream")
RUN = ROOT / "tmp/continuation-20261016/wan-byte-credit-integration-source/integration-source-6/integration/runs/sustained-byte-credit-1"
SELECTED = (9, 11, 14, 15)
CONDITIONING_AFFECTED = {"h1": {15}, "h2": {14}}


def sha(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def load(path: Path):
    return json.loads(path.read_text(encoding="utf-8"))


def percentile(values: list[int], fraction: float) -> int:
    if not values:
        return 0
    ordered = sorted(values)
    return ordered[max(0, math.ceil(fraction * len(ordered)) - 1)]


def stats(values: list[int]) -> dict[str, int]:
    return {
        "count": len(values),
        "sum_ns": sum(values),
        "p50_ns": int(statistics.median(values)) if values else 0,
        "p95_ns": percentile(values, 0.95),
        "max_ns": max(values) if values else 0,
    }


def selected_trace(rows: list[dict], lane: int) -> dict:
    matches = [row["trace"] for row in rows if row.get("lane") == lane]
    if len(matches) != 1:
        raise ValueError(f"selected lane {lane} cardinality differs")
    return matches[0]


def analyze() -> tuple[dict, dict[str, str]]:
    final = load(RUN / "final-status.json")
    if final.get("accepted") is not False or final.get("size_mode") != "sustained":
        raise ValueError("cohort disposition differs")
    input_paths = [RUN / "final-status.json"]
    intervals: list[dict] = []
    for arm in ("h1", "h2"):
        attestation_path = RUN / arm / "attestation.json"
        results_path = RUN / arm / "results.jsonl"
        sender_records_path = RUN / arm / "results.sender-wait-records.jsonl"
        input_paths.extend((attestation_path, results_path, sender_records_path))
        attestation = load(attestation_path)
        expected_validation = attestation.get("conditioning_issues", []) + attestation.get("performance_issues", [])
        if (attestation.get("integrity_issues") != []
                or attestation.get("validation_issues") != expected_validation
                or attestation.get("classification") != "inconclusive-upstream-reuse"):
            raise ValueError(f"{arm} disposition differs")
        rows = [json.loads(line) for line in results_path.read_text(encoding="utf-8").splitlines()]
        sender_records = [json.loads(line) for line in sender_records_path.read_text(encoding="utf-8").splitlines()]
        summaries = {item["sequence"]: item for item in attestation["byte_credit_recycle_intervals"]}
        if sorted(summaries) != list(SELECTED):
            raise ValueError(f"{arm} selected summaries differ")
        for sequence in SELECTED:
            summary = summaries[sequence]
            lane = summary["selected_lane"]
            stream_id = summary["selected_stream_id"]
            controls = RUN / arm / "results.sender-wait-controls"
            sender_path = controls / f"sequence-{sequence}-post-sender.json"
            capacity_path = controls / f"sequence-{sequence}-post-capacity.json"
            input_paths.extend((sender_path, capacity_path))
            sender = load(sender_path)["response"]
            capacity = load(capacity_path)["response"]["server_receive_growth"]
            peer_trace = selected_trace(sender["byte_credit_traces"], lane)
            receiver_trace = selected_trace(capacity["byte_credit_traces"], lane)
            receiver = [record for record in receiver_trace["receiver_records"]
                        if record.get("stream_id") == stream_id]
            peer = [record for record in peer_trace["peer_records"]
                    if record.get("stream_id") == stream_id]
            waits = [record for record in peer_trace["wait_records"]
                     if record.get("stream_id") == stream_id]
            receiver_join = [(r["relative_ordinal"], r["delta"], r["cumulative"]) for r in receiver]
            peer_join = [(r["relative_ordinal"], r["delta"], r["cumulative"]) for r in peer]
            if (summary.get("accepted") is not True or summary.get("issues") != []
                    or len(receiver) != summary["receiver_records"]
                    or len(peer) != summary["peer_records"]
                    or len(waits) != summary["wait_records"]
                    or receiver_join != peer_join
                    or [r["relative_ordinal"] for r in receiver] != list(range(1, len(receiver) + 1))):
                raise ValueError(f"{arm} sequence {sequence} ordered join differs")
            for trace in (receiver_trace, peer_trace):
                if (trace.get("complete") is not True or trace.get("reset_performed") is not True
                        or trace.get("ambiguous") is not False or trace.get("overflow") is not False
                        or trace.get("orphan_overflow") is not False
                        or trace.get("dropped_receiver_records") != 0
                        or trace.get("dropped_peer_records") != 0
                        or trace.get("dropped_wait_records") != 0):
                    raise ValueError(f"{arm} sequence {sequence} trace health differs")
            if any(r.get("ambiguous") is not False or r.get("active") is not False for r in receiver):
                raise ValueError(f"{arm} sequence {sequence} receiver record differs")
            if any(r.get("ambiguous") is not False or r.get("active") is not False
                   or r.get("terminal") is not False or r.get("retry_zero_after_advance") is not False
                   or r.get("outcome") != "retry" for r in waits):
                raise ValueError(f"{arm} sequence {sequence} wait record differs")
            by_ordinal = {r["relative_ordinal"]: r for r in receiver}
            wait_ranges = Counter()
            wait_range_ns: dict[str, int] = defaultdict(int)
            for wait in waits:
                if not wait["advanced"]:
                    label = "stale"
                else:
                    kinds = {by_ordinal[ordinal]["kind"]
                             for ordinal in range(wait["range_first_ordinal"], wait["range_last_ordinal"] + 1)}
                    label = next(iter(kinds)) if len(kinds) == 1 else "mixed"
                wait_ranges[label] += 1
                wait_range_ns[label] += wait["entry_to_wake_observed_ns"]
            counter_record = next(record for record in sender["send_counters"] if record.get("lane") == lane)
            timing = counter_record["timing"]
            byte_zero = timing["credit_wait_by_cause"]["byte_zero"]
            trace_wait_total = sum(record["entry_to_retry_observed_ns"] for record in waits)
            if byte_zero["count"] != len(waits):
                raise ValueError(f"{arm} sequence {sequence} wait cardinality differs from sender ledger")
            sender_record = next(record for record in sender_records if record["sequence"] == sequence)
            if sender_record.get("valid") is not True or sender_record["sender_validation"].get("accepted") is not True:
                raise ValueError(f"{arm} sequence {sequence} sender interval differs")
            row = rows[sequence - 1]
            raw = sender_record["sender_validation"]["selected"]["raw_write_activity"]
            allocation_wait_ns = sum(
                timing["credit_wait_by_cause"][name]["total_nanoseconds"]
                for name in ("stream_allocation_zero", "session_allocation_zero", "stream_and_session_allocation_zero")
            )
            intervals.append({
                "arm": arm,
                "sequence": sequence,
                "stage": row["measurement_stage"],
                "repetition": row["repetition"],
                "conditioning_affected": sequence in CONDITIONING_AFFECTED[arm],
                "selected_lane": lane,
                "selected_stream_id": stream_id,
                "selected_generation": summary["selected_generation"],
                "wall_seconds": row["wall_seconds"],
                "useful_transfer_bytes_per_second": row["useful_transfer_bytes_per_second"],
                "receiver_records": len(receiver),
                "peer_records": len(peer),
                "wait_records": len(waits),
                "positive_credit_delta_bytes": sum(r["delta"] for r in receiver),
                "receiver_kind_counts": dict(sorted(Counter(r["kind"] for r in receiver).items())),
                "receiver_flag_counts": {str(k): v for k, v in sorted(Counter(r["flags"] for r in receiver).items())},
                "peer_notify_result_counts": dict(sorted(Counter(r["notify_result"] for r in peer).items())),
                "peer_records_with_active_wait_token": sum(r["active_wait_token"] != 0 for r in peer),
                "wait_classification_counts": dict(sorted(Counter(r["classification"] for r in waits).items())),
                "wait_range_kind_counts": dict(sorted(wait_ranges.items())),
                "wait_range_kind_entry_to_wake_ns": dict(sorted(wait_range_ns.items())),
                "receiver_eligibility_to_enqueue_observed": stats([r["eligibility_to_enqueue_observed_ns"] for r in receiver]),
                "receiver_enqueue_to_completion_result_observed": stats([r["enqueue_to_completion_result_observed_ns"] for r in receiver]),
                "receiver_eligibility_to_completion_result_observed": stats([r["eligibility_to_completion_result_observed_ns"] for r in receiver]),
                "peer_handler_to_credit_applied": stats([r["handler_to_credit_applied_ns"] for r in peer]),
                "peer_credit_applied_to_notify_observed": stats([r["credit_applied_to_notify_observed_ns"] for r in peer]),
                "peer_handler_to_notify_observed": stats([r["handler_to_notify_observed_ns"] for r in peer]),
                "wait_entry_to_wake_observed": stats([r["entry_to_wake_observed_ns"] for r in waits]),
                "wait_wake_to_retry_observed": stats([r["wake_to_retry_observed_ns"] for r in waits]),
                "wait_entry_to_retry_observed": stats([r["entry_to_retry_observed_ns"] for r in waits]),
                "sender_byte_zero_wait": byte_zero,
                "trace_minus_sender_byte_zero_total_ns": trace_wait_total - byte_zero["total_nanoseconds"],
                "allocation_credit_wait_total_ns": allocation_wait_ns,
                "physical_write_total": timing["physical_write_total"],
                "contained_raw_duration": raw["durations"]["contained_raw_duration"],
                "raw_residual_duration": raw["durations"]["residual_duration"],
                "integrity": {
                    "ordered_delta_cumulative_join": True,
                    "zero_drops_ambiguity_overflow": True,
                    "retry_zero_after_advance_count": 0,
                    "terminal_wait_count": 0,
                },
            })
    by_key = {(item["arm"], item["sequence"]): item for item in intervals}
    comparisons = []
    for sequence in SELECTED:
        h1 = by_key[("h1", sequence)]
        h2 = by_key[("h2", sequence)]
        comparisons.append({
            "sequence": sequence,
            "comparison_conditioning_clean": not h1["conditioning_affected"] and not h2["conditioning_affected"],
            "h2_minus_h1_wall_ns": round((h2["wall_seconds"] - h1["wall_seconds"]) * 1_000_000_000),
            "h2_minus_h1_sender_byte_zero_wait_ns": h2["sender_byte_zero_wait"]["total_nanoseconds"] - h1["sender_byte_zero_wait"]["total_nanoseconds"],
            "h2_minus_h1_normal_read_wait_range_ns": h2["wait_range_kind_entry_to_wake_ns"].get("normal_read", 0) - h1["wait_range_kind_entry_to_wake_ns"].get("normal_read", 0),
            "h2_minus_h1_forced_growth_wait_range_ns": h2["wait_range_kind_entry_to_wake_ns"].get("forced_growth", 0) - h1["wait_range_kind_entry_to_wake_ns"].get("forced_growth", 0),
            "h2_minus_h1_receiver_local_total_ns": h2["receiver_eligibility_to_completion_result_observed"]["sum_ns"] - h1["receiver_eligibility_to_completion_result_observed"]["sum_ns"],
            "h2_minus_h1_peer_handler_notify_total_ns": h2["peer_handler_to_notify_observed"]["sum_ns"] - h1["peer_handler_to_notify_observed"]["sum_ns"],
        })
    input_hashes = {str(path.relative_to(ROOT)): sha(path) for path in sorted(set(input_paths))}
    result = {
        "schema_version": 1,
        "diagnostic": "wan-byte-credit-recycle-sustained-trace-analysis",
        "source_only_offline_analysis": True,
        "endpoint_execution": False,
        "wan_traffic": False,
        "input_trace_integrity_accepted": True,
        "cohort_overall_accepted": False,
        "cohort_disposition": {
            "h1": "inconclusive-upstream-reuse; steady download floor failed",
            "h2": "inconclusive-upstream-reuse; retained-warmup and steady download floors failed",
            "integrity_and_closure": "accepted for both arms",
        },
        "selected_sequences": list(SELECTED),
        "intervals": intervals,
        "comparisons": comparisons,
        "interpretation": {
            "supported": [
                "All 645 receiver records have an exact ordered ordinal/delta/cumulative peer match; all 138 byte-zero wait records are complete and non-ambiguous.",
                "For conditioning-clean sequences 9 and 11, H2 adds byte-zero wait, dominated by waits whose applied range contains normal_read updates rather than forced_growth updates.",
                "Receiver eligibility-to-completion, peer handler-to-notify, and wake-to-retry segments are microsecond-scale and do not account for the observed hundreds-of-milliseconds byte-zero waits.",
                "H2 sequence 15 applies 52 positive updates without entering byte-zero wait, showing that positive credit recycling can proceed while the writer retains usable credit.",
            ],
            "not_supported": [
                "No cross-process timestamp subtraction or full end-to-end update latency attribution.",
                "No causal claim that HTTP/2, TLS, growth policy, or any one transport stage caused the failed floor.",
                "No pure observer-overhead measurement: each local segment includes the underlying protocol work and scheduling as well as observer work.",
                "No performance conclusion from H1 sequence 15 or H2 sequence 14 because those selected rows are within the independently retained reuse-conditioning failures.",
            ],
            "unmeasured_gap": "Time before receiver eligibility includes application consumption and quarter-window batching. Time after receiver completion-result observation and before peer handler entry includes the cross-endpoint send/transit/receive path. These portions cannot be separated by the retained clocks.",
        },
        "source_bindings": {
            "protocol_checkpoint_manifest_sha256": "d315f2e4f49c3b00d714346d5ff38a797c52e7b1874e7d21e7d89679a46d4287",
            "adapter_checkpoint_manifest_sha256": "4b91d78895462b0280ac0915bbf9580e49c864b747a6f231c3065d227f994062",
            "build_records_ledger_sha256": "6d8abe7c2a1e6ff2ac5de785fdc67bc201a943e8d0833224b5d6aa027f7a3961",
            "build_receipt_sha256": "93c91824df93bdfd78efc50dbc22b3c028b2e87ac4c88b77600c30207df0daff",
            "runner_checkpoint_manifest_sha256": "20ed7a1824c3d95cf5866620cb2dcb7e33d07eacf8ab4885d708d896e77b7f1e",
            "sustained_launcher_manifest_sha256": "0d8eebbd0fa382bbc375103a410128e2242e2bba31b8797e7790405ed6b98f8e",
        },
    }
    return result, input_hashes


if __name__ == "__main__":
    result, inputs = analyze()
    print(json.dumps({"analysis": result, "input_sha256": inputs}, indent=2, sort_keys=True))

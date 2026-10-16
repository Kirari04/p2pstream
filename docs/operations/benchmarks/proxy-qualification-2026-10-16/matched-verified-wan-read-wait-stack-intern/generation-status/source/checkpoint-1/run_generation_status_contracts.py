#!/usr/bin/env python3
"""Focused in-memory contracts for generation-status wait continuity."""
import importlib.util
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("generation_status_analyzer", ROOT / "analyze_interned_trace.py")
ANALYZER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ANALYZER)
GID = 7
MARKERS = {"handler_gid": GID, "region_begin": (100, 1), "region_end": (200, 99)}
WAIT_STACK = [
    "\tp2pstream/third_party/yamux.(*Stream).Read @ 0x1",
    "\t\t/source/third_party/yamux/stream.go:207",
]
WAIT_FRAMES = [{
    "function": "p2pstream/third_party/yamux.(*Stream).Read",
    "file": "/source/third_party/yamux/stream.go",
    "line": 207,
}]


def transition(time, order, old, new, reason="", stack=None, frames=None, header_g=GID):
    return {
        "kind": "StateTransition",
        "fields": f'GoID={GID} {old}->{new} Reason={json.dumps(reason)}'.encode(),
        "transition_stack": list(stack or []),
        "transition_frames": list(frames or []),
        "g": header_g,
        "time": time,
        "order": order,
    }


def reduce(events):
    original = ANALYZER.event_stream
    ANALYZER.event_stream = lambda unused: iter(events)
    try:
        return ANALYZER.reduce_states(Path("in-memory"), MARKERS)
    finally:
        ANALYZER.event_stream = original


def initial_running():
    return transition(90, 0, "Running", "Running")


def test_single_snapshot_continuity():
    value = reduce([
        initial_running(),
        transition(110, 2, "Running", "Waiting", "select", WAIT_STACK, WAIT_FRAMES),
        transition(130, 3, "Waiting", "Waiting", header_g=99),
        transition(140, 4, "Waiting", "Runnable", header_g=99),
        transition(150, 5, "Runnable", "Running"),
    ])
    assert value["state_ns"] == {"Running": 60, "Runnable": 10, "Syscall": 0, "Waiting": 30}
    assert value["partition_delta_ns"] == 0
    assert value["waiting_category_ns"] == {"waiting_yamux_empty_receive": 30}
    assert value["waiting_reason_ns"] == {"select": 30}
    assert value["waiting_status_snapshot_count"] == 1
    assert value["waiting_status_snapshot_carried_ns"] == 10
    assert value["handler_transition_count"] == 4
    assert value["missing_stack_ns"] == 0


def test_multiple_snapshots_preserve_one_active_wait():
    value = reduce([
        initial_running(),
        transition(110, 2, "Running", "Waiting", "select", WAIT_STACK, WAIT_FRAMES),
        transition(120, 3, "Waiting", "Waiting", header_g=98),
        transition(130, 4, "Waiting", "Waiting", header_g=97),
        transition(140, 5, "Waiting", "Runnable", header_g=96),
        transition(150, 6, "Runnable", "Running"),
    ])
    assert value["waiting_category_ns"] == {"waiting_yamux_empty_receive": 30}
    assert value["waiting_status_snapshot_count"] == 2
    assert value["waiting_status_snapshot_carried_ns"] == 20
    assert value["handler_transition_count"] == 5
    assert value["partition_sum_ns"] == value["region_duration_ns"] == 100


def test_snapshot_does_not_repair_ordinary_missing_stack():
    value = reduce([
        initial_running(),
        transition(110, 2, "Running", "Waiting"),
        transition(120, 3, "Waiting", "Waiting", header_g=98),
        transition(140, 4, "Waiting", "Runnable", header_g=97),
        transition(150, 5, "Runnable", "Running"),
    ])
    assert value["waiting_status_snapshot_count"] == 1
    assert value["waiting_status_snapshot_carried_ns"] == 20
    assert value["missing_stack_ns"] == 30
    assert value["attribution_complete"] is False


def test_inconsistent_snapshot_state_rejected():
    try:
        reduce([
            initial_running(),
            transition(110, 2, "Waiting", "Waiting", header_g=98),
        ])
    except ANALYZER.InvalidTrace as error:
        assert str(error) == "handler transition old state disagrees with partition state"
    else:
        raise AssertionError("inconsistent Waiting snapshot was accepted")


def main():
    cases = [
        test_single_snapshot_continuity,
        test_multiple_snapshots_preserve_one_active_wait,
        test_snapshot_does_not_repair_ordinary_missing_stack,
        test_inconsistent_snapshot_state_rejected,
    ]
    for case in cases:
        case()
    print(json.dumps({"accepted": True, "case_count": len(cases), "cases": [case.__name__ for case in cases]}, sort_keys=True))


if __name__ == "__main__":
    main()

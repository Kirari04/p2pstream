#!/usr/bin/env python3
"""Focused adapter-only lifecycle contracts for the frozen stack-intern adapter.

The production adapter is imported from an exact SHA-256-bound checkpoint and
is never edited. Test-only monkeypatches substitute tiny child processes and
short deadlines while preserving the adapter's real ownership and cleanup
logic.
"""

from __future__ import annotations

import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time


EXPECTED_ADAPTER_SHA256 = "719eac5dac37a0dc686283025e7005aba3cbf5408436b6257e6fde8811a50df8"
EXPECTED_CHECKPOINT_MANIFEST_SHA256 = "8a642d8cd435700f95d8016d8c526fcd178cd5197860228e5db12d275086e6b0"
CASES = (
    "natural_success_clean_environment",
    "signal_during_parser_spawn",
    "signal_during_canonicalizer_spawn",
    "term_ignoring_children_require_kill",
    "deadline_terminates_children",
    "second_child_startup_failure",
    "stderr_overflow_invalidates",
    "parser_nonzero_removes_partial",
    "raw_empty_rejected",
    "raw_oversize_rejected",
    "raw_nonregular_rejected",
    "tool_hash_mismatch_rejected",
    "unsafe_working_directory_rejected",
    "duplicate_output_rejected",
)
REAL_POPEN = subprocess.Popen


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def atomic_json(path: Path, value) -> None:
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
        json.dump(value, stream, indent=2, sort_keys=True)
        stream.write("\n")
        stream.flush()
        os.fsync(stream.fileno())


def fixture_child(role: str, behavior: str, output: str | None, ledger: str | None) -> int:
    if behavior == "ignore_term":
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        while True:
            time.sleep(1)
    if behavior == "sleep":
        time.sleep(30)
        return 0
    if role == "parser":
        if behavior == "stderr_overflow":
            sys.stderr.buffer.write(b"E" * 4096)
            sys.stderr.buffer.flush()
        sys.stdout.buffer.write(b"M=1 P=1 G=1 Sync Time=1\n")
        sys.stdout.buffer.flush()
        return 7 if behavior == "exit7" else 0
    if role != "canonicalizer" or output is None or ledger is None:
        raise SystemExit("invalid canonicalizer fixture arguments")
    sys.stdin.buffer.read()
    Path(output).write_bytes(b"adapter-contract-canonical\n")
    Path(ledger).write_bytes(b"{}\n")
    return 0


def group_absent(pgid: int | None) -> bool:
    if pgid is None:
        return True
    try:
        os.killpg(pgid, 0)
        return False
    except ProcessLookupError:
        return True
    except PermissionError:
        return False


def load_adapter(path: Path):
    spec = importlib.util.spec_from_file_location("frozen_stack_intern_adapter", path)
    if spec is None or spec.loader is None:
        raise AssertionError("could not construct adapter import spec")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def adapter_arguments(case_dir: Path, raw: Path, duplicate: bool = False) -> list[str]:
    parser_status = case_dir / "parser.status"
    canonicalizer_status = parser_status if duplicate else case_dir / "canonicalizer.status"
    return [
        "run_interned_trace_parser.py",
        "--raw-trace", str(raw),
        "--working-directory", str(case_dir),
        "--canonical-output", str(case_dir / "canonical.bin"),
        "--ledger", str(case_dir / "canonical.ledger.json"),
        "--parser-status", str(parser_status),
        "--canonicalizer-status", str(canonicalizer_status),
        "--parser-stderr", str(case_dir / "parser.stderr"),
        "--canonicalizer-stderr", str(case_dir / "canonicalizer.stderr"),
        "--process-record", str(case_dir / "process-record.json"),
    ]


def run_one(case: str, case_dir: Path, adapter_path: Path) -> dict:
    case_dir.mkdir(mode=0o700)
    raw = case_dir / "input.trace"
    raw.write_bytes(b"trace\x00fixture\n")
    adapter = load_adapter(adapter_path)
    adapter.NORMAL_DEADLINE_SECONDS = 0.20
    adapter.TERM_GRACE_SECONDS = 0.20
    adapter.KILL_GRACE_SECONDS = 0.50
    adapter.POLL_SECONDS = 0.005
    adapter.MAX_STDERR_BYTES = 1024
    inherited_marker = "ADAPTER_CONTRACT_MUST_NOT_INHERIT"
    os.environ[inherited_marker] = "secret-marker"
    captured_environments: list[dict[str, str]] = []
    popen_roles: list[str] = []
    second_publication_signal_sent = False

    original_popen = adapter.subprocess.Popen
    original_fresh_write = adapter.fresh_write

    def behavior_for(role: str) -> str:
        if case in {"signal_during_parser_spawn", "signal_during_canonicalizer_spawn",
                    "deadline_terminates_children", "second_child_startup_failure"}:
            return "sleep"
        if case == "term_ignoring_children_require_kill":
            return "ignore_term"
        if case == "stderr_overflow_invalidates" and role == "parser":
            return "stderr_overflow"
        if case == "parser_nonzero_removes_partial" and role == "parser":
            return "exit7"
        return "normal"

    def patched_popen(argv, **kwargs):
        role = "parser" if not popen_roles else "canonicalizer"
        popen_roles.append(role)
        captured_environments.append(dict(kwargs.get("env", {})))
        if case == "second_child_startup_failure" and role == "canonicalizer":
            raise OSError("synthetic second-child startup failure")
        behavior = behavior_for(role)
        command = [str(adapter.PINNED_PYTHON), str(Path(__file__).resolve()),
                   "--fixture-child", "--role", role, "--behavior", behavior]
        if role == "canonicalizer":
            output_index = argv.index("--output") + 1
            ledger_index = argv.index("--ledger") + 1
            command.extend(["--output", argv[output_index], "--ledger", argv[ledger_index]])
        process = REAL_POPEN(command, **kwargs)
        if behavior == "ignore_term":
            # Let the single-process fixture install SIG_IGN before the adapter's
            # intentionally short deadline initiates its production TERM/KILL path.
            time.sleep(0.05)
        if ((case == "signal_during_parser_spawn" and role == "parser") or
                (case == "signal_during_canonicalizer_spawn" and role == "canonicalizer")):
            os.kill(os.getpid(), signal.SIGTERM)
        return process

    def patched_fresh_write(path, payload):
        nonlocal second_publication_signal_sent
        if case in {"signal_during_parser_spawn", "signal_during_canonicalizer_spawn"} and not second_publication_signal_sent:
            second_publication_signal_sent = True
            os.kill(os.getpid(), signal.SIGTERM)
        return original_fresh_write(path, payload)

    expected_preflight = case in {
        "raw_empty_rejected", "raw_oversize_rejected", "raw_nonregular_rejected",
        "tool_hash_mismatch_rejected", "unsafe_working_directory_rejected",
        "duplicate_output_rejected",
    }
    if case == "raw_empty_rejected":
        raw.write_bytes(b"")
    elif case == "raw_oversize_rejected":
        adapter.MAX_RAW_TRACE_BYTES = 16
        raw.write_bytes(b"x" * 17)
    elif case == "raw_nonregular_rejected":
        raw.unlink()
        raw.mkdir()
    elif case == "tool_hash_mismatch_rejected":
        adapter.PINNED_GO_SHA256 = "0" * 64
    elif case == "unsafe_working_directory_rejected":
        case_dir.chmod(0o770)

    adapter.subprocess.Popen = patched_popen
    adapter.fresh_write = patched_fresh_write
    saved_argv = sys.argv
    sys.argv = adapter_arguments(case_dir, raw, duplicate=case == "duplicate_output_rejected")
    adapter_status = None
    raised = None
    try:
        adapter_status = adapter.main()
    except BaseException as error:
        raised = f"{type(error).__name__}: {error}"
    finally:
        sys.argv = saved_argv
        adapter.subprocess.Popen = original_popen
        adapter.fresh_write = original_fresh_write

    record_path = case_dir / "process-record.json"
    record = json.loads(record_path.read_text()) if record_path.exists() else None
    result = {
        "case": case,
        "adapter_sha256": sha256_file(adapter_path),
        "adapter_status": adapter_status,
        "raised": raised,
        "popen_roles": popen_roles,
        "captured_child_environments": captured_environments,
        "process_record": record,
        "files": sorted(path.name for path in case_dir.iterdir()),
    }

    if expected_preflight:
        if raised is None or popen_roles or record is not None:
            raise AssertionError(f"{case}: preflight did not reject before child ownership: {result}")
        if any((case_dir / name).exists() for name in (
            "canonical.bin", "canonical.ledger.json", "parser.status",
            "canonicalizer.status", "parser.stderr", "canonicalizer.stderr")):
            raise AssertionError(f"{case}: preflight created adapter outputs")
        result["contract_passed"] = True
        return result

    if raised is not None or adapter_status not in (0, 1) or record is None:
        raise AssertionError(f"{case}: adapter did not return with a process record: {result}")
    for child in ("parser", "canonicalizer"):
        child_record = record[child]
        if child_record["pid"] is not None and not group_absent(child_record["pgid"]):
            raise AssertionError(f"{case}: residual {child} group")
    for environment in captured_environments:
        if environment != adapter.CHILD_ENVIRONMENT or inherited_marker in environment:
            raise AssertionError(f"{case}: child environment was not exact")

    parser_status = (case_dir / "parser.status").read_text()
    canonicalizer_status = (case_dir / "canonicalizer.status").read_text()
    if case == "natural_success_clean_environment":
        assert adapter_status == 0 and record["accepted"] is True
        assert parser_status == "0\n" and canonicalizer_status == "0\n"
        assert (case_dir / "canonical.bin").exists() and (case_dir / "canonical.ledger.json").exists()
        assert all(record[name]["joined"] and record[name]["group_gone"] for name in ("parser", "canonicalizer"))
        assert not any(record[name]["term_attempted"] or record[name]["kill_attempted"] for name in ("parser", "canonicalizer"))
    elif case in {"signal_during_parser_spawn", "signal_during_canonicalizer_spawn"}:
        assert adapter_status == 1 and record["accepted"] is False
        assert record["interrupted_signal"] == signal.SIGTERM and second_publication_signal_sent
        assert all(record[name]["pid"] is not None and record[name]["joined"] and record[name]["group_gone"]
                   for name in ("parser", "canonicalizer"))
        assert not (case_dir / "canonical.bin").exists() and not (case_dir / "canonical.ledger.json").exists()
    elif case == "term_ignoring_children_require_kill":
        assert adapter_status == 1 and record["timed_out"] is True
        assert all(record[name]["term_attempted"] and record[name]["kill_attempted"]
                   and record[name]["status"] == -signal.SIGKILL and record[name]["joined"]
                   and record[name]["group_gone"] for name in ("parser", "canonicalizer"))
    elif case == "deadline_terminates_children":
        assert adapter_status == 1 and record["timed_out"] is True
        assert all(record[name]["term_attempted"] and record[name]["joined"] and record[name]["group_gone"]
                   for name in ("parser", "canonicalizer"))
    elif case == "second_child_startup_failure":
        assert adapter_status == 1 and "synthetic second-child startup failure" in record["startup_error"]
        assert record["parser"]["joined"] and record["parser"]["group_gone"]
        assert record["canonicalizer"]["pid"] is None and record["canonicalizer"]["status"] == 127
    elif case == "stderr_overflow_invalidates":
        assert adapter_status == 1 and record["parser"]["stderr_overflow"] is True
        assert (case_dir / "parser.stderr").stat().st_size == adapter.MAX_STDERR_BYTES
        assert parser_status == "0\n" and canonicalizer_status == "0\n"
        assert record["removed_invalid_artifacts"]["canonical_output"]["removed"] is True
        assert record["removed_invalid_artifacts"]["ledger"]["removed"] is True
    elif case == "parser_nonzero_removes_partial":
        assert adapter_status == 1 and parser_status == "7\n" and canonicalizer_status == "0\n"
        assert record["removed_invalid_artifacts"]["canonical_output"]["removed"] is True
        assert record["removed_invalid_artifacts"]["ledger"]["removed"] is True
        assert not (case_dir / "canonical.bin").exists() and not (case_dir / "canonical.ledger.json").exists()
    else:
        raise AssertionError(f"unvalidated case: {case}")
    result["contract_passed"] = True
    return result


def run_all(output: Path, adapter: Path, checkpoint_manifest: Path) -> int:
    if output.exists():
        raise SystemExit("contract output must be fresh")
    if sha256_file(adapter) != EXPECTED_ADAPTER_SHA256:
        raise SystemExit("production adapter SHA-256 mismatch")
    if sha256_file(checkpoint_manifest) != EXPECTED_CHECKPOINT_MANIFEST_SHA256:
        raise SystemExit("checkpoint manifest SHA-256 mismatch")
    output.mkdir(mode=0o700)
    results = []
    child_statuses = []
    for case in CASES:
        case_dir = output / case
        command = [sys.executable, str(Path(__file__).resolve()), "--case", case,
                   "--case-dir", str(case_dir), "--adapter", str(adapter)]
        invocation = output / f"{case}.invocation.json"
        atomic_json(invocation, {"argv": command})
        process = REAL_POPEN(command, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                             stderr=subprocess.PIPE, text=True)
        stdout_text, stderr_text = process.communicate(timeout=20)
        child_statuses.append({"case": case, "status": process.returncode})
        (output / f"{case}.stdout.log").write_text(stdout_text)
        (output / f"{case}.stderr.log").write_text(stderr_text)
        (output / f"{case}.status").write_text(f"{process.returncode}\n")
        if process.returncode != 0:
            results.append({
                "case": case,
                "contract_passed": False,
                "child_status": process.returncode,
                "result_error": "contract child returned nonzero",
            })
            continue
        # The case subprocess emits exactly one JSON document on success.
        try:
            case_result = json.loads(stdout_text)
        except json.JSONDecodeError as error:
            results.append({
                "case": case,
                "contract_passed": False,
                "child_status": process.returncode,
                "result_error": f"invalid result JSON: {error}",
            })
            continue
        case_result["child_status"] = process.returncode
        results.append(case_result)
    summary = {
        "schema_version": 1,
        "accepted": len(results) == len(CASES) and all(item.get("contract_passed") for item in results),
        "adapter": {"path": str(adapter), "sha256": sha256_file(adapter)},
        "checkpoint_manifest": {"path": str(checkpoint_manifest), "sha256": sha256_file(checkpoint_manifest)},
        "contract_source_sha256": sha256_file(Path(__file__)),
        "case_count": len(results),
        "child_statuses": child_statuses,
        "cases": results,
    }
    atomic_json(output / "summary.json", summary)
    return 0 if summary["accepted"] else 1


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--run-all", action="store_true")
    parser.add_argument("--case", choices=CASES)
    parser.add_argument("--case-dir", type=Path)
    parser.add_argument("--adapter", type=Path)
    parser.add_argument("--checkpoint-manifest", type=Path)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--fixture-child", action="store_true")
    parser.add_argument("--role", choices=("parser", "canonicalizer"))
    parser.add_argument("--behavior")
    parser.add_argument("--ledger")
    arguments = parser.parse_args()
    if arguments.fixture_child:
        return fixture_child(arguments.role, arguments.behavior, arguments.output, arguments.ledger)
    if arguments.case:
        result = run_one(arguments.case, arguments.case_dir, arguments.adapter)
        json.dump(result, sys.stdout, sort_keys=True)
        sys.stdout.write("\n")
        return 0
    if arguments.run_all:
        return run_all(arguments.output, arguments.adapter, arguments.checkpoint_manifest)
    parser.error("choose --run-all, --case, or --fixture-child")
    return 2


if __name__ == "__main__":
    raise SystemExit(main())

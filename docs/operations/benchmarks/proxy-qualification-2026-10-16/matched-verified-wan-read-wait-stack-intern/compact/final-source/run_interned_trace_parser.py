#!/usr/bin/env python3
"""Run and join the pinned parsed-trace producer and stack canonicalizer."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import selectors
import signal
import stat
import subprocess
import time


PINNED_GO = Path("/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin/go")
PINNED_GO_SHA256 = "29e6e0b8be61beb1489ceae62b304343566de8a1dc700af74bde7aeb9c80ad45"
PINNED_PYTHON = Path("/usr/bin/python3.14")
PINNED_PYTHON_SHA256 = "52e0a13e60a981d8c4b6478be2ba5176f69da07948a056bf49cf6f077e30cb41"
CANONICALIZER_SHA256 = "4d1bbcdd2140d148741775842e1ba9d2d74f2541b15933a6232b18cf3473e793"
FORMAT_MODULE_SHA256 = "f370243b753a94a122157e9b0b13eb535065fe88746269bd70de5475d82e4dbf"

MAX_RAW_TRACE_BYTES = 64 * 1024 * 1024
MAX_STDERR_BYTES = 1024 * 1024
NORMAL_DEADLINE_SECONDS = 600
TERM_GRACE_SECONDS = 5
KILL_GRACE_SECONDS = 5
POLL_SECONDS = 0.05

CHILD_ENVIRONMENT = {
    "GOENV": "off",
    "GOTOOLCHAIN": "local",
    "HOME": "/home/user",
    "LANG": "C.UTF-8",
    "LC_ALL": "C.UTF-8",
    "PATH": "/usr/bin:/bin",
    "PYTHONDONTWRITEBYTECODE": "1",
    "PYTHONHASHSEED": "0",
    "PYTHONNOUSERSITE": "1",
    "TZ": "Europe/Zurich",
}


class AdapterError(Exception):
    pass


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb", buffering=1024 * 1024) as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def file_identity(path: Path, maximum: int | None = None) -> dict:
    metadata = path.stat()
    if not stat.S_ISREG(metadata.st_mode):
        raise AdapterError(f"not a regular file: {path}")
    if maximum is not None and (metadata.st_size <= 0 or metadata.st_size > maximum):
        raise AdapterError(f"file size violates bound: {path}")
    return {
        "device": metadata.st_dev,
        "inode": metadata.st_ino,
        "size": metadata.st_size,
        "mtime_ns": metadata.st_mtime_ns,
        "ctime_ns": metadata.st_ctime_ns,
        "sha256": sha256_file(path),
    }


def fresh_write(path: Path, payload: bytes) -> None:
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "wb") as stream:
        stream.write(payload)
        stream.flush()
        os.fsync(stream.fileno())


def group_exists(pgid: int | None) -> bool:
    if pgid is None:
        return False
    try:
        os.killpg(pgid, 0)
        return True
    except ProcessLookupError:
        return False
    except PermissionError:
        return True


def signal_group(pgid: int | None, requested: signal.Signals) -> bool:
    if pgid is None or not group_exists(pgid):
        return False
    try:
        os.killpg(pgid, requested)
        return True
    except ProcessLookupError:
        return False


def drain_until(
    processes: list[subprocess.Popen],
    selector: selectors.BaseSelector,
    buffers: dict[int, bytearray],
    overflows: dict[int, bool],
    deadline: float,
    stop_requested=None,
) -> bool:
    while True:
        if stop_requested is not None and stop_requested():
            return False
        wait = min(POLL_SECONDS, max(0.0, deadline - time.monotonic()))
        for key, _ in selector.select(timeout=wait):
            try:
                chunk = os.read(key.fd, 64 * 1024)
            except BlockingIOError:
                continue
            if not chunk:
                selector.unregister(key.fileobj)
                key.fileobj.close()
                continue
            remaining = MAX_STDERR_BYTES - len(buffers[key.fd])
            if remaining > 0:
                buffers[key.fd].extend(chunk[:remaining])
            if len(chunk) > remaining:
                overflows[key.fd] = True
        complete = all(process.poll() is not None for process in processes)
        if complete and not selector.get_map():
            return True
        if time.monotonic() >= deadline:
            return False


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--raw-trace", required=True, type=Path)
    parser.add_argument("--working-directory", required=True, type=Path)
    parser.add_argument("--canonical-output", required=True, type=Path)
    parser.add_argument("--ledger", required=True, type=Path)
    parser.add_argument("--parser-status", required=True, type=Path)
    parser.add_argument("--canonicalizer-status", required=True, type=Path)
    parser.add_argument("--parser-stderr", required=True, type=Path)
    parser.add_argument("--canonicalizer-stderr", required=True, type=Path)
    parser.add_argument("--process-record", required=True, type=Path)
    arguments = parser.parse_args()

    script_directory = Path(__file__).resolve(strict=True).parent
    canonicalizer = (script_directory / "intern_parsed_trace.py").resolve(strict=True)
    format_module = (script_directory / "stack_intern_format.py").resolve(strict=True)
    go_binary = PINNED_GO.resolve(strict=True)
    python_binary = PINNED_PYTHON.resolve(strict=True)
    working_directory = arguments.working_directory.resolve(strict=True)
    working_metadata = working_directory.stat()
    if (not stat.S_ISDIR(working_metadata.st_mode) or working_metadata.st_uid != os.geteuid() or
            working_metadata.st_mode & 0o022):
        raise SystemExit("working directory must be an euid-owned non-group/world-writable directory")
    working_identity = {
        "device": working_metadata.st_dev,
        "inode": working_metadata.st_ino,
        "uid": working_metadata.st_uid,
        "mode": stat.S_IMODE(working_metadata.st_mode),
    }
    raw_trace = arguments.raw_trace.resolve(strict=True)
    raw_identity = file_identity(raw_trace, MAX_RAW_TRACE_BYTES)
    pinned_inputs = {
        "go": (go_binary, PINNED_GO_SHA256),
        "python": (python_binary, PINNED_PYTHON_SHA256),
        "canonicalizer": (canonicalizer, CANONICALIZER_SHA256),
        "format_module": (format_module, FORMAT_MODULE_SHA256),
    }
    for label, (path, expected_hash) in pinned_inputs.items():
        if sha256_file(path) != expected_hash:
            raise SystemExit(f"pinned {label} hash mismatch")

    output_names = {
        "canonical_output": arguments.canonical_output,
        "ledger": arguments.ledger,
        "parser_status": arguments.parser_status,
        "canonicalizer_status": arguments.canonicalizer_status,
        "parser_stderr": arguments.parser_stderr,
        "canonicalizer_stderr": arguments.canonicalizer_stderr,
        "process_record": arguments.process_record,
    }
    outputs = {name: path.resolve(strict=False) for name, path in output_names.items()}
    if len(set(outputs.values())) != len(outputs):
        raise SystemExit("adapter output paths are not distinct")
    input_paths = {raw_trace, go_binary, python_binary, canonicalizer, format_module}
    if set(outputs.values()) & input_paths:
        raise SystemExit("adapter output aliases an input")
    for name, path in outputs.items():
        if path.parent.resolve(strict=True) != working_directory or path.exists():
            raise SystemExit(f"adapter output is not a fresh direct child of working directory: {name}")

    parser_argv = [str(go_binary), "tool", "trace", "-d=parsed", str(raw_trace)]
    canonicalizer_argv = [
        str(python_binary), str(canonicalizer), "--input", "-",
        "--output", str(outputs["canonical_output"]),
        "--ledger", str(outputs["ledger"]),
    ]
    processes: list[subprocess.Popen] = []
    parser_process = None
    canonicalizer_process = None
    parser_status = 127
    canonicalizer_status = 127
    timed_out = False
    interrupted_signal = None
    startup_error = None
    term_attempted = {"parser": False, "canonicalizer": False}
    kill_attempted = {"parser": False, "canonicalizer": False}
    selector = selectors.DefaultSelector()
    buffers: dict[int, bytearray] = {}
    overflows: dict[int, bool] = {}

    def install_pipe(pipe) -> None:
        descriptor = pipe.fileno()
        os.set_blocking(descriptor, False)
        selector.register(pipe, selectors.EVENT_READ)
        buffers[descriptor] = bytearray()
        overflows[descriptor] = False

    def interrupted(signum, _frame):
        nonlocal interrupted_signal
        if interrupted_signal is None:
            interrupted_signal = signum

    previous_term = signal.signal(signal.SIGTERM, interrupted)
    previous_int = signal.signal(signal.SIGINT, interrupted)
    success = False
    try:
        parser_process = subprocess.Popen(
            parser_argv, cwd=working_directory, env=CHILD_ENVIRONMENT,
            stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            close_fds=True, start_new_session=True,
        )
        processes.append(parser_process)
        install_pipe(parser_process.stderr)
        try:
            canonicalizer_process = subprocess.Popen(
                canonicalizer_argv, cwd=working_directory, env=CHILD_ENVIRONMENT,
                stdin=parser_process.stdout, stdout=subprocess.DEVNULL,
                stderr=subprocess.PIPE, close_fds=True, start_new_session=True,
            )
            processes.append(canonicalizer_process)
            install_pipe(canonicalizer_process.stderr)
        finally:
            if parser_process.stdout is not None:
                parser_process.stdout.close()
        if not drain_until(processes, selector, buffers, overflows,
                           time.monotonic() + NORMAL_DEADLINE_SECONDS,
                           lambda: interrupted_signal is not None):
            if interrupted_signal is None:
                timed_out = True
                raise AdapterError("parser/canonicalizer exceeded fixed offline deadline")
            raise AdapterError(f"parser adapter received signal {interrupted_signal}")
        parser_status = parser_process.returncode
        canonicalizer_status = canonicalizer_process.returncode
        success = parser_status == 0 and canonicalizer_status == 0 and not any(overflows.values())
    except BaseException as error:
        startup_error = f"{type(error).__name__}: {error}"
    finally:
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        signal.signal(signal.SIGINT, signal.SIG_IGN)
        labels = (("parser", parser_process), ("canonicalizer", canonicalizer_process))
        groups_need_cleanup = not success or any(
            process is not None and group_exists(process.pid) for _, process in labels
        )
        if groups_need_cleanup:
            success = False
            for label, process in labels:
                term_attempted[label] = signal_group(None if process is None else process.pid, signal.SIGTERM)
            drain_until(processes, selector, buffers, overflows,
                        time.monotonic() + TERM_GRACE_SECONDS)
            for label, process in labels:
                kill_attempted[label] = signal_group(None if process is None else process.pid, signal.SIGKILL)
            drain_until(processes, selector, buffers, overflows,
                        time.monotonic() + KILL_GRACE_SECONDS)
        for process in processes:
            process.poll()
        if parser_process is not None and parser_process.returncode is not None:
            parser_status = parser_process.returncode
        if canonicalizer_process is not None and canonicalizer_process.returncode is not None:
            canonicalizer_status = canonicalizer_process.returncode
        for key in list(selector.get_map().values()):
            selector.unregister(key.fileobj)
            key.fileobj.close()
        selector.close()

    buffer_items = list(buffers.items())
    parser_stderr = bytes(buffer_items[0][1]) if buffer_items else b""
    canonicalizer_stderr = bytes(buffer_items[1][1]) if len(buffer_items) > 1 else b""
    parser_overflow = bool(buffer_items and overflows[buffer_items[0][0]])
    canonicalizer_overflow = bool(len(buffer_items) > 1 and overflows[buffer_items[1][0]])
    try:
        raw_post = file_identity(raw_trace, MAX_RAW_TRACE_BYTES)
        raw_stable = raw_post == raw_identity
    except (AdapterError, OSError) as error:
        raw_post = {"error": f"{type(error).__name__}: {error}"}
        raw_stable = False
    try:
        source_stable = all(
            sha256_file(path) == expected for path, expected in pinned_inputs.values()
        )
    except OSError:
        source_stable = False
    parser_group_gone = parser_process is not None and not group_exists(parser_process.pid)
    canonicalizer_group_gone = canonicalizer_process is not None and not group_exists(canonicalizer_process.pid)
    parser_joined = parser_process is not None and parser_process.returncode is not None
    canonicalizer_joined = canonicalizer_process is not None and canonicalizer_process.returncode is not None
    try:
        working_post_metadata = working_directory.stat()
        working_post_identity = {
            "device": working_post_metadata.st_dev,
            "inode": working_post_metadata.st_ino,
            "uid": working_post_metadata.st_uid,
            "mode": stat.S_IMODE(working_post_metadata.st_mode),
        }
        working_stable = working_post_identity == working_identity
    except OSError as error:
        working_post_identity = {"error": f"{type(error).__name__}: {error}"}
        working_stable = False
    artifact_identities = {}
    artifact_error = None
    if success:
        try:
            artifact_identities = {
                "canonical_output": file_identity(outputs["canonical_output"], 512 * 1024 * 1024),
                "ledger": file_identity(outputs["ledger"], 64 * 1024),
            }
        except (AdapterError, OSError) as error:
            artifact_error = f"{type(error).__name__}: {error}"
            success = False
    accepted = (
        success and raw_stable and source_stable and parser_joined and canonicalizer_joined and
        parser_group_gone and canonicalizer_group_gone and not parser_overflow and
        not canonicalizer_overflow and interrupted_signal is None and not timed_out and
        startup_error is None and artifact_error is None and working_stable
    )
    removed_invalid_artifacts = {}
    if not accepted:
        cleanup_safe = (
            (parser_process is None or (parser_joined and parser_group_gone)) and
            (canonicalizer_process is None or (canonicalizer_joined and canonicalizer_group_gone))
        )
        for name in ("canonical_output", "ledger"):
            path = outputs[name]
            if not path.exists():
                removed_invalid_artifacts[name] = {"existed": False, "removed": False}
                continue
            try:
                identity = file_identity(path)
            except (AdapterError, OSError) as error:
                identity = {"error": f"{type(error).__name__}: {error}"}
            removed = False
            removal_error = None
            if cleanup_safe:
                try:
                    path.unlink()
                    removed = True
                except OSError as error:
                    removal_error = f"{type(error).__name__}: {error}"
            else:
                removal_error = "removal skipped because an owned process group did not join"
            removed_invalid_artifacts[name] = {
                "existed": True,
                "identity": identity,
                "removed": removed,
                "removal_error": removal_error,
            }
    fresh_write(outputs["parser_stderr"], parser_stderr)
    fresh_write(outputs["canonicalizer_stderr"], canonicalizer_stderr)
    fresh_write(outputs["parser_status"], f"{parser_status}\n".encode())
    fresh_write(outputs["canonicalizer_status"], f"{canonicalizer_status}\n".encode())
    record = {
        "schema_version": 1,
        "accepted": accepted,
        "working_directory": str(working_directory),
        "working_directory_identity": working_identity,
        "working_directory_post_identity": working_post_identity,
        "working_directory_stable": working_stable,
        "environment": CHILD_ENVIRONMENT,
        "parser_argv": parser_argv,
        "canonicalizer_argv": canonicalizer_argv,
        "raw_trace": {"path": str(raw_trace), "pre": raw_identity, "post": raw_post, "stable": raw_stable},
        "pinned_inputs": {
            label: {"path": str(path), "sha256": expected}
            for label, (path, expected) in pinned_inputs.items()
        },
        "parser": {
            "pid": None if parser_process is None else parser_process.pid,
            "pgid": None if parser_process is None else parser_process.pid,
            "status": parser_status, "joined": parser_joined,
            "group_gone": parser_group_gone,
            "term_attempted": term_attempted["parser"],
            "kill_attempted": kill_attempted["parser"],
            "stderr_bytes": len(parser_stderr), "stderr_overflow": parser_overflow,
        },
        "canonicalizer": {
            "pid": None if canonicalizer_process is None else canonicalizer_process.pid,
            "pgid": None if canonicalizer_process is None else canonicalizer_process.pid,
            "status": canonicalizer_status, "joined": canonicalizer_joined,
            "group_gone": canonicalizer_group_gone,
            "term_attempted": term_attempted["canonicalizer"],
            "kill_attempted": kill_attempted["canonicalizer"],
            "stderr_bytes": len(canonicalizer_stderr),
            "stderr_overflow": canonicalizer_overflow,
        },
        "limits": {
            "max_raw_trace_bytes": MAX_RAW_TRACE_BYTES,
            "max_stderr_bytes": MAX_STDERR_BYTES,
            "normal_deadline_seconds": NORMAL_DEADLINE_SECONDS,
            "term_grace_seconds": TERM_GRACE_SECONDS,
            "kill_grace_seconds": KILL_GRACE_SECONDS,
        },
        "source_stable": source_stable, "timed_out": timed_out,
        "interrupted_signal": interrupted_signal, "startup_error": startup_error,
        "artifact_identities": artifact_identities,
        "artifact_error": artifact_error,
        "removed_invalid_artifacts": removed_invalid_artifacts,
    }
    fresh_write(outputs["process_record"],
                (json.dumps(record, indent=2, sort_keys=True) + "\n").encode())
    signal.signal(signal.SIGTERM, previous_term)
    signal.signal(signal.SIGINT, previous_int)
    return 0 if accepted else 1


if __name__ == "__main__":
    raise SystemExit(main())

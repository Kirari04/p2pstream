#!/usr/bin/env python3
"""Validate the immutable raw-write closure artifacts after the agent joined."""
from __future__ import annotations

import hashlib
import json
import os
import pathlib
import stat
from typing import Any

MAX_ARTIFACT_BYTES = 4 << 20


def _artifact(path: pathlib.Path, *, require_owned_0600: bool) -> tuple[dict[str, Any], Any]:
    metadata = path.lstat()
    if path.is_symlink() or not stat.S_ISREG(metadata.st_mode):
        raise ValueError(f"{path.name} is not a regular file")
    if require_owned_0600 and (metadata.st_uid != os.geteuid() or stat.S_IMODE(metadata.st_mode) != 0o600):
        raise ValueError(f"{path.name} is not an owned 0600 file")
    if metadata.st_size <= 0 or metadata.st_size > MAX_ARTIFACT_BYTES:
        raise ValueError(f"{path.name} violates the fixed artifact size bound")
    digest = hashlib.sha256()
    data = bytearray()
    with path.open("rb") as stream:
        while True:
            block = stream.read(1 << 20)
            if not block:
                break
            digest.update(block)
            data.extend(block)
            if len(data) > MAX_ARTIFACT_BYTES:
                raise ValueError(f"{path.name} changed beyond its fixed artifact size bound")
    if len(data) != metadata.st_size:
        raise ValueError(f"{path.name} changed while it was read")
    return ({"path": str(path), "size": len(data), "sha256": digest.hexdigest()}, json.loads(data))


def validate_post_join_raw_closure(control_directory: pathlib.Path, sender_wait_validator: Any) -> dict[str, Any]:
    control_directory = pathlib.Path(control_directory)
    terminal_path = control_directory / "final-raw-write-terminal.json"
    validation_path = control_directory / "final-raw-write-validation.json"
    record_path = control_directory / "final-raw-write-record.json"
    issues: list[str] = []
    record = terminal = saved_validation = independent_validation = {}
    terminal_artifact = validation_artifact = None
    try:
        terminal_artifact, terminal = _artifact(terminal_path, require_owned_0600=True)
        validation_artifact, saved_validation = _artifact(validation_path, require_owned_0600=False)
        _, record = _artifact(record_path, require_owned_0600=False)
        independent_validation = sender_wait_validator.validate_final_closure(terminal, control_directory)
        expected_record_fields = {
            "schema_version", "diagnostic", "agent_natural_join_status", "terminal_file_status",
            "validation_status", "terminal_artifact", "validation_artifact", "valid",
        }
        def exact_zero(value: Any) -> bool:
            return type(value) is int and value == 0
        accepted = bool(
            type(record) is dict and set(record) == expected_record_fields
            and type(record.get("schema_version")) is int and record.get("schema_version") == 1
            and record.get("diagnostic") == "raw-write-final-closure"
            and exact_zero(record.get("agent_natural_join_status"))
            and exact_zero(record.get("terminal_file_status"))
            and exact_zero(record.get("validation_status"))
            and record.get("valid") is True
            and record.get("terminal_artifact") == terminal_artifact
            and record.get("validation_artifact") == validation_artifact
            and saved_validation == independent_validation
            and type(independent_validation) is dict
            and independent_validation.get("accepted") is True
            and independent_validation.get("mode") == "final-closure"
        )
        if not accepted:
            issues.append("raw-write final closure record, artifacts, or independent validation differ")
    except (OSError, json.JSONDecodeError, TypeError, ValueError) as error:
        accepted = False
        issues.append(f"raw-write final closure artifacts are unreadable: {error}")
    return {
        "schema_version": 1,
        "accepted": accepted,
        "issues": issues,
        "record": record,
        "terminal": terminal,
        "saved_validation": saved_validation,
        "independent_validation": independent_validation,
        "terminal_artifact": terminal_artifact,
        "validation_artifact": validation_artifact,
    }

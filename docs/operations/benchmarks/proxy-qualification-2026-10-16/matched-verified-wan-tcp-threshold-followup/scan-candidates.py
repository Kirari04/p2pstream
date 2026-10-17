#!/usr/bin/env python3
"""Nonprinting exact-value scan of the reviewed loose follow-up evidence candidates."""
from __future__ import annotations

import csv
import hashlib
import json
from pathlib import Path

HERE = Path(__file__).resolve().parent
WORKTREE = HERE.parents[3]
INVENTORY = HERE / "PROSPECTIVE-INVENTORY.tsv"
ALLOWLIST = HERE / "SECRET-SCAN-INPUT-ALLOWLIST.tsv"
RESULT = HERE / "SECRET-SCAN.json"
BINDING = HERE / "SECRET-SCAN-BINDING.json"

CONFIG = json.loads((HERE / "SECRET-REFERENCE-RUNS.json").read_text())
RUNS = tuple(
    (run["name"], WORKTREE / run["root"], HERE / (run["name"].upper() + "-OMISSIONS.plan.tsv"))
    for run in CONFIG["runs"] if run["state"] != "pending"
)


def digest_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def digest_file(path: Path) -> str:
    value = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            value.update(block)
    return value.hexdigest()


def walk_private_strings(value, key=""):
    if isinstance(value, dict):
        for child_key, child in value.items():
            yield from walk_private_strings(child, str(child_key).lower())
    elif isinstance(value, list):
        for child in value:
            yield from walk_private_strings(child, key)
    elif isinstance(value, str) and len(value) >= 12:
        if any(marker in key for marker in ("token", "secret", "password", "bearer", "auth")):
            yield value.encode()


def main() -> None:
    with INVENTORY.open(newline="") as stream:
        candidates = list(csv.DictReader(stream, delimiter="\t"))
    if not candidates:
        raise SystemExit("empty candidate inventory")

    recovered: dict[str, bytes] = {}
    recovered_kind_counts: dict[str, int] = {}
    for run_name, run_root, omissions_path in RUNS:
        with omissions_path.open(newline="") as stream:
            omissions = list(csv.DictReader(stream, delimiter="\t"))
        for omission in omissions:
            path = run_root / omission["relative_path"]
            if path.is_symlink() or not path.is_file():
                raise SystemExit(f"invalid omitted input: {path}")
            data = path.read_bytes()
            if digest_bytes(data) != omission["sha256"]:
                raise SystemExit(f"omitted input hash changed: {path}")
            if omission["reason"] == "private key" and data:
                label = f"private-key:{run_name}:{omission['relative_path']}"
                recovered[label] = data
                recovered_kind_counts["private-key"] = recovered_kind_counts.get("private-key", 0) + 1
            if path.suffix == ".json":
                try:
                    value = json.loads(data)
                except Exception:
                    continue
                for index, secret in enumerate(walk_private_strings(value)):
                    label = f"json-private-value:{run_name}:{omission['relative_path']}:{index}"
                    recovered[label] = secret
                    recovered_kind_counts["json-private-value"] = recovered_kind_counts.get("json-private-value", 0) + 1

    allow_rows = []
    hits = []
    for candidate in candidates:
        path = WORKTREE / candidate["source"]
        if path.is_symlink() or not path.is_file():
            raise SystemExit(f"candidate changed type: {path}")
        if path.stat().st_size != int(candidate["bytes"]):
            raise SystemExit(f"candidate changed size: {path}")
        data = path.read_bytes()
        digest = digest_bytes(data)
        matched = sum(1 for secret in recovered.values() if secret and secret in data)
        if matched:
            hits.append({"artifact": candidate["source"], "recovered_label_count": matched})
        allow_rows.append({
            "sha256": digest,
            "bytes": candidate["bytes"],
            "placement": candidate["placement"],
            "category": candidate["category"],
            "destination": candidate["destination"],
            "source": candidate["source"],
        })

    fields = ("sha256", "bytes", "placement", "category", "destination", "source")
    with ALLOWLIST.open("w", newline="") as stream:
        writer = csv.DictWriter(stream, fieldnames=fields, delimiter="\t", lineterminator="\n")
        writer.writeheader()
        writer.writerows(allow_rows)

    result = {
        "schema_version": 1,
        "accepted": not hits,
        "candidate_files": len(candidates),
        "candidate_bytes": sum(int(row["bytes"]) for row in candidates),
        "recovered_value_labels": len(recovered),
        "recovered_value_kinds": recovered_kind_counts,
        "genuine_hit_artifacts": hits,
        "values_printed": 0,
        "limitations": [
            "The scan compares exact recoverable private-key bytes and authentication-like JSON values from omitted closed-run files.",
            "Values that were process-local and never persisted cannot be recovered for exact comparison.",
            "This exact-value scan does not prove absence of every possible secret representation.",
        ],
    }
    RESULT.write_text(json.dumps(result, indent=2, sort_keys=True) + "\n")

    binding_inputs = (
        "scan-candidates.py",
        "SECRET-REFERENCE-RUNS.json",
        "prepare-selection.py",
                "SELECTION-ROOTS.plan.tsv",
        "PROSPECTIVE-INVENTORY.tsv",
        "TINY1-OMISSIONS.plan.tsv",
        "TINY2-OMISSIONS.plan.tsv",
        "OMITTED-ROOTS.tsv",
        "ACCEPTED-BINDINGS.tsv",
        "SECRET-SCAN-INPUT-ALLOWLIST.tsv",
        "SECRET-SCAN.json",
    )
    binding = {
        "schema_version": 1,
        "accepted": not hits,
        "inputs": {name: digest_file(HERE / name) for name in binding_inputs},
        "run_ledgers": {
            run["name"]: run["ledger_sha256"] for run in CONFIG["runs"] if run["state"] != "pending"
        },
    }
    BINDING.write_text(json.dumps(binding, indent=2, sort_keys=True) + "\n")
    print(json.dumps({
        "accepted": not hits,
        "candidate_files": len(candidates),
        "candidate_bytes": result["candidate_bytes"],
        "recovered_value_labels": len(recovered),
        "genuine_hit_artifacts": len(hits),
        "values_printed": 0,
    }, sort_keys=True))
    if hits:
        raise SystemExit(1)


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""Nonprinting secret scan for the closed smoke2 candidate selection.

This script is prepared for later review. It does nothing without --execute.
It never prints recovered secret values or matching content.
"""

from __future__ import annotations

import argparse
import csv
import json
import re
from pathlib import Path

HERE = Path(__file__).resolve().parent
WORKTREE = HERE.parents[2]
RUN = WORKTREE / "tmp/continuation-20261016/wan-sender-wait-integration-source/integration-source/runs/small-sender-wait-smoke-2"
CATEGORY = "smoke2-run-candidate-pending-secret-scan"


def candidate_paths() -> list[tuple[str, Path]]:
    rows = csv.DictReader((HERE / "PROSPECTIVE-ALLOWLIST.tsv").open(), delimiter="\t")
    selected = []
    prefix = "integration-source/runs/small-sender-wait-smoke-2/"
    for row in rows:
        if row["category"] != CATEGORY:
            continue
        relative = row["source_relative_path"]
        if not relative.startswith(prefix):
            raise RuntimeError(f"candidate outside closed run: {relative}")
        run_relative = relative.removeprefix(prefix)
        selected.append((run_relative, RUN / run_relative))
    if not selected:
        raise RuntimeError("no smoke2 candidate rows")
    return selected


def recovered_values() -> list[tuple[str, bytes]]:
    recovered: list[tuple[str, bytes]] = []
    for ready in sorted(RUN.glob("*/proxy-agent-growth.*/*-server.ready.json")):
        data = json.loads(ready.read_text())
        agents = data.get("agents")
        if not isinstance(agents, list):
            raise RuntimeError(f"missing agents list: {ready.relative_to(RUN)}")
        for index, agent in enumerate(agents):
            token = agent.get("token") if isinstance(agent, dict) else None
            if not isinstance(token, str) or not token:
                raise RuntimeError(f"missing management token: {ready.relative_to(RUN)} agent {index}")
            recovered.append((f"management-token:{ready.relative_to(RUN)}:{index}", token.encode()))

    key = RUN / "tls/public.test.key.pem"
    key_bytes = key.read_bytes()
    if b"PRIVATE KEY" not in key_bytes:
        raise RuntimeError("private-key fixture does not contain a private-key marker")
    recovered.append(("tls-private-key:complete-pem", key_bytes))
    for index, line in enumerate(key_bytes.splitlines()):
        if len(line) >= 32 and b"PRIVATE KEY" not in line:
            recovered.append((f"tls-private-key:body-line:{index}", line))

    if not any(label.startswith("management-token:") for label, _ in recovered):
        raise RuntimeError("no recoverable management tokens")
    return recovered


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--execute", action="store_true", help="run the reviewed scan")
    parser.add_argument("--output", type=Path, help="write a JSON report")
    args = parser.parse_args()
    if not args.execute:
        parser.error("review only: pass --execute after explicit scan authorization")

    candidates = candidate_paths()
    recovered = recovered_values()
    exact_hits: dict[str, list[str]] = {label: [] for label, _ in recovered}
    patterns = {
        "authorization-bearer": re.compile(rb"(?i)authorization\s*[:=]\s*bearer\s+\S+"),
        "json-token-field": re.compile(rb'(?i)"(?:token|password|secret)"\s*:'),
        "private-key-marker": re.compile(rb"-----BEGIN [^-]*PRIVATE KEY-----"),
    }
    heuristic_hits: dict[str, list[str]] = {name: [] for name in patterns}

    for relative, path in candidates:
        if not path.is_file() or path.is_symlink():
            raise RuntimeError(f"candidate is not a regular file: {relative}")
        data = path.read_bytes()
        for label, value in recovered:
            if value and value in data:
                exact_hits[label].append(relative)
        for name, pattern in patterns.items():
            if pattern.search(data):
                heuristic_hits[name].append(relative)

    exact_hits = {label: paths for label, paths in exact_hits.items() if paths}
    heuristic_hits = {label: paths for label, paths in heuristic_hits.items() if paths}
    report = {
        "accepted": not exact_hits and not heuristic_hits,
        "candidate_files": len(candidates),
        "candidate_bytes": sum(path.stat().st_size for _, path in candidates),
        "exact_recovered_value_hits": exact_hits,
        "heuristic_hits": heuristic_hits,
        "limitations": [
            "The process-local trace bearer was not persisted, so exact comparison is unavailable.",
            "The scan covers only the closed smoke2 candidate category in the prospective map.",
        ],
        "secret_values_printed": False,
    }
    encoded = json.dumps(report, indent=2, sort_keys=True) + "\n"
    if args.output:
        args.output.write_text(encoded)
    else:
        print(encoded, end="")
    return 0 if report["accepted"] else 1


if __name__ == "__main__":
    raise SystemExit(main())

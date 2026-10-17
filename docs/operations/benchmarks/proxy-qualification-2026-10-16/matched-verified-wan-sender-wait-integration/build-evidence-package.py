#!/usr/bin/env python3
"""Validate or build the sender wait integration evidence package.

The default check is read-only. Building requires an explicit source root and
output root. The map is exact; this program discovers no evidence by walking
mutable roots. It validates the two frozen nonprinting scan records and their
exact candidate bindings; it never recovers or prints a secret value.
"""

from __future__ import annotations

import argparse
import csv
import gzip
import hashlib
import json
import os
import shutil
import stat
import tarfile
import tempfile
from pathlib import Path, PurePosixPath

HERE = Path(__file__).resolve().parent
ALLOWLIST = HERE / "ALLOWLIST.tsv"
PACKAGE_MAP = HERE / "PACKAGE-MAP.tsv"
BUNDLE_NAME = "ancillary-sender-wait-integration-evidence.tar.gz"
BUNDLE_HASH_NAME = "ANCILLARY-BUNDLE.sha256"
MAP_FIELDS = ("placement", "sha256", "bytes", "mode", "category", "destination", "source")
ALLOW_FIELDS = ("sha256", "category", "destination", "source")
METADATA = {
    "PACKAGE-README-DRAFT.md": "README.md",
    "ATTEMPT-HISTORY.md": "ATTEMPT-HISTORY.md",
    "EVIDENCE-GAPS.md": "EVIDENCE-GAPS.md",
    "SECURITY-SCANS.md": "SECURITY-SCANS.md",
    "EXCLUSIONS.tsv": "EXCLUSIONS.tsv",
    "EXCLUDED-RUN-ARTIFACTS.tsv": "EXCLUDED-RUN-ARTIFACTS.tsv",
    "SELECTION-SUMMARY.json": "SELECTION-SUMMARY.json",
    "SUSTAINED-CANDIDATE-INVENTORY.json": "SUSTAINED-CANDIDATE-INVENTORY.json",
    "SMOKE2-SECRET-SCAN.json": "SMOKE2-SECRET-SCAN.json",
    "SMOKE2-SECRET-SCAN-BINDING.json": "SMOKE2-SECRET-SCAN-BINDING.json",
    "SMOKE2-SECRET-SCAN-INPUT-ALLOWLIST.tsv": "SMOKE2-SECRET-SCAN-INPUT-ALLOWLIST.tsv",
    "scan_smoke2_candidates.py": "scan_smoke2_candidates.py",
    "SUSTAINED-SECRET-SCAN.json": "SUSTAINED-SECRET-SCAN.json",
    "SUSTAINED-SECRET-SCAN-BINDING.json": "SUSTAINED-SECRET-SCAN-BINDING.json",
    "SUSTAINED-SECRET-SCAN-INPUT-ALLOWLIST.tsv": "SUSTAINED-SECRET-SCAN-INPUT-ALLOWLIST.tsv",
    "scan_sustained_candidates.py": "scan_sustained_candidates.py",
    "build-evidence-package.py": "build-evidence-package.py",
    "verify-evidence-package.py": "verify-evidence-package.py",
}


def digest(path: Path) -> str:
    value = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            value.update(block)
    return value.hexdigest()


def safe_relative(value: str, label: str) -> PurePosixPath:
    path = PurePosixPath(value)
    if not value or path.is_absolute() or "." in path.parts or ".." in path.parts:
        raise SystemExit(f"unsafe {label}: {value!r}")
    if path.as_posix() != value:
        raise SystemExit(f"non-canonical {label}: {value!r}")
    return path


def regular_source(source_root: Path, value: str) -> Path:
    relative = safe_relative(value, "source")
    current = source_root
    for part in relative.parts:
        current = current / part
        try:
            mode = current.lstat().st_mode
        except FileNotFoundError as error:
            raise SystemExit(f"missing source: {current}") from error
        if stat.S_ISLNK(mode):
            raise SystemExit(f"symlink source component: {current}")
    if not current.is_file() or not stat.S_ISREG(current.stat().st_mode):
        raise SystemExit(f"source is not a regular file: {current}")
    return current


def content_guard(row: dict[str, str]) -> None:
    source = PurePosixPath(row["source"])
    lowered = source.as_posix().lower()
    forbidden = (
        ".key.pem",
        ".ready.json",
        "/.env",
        ".parsed.txt",
        ".wire.txt",
        "/__pycache__/",
        ".pyc",
        "/cache/",
        "/gopath/",
        "/tmp/",
        "/baseline/source/",
        "/candidate/source/",
        "/build/bin/",
        "/audited-build-attempt-1/bin/",
        "/proxy-agent-growth.",
    )
    if any(token in lowered for token in forbidden):
        raise SystemExit(f"forbidden selected artifact: {row['source']}")
    if source.name in {"source.tar", "source-8ac4c8d.tar"}:
        raise SystemExit(f"full source archive selected: {row['source']}")
    if source.suffix in {".a", ".test"}:
        raise SystemExit(f"compiled artifact selected: {row['source']}")


def load_rows(source_root: Path) -> list[dict[str, str]]:
    with ALLOWLIST.open(newline="") as stream:
        reader = csv.DictReader(stream, delimiter="\t")
        if tuple(reader.fieldnames or ()) != ALLOW_FIELDS:
            raise SystemExit("invalid allowlist header")
        allow = list(reader)
    with PACKAGE_MAP.open(newline="") as stream:
        reader = csv.DictReader(stream, delimiter="\t")
        if tuple(reader.fieldnames or ()) != MAP_FIELDS:
            raise SystemExit("invalid package-map header")
        rows = list(reader)
    expected = {(r["sha256"], r["category"], r["destination"], r["source"]) for r in allow}
    actual = {(r["sha256"], r["category"], r["destination"], r["source"]) for r in rows}
    if not rows or len(expected) != len(allow) or len(actual) != len(rows) or expected != actual:
        raise SystemExit("PACKAGE-MAP.tsv is not a one-to-one partition of ALLOWLIST.tsv")
    destinations: set[str] = set()
    sources: set[str] = set()
    for row in rows:
        if row["placement"] not in {"archive", "loose"}:
            raise SystemExit(f"invalid placement: {row['placement']}")
        safe_relative(row["destination"], "destination")
        safe_relative(row["source"], "source")
        if row["destination"] in destinations or row["source"] in sources:
            raise SystemExit(f"duplicate map record: {row['destination']}")
        destinations.add(row["destination"])
        sources.add(row["source"])
        if row["mode"] not in {"0644", "0755"}:
            raise SystemExit(f"invalid mode: {row['mode']}")
        if len(row["sha256"]) != 64 or any(c not in "0123456789abcdef" for c in row["sha256"]):
            raise SystemExit(f"invalid SHA-256: {row['sha256']}")
        content_guard(row)
        source = regular_source(source_root, row["source"])
        if source.stat().st_size != int(row["bytes"]) or digest(source) != row["sha256"]:
            raise SystemExit(f"source binding mismatch: {row['source']}")
    return sorted(rows, key=lambda row: row["destination"])


def validate_scan_record(prefix: str, category: str, rows: list[dict[str, str]]) -> None:
    report_path = HERE / f"{prefix}-SECRET-SCAN.json"
    binding_path = HERE / f"{prefix}-SECRET-SCAN-BINDING.json"
    report = json.loads(report_path.read_text())
    binding = json.loads(binding_path.read_text())
    input_path = HERE / binding["scan_input_allowlist_path"]
    if not report.get("accepted") or report.get("exact_recovered_value_hits") or report.get("heuristic_hits"):
        raise SystemExit(f"{prefix} scan did not pass cleanly")
    if digest(report_path) != binding["scan_report_sha256"]:
        raise SystemExit(f"{prefix} scan report binding mismatch")
    scanner_name = "scan_smoke2_candidates.py" if prefix == "SMOKE2" else "scan_sustained_candidates.py"
    if digest(HERE / scanner_name) != binding["scan_source_sha256"]:
        raise SystemExit(f"{prefix} scanner-source binding mismatch")
    input_hash = binding.get("scan_input_allowlist_sha256", binding.get("prospective_allowlist_sha256"))
    if digest(input_path) != input_hash:
        raise SystemExit(f"{prefix} scan input binding mismatch")
    with input_path.open(newline="") as stream:
        scanned = [row for row in csv.DictReader(stream, delimiter="\t") if row["category"] == category]
    current = [row for row in rows if row["category"] == category]
    left = sorted((row["destination"], row["bytes"], row["category"]) for row in scanned)
    right = sorted((row["destination"], row["bytes"], row["category"]) for row in current)
    if left != right or len(current) != report["candidate_files"]:
        raise SystemExit(f"{prefix} candidate set differs from scanned input")
    if sum(int(row["bytes"]) for row in current) != report["candidate_bytes"]:
        raise SystemExit(f"{prefix} candidate byte total differs from scanned input")


def copy_exact(source: Path, target: Path, mode: int) -> None:
    target.parent.mkdir(parents=True, exist_ok=True)
    if target.exists() or target.is_symlink():
        raise SystemExit(f"output collision: {target}")
    with source.open("rb") as incoming, target.open("xb") as outgoing:
        shutil.copyfileobj(incoming, outgoing, length=1024 * 1024)
    target.chmod(mode)


def build_bundle(rows: list[dict[str, str]], source_root: Path, output_root: Path) -> None:
    bundle = output_root / BUNDLE_NAME
    fd, temp_name = tempfile.mkstemp(prefix=f".{BUNDLE_NAME}.", dir=output_root)
    os.close(fd)
    temporary = Path(temp_name)
    try:
        with temporary.open("wb") as raw:
            with gzip.GzipFile(filename="", mode="wb", fileobj=raw, compresslevel=9, mtime=0) as compressed:
                with tarfile.open(fileobj=compressed, mode="w", format=tarfile.GNU_FORMAT) as archive:
                    for row in rows:
                        if row["placement"] != "archive":
                            continue
                        source = regular_source(source_root, row["source"])
                        info = tarfile.TarInfo(row["destination"])
                        info.size = int(row["bytes"])
                        info.mode = int(row["mode"], 8)
                        info.uid = info.gid = info.mtime = 0
                        info.uname = info.gname = ""
                        info.type = tarfile.REGTYPE
                        with source.open("rb") as content:
                            archive.addfile(info, content)
        os.replace(temporary, bundle)
        bundle.chmod(0o644)
    finally:
        temporary.unlink(missing_ok=True)
    (output_root / BUNDLE_HASH_NAME).write_text(f"{digest(bundle)}  {BUNDLE_NAME}\n")


def write_root_index(output_root: Path) -> None:
    index = output_root / "SHA256SUMS"
    lines = []
    for path in sorted(p for p in output_root.rglob("*") if p.is_file() and p != index):
        lines.append(f"{digest(path)}  {path.relative_to(output_root).as_posix()}\n")
    index.write_text("".join(lines))


def build(rows: list[dict[str, str]], source_root: Path, output_root: Path) -> None:
    if output_root.exists() and (output_root.is_symlink() or any(output_root.iterdir())):
        raise SystemExit("output root must be absent or empty")
    output_root.mkdir(parents=True, exist_ok=True)
    for row in rows:
        if row["placement"] == "loose":
            copy_exact(
                regular_source(source_root, row["source"]),
                output_root.joinpath(*PurePosixPath(row["destination"]).parts),
                int(row["mode"], 8),
            )
    build_bundle(rows, source_root, output_root)
    for source_name, target_name in METADATA.items():
        mode = 0o755 if source_name.endswith(".py") else 0o644
        copy_exact(HERE / source_name, output_root / target_name, mode)
    copy_exact(ALLOWLIST, output_root / "ALLOWLIST.tsv", 0o644)
    copy_exact(PACKAGE_MAP, output_root / "PACKAGE-MAP.tsv", 0o644)
    write_root_index(output_root)
    print(f"built package at {output_root}")


def main() -> None:
    parser = argparse.ArgumentParser()
    action = parser.add_mutually_exclusive_group(required=True)
    action.add_argument("--check-plan", action="store_true")
    action.add_argument("--build", action="store_true")
    parser.add_argument("--source-root", required=True, type=Path)
    parser.add_argument("--output-root", type=Path)
    args = parser.parse_args()
    source_root = args.source_root.resolve()
    if not source_root.is_dir():
        parser.error("--source-root must be an existing directory")
    rows = load_rows(source_root)
    validate_scan_record("SMOKE2", "smoke2-run-candidate-pending-secret-scan", rows)
    validate_scan_record("SUSTAINED", "sustained-run-candidate-pending-secret-scan", rows)
    print("validated frozen secret scans: smoke2=pass; sustained=pass; values printed=0")
    counts = {kind: sum(r["placement"] == kind for r in rows) for kind in ("archive", "loose")}
    sizes = {kind: sum(int(r["bytes"]) for r in rows if r["placement"] == kind) for kind in counts}
    print(
        f"validated plan: archive={counts['archive']} files/{sizes['archive']} bytes; "
        f"loose={counts['loose']} files/{sizes['loose']} bytes"
    )
    if args.build:
        if args.output_root is None:
            parser.error("--build requires --output-root")
        build(rows, source_root, args.output_root.resolve())
    elif args.output_root is not None:
        parser.error("--output-root is only valid with --build")


if __name__ == "__main__":
    main()

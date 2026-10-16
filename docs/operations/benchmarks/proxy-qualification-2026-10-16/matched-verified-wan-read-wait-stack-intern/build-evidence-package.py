#!/usr/bin/env python3
"""Validate or build the compact read-wait evidence package.

The default check is read-only. Building requires an explicit source root,
output root, and either recoverable secret values or an explicit declaration
that none are available. The map is exact; this program never discovers new
evidence by walking mutable run roots.
"""

from __future__ import annotations

import argparse
import csv
import gzip
import hashlib
import os
import shutil
import stat
import tarfile
import tempfile
from pathlib import Path, PurePosixPath

HERE = Path(__file__).resolve().parent
ALLOWLIST = HERE / "ALLOWLIST.tsv"
PACKAGE_MAP = HERE / "PACKAGE-MAP.tsv"
BUNDLE_NAME = "ancillary-source-contracts.tar.gz"
BUNDLE_HASH_NAME = "ANCILLARY-BUNDLE.sha256"
MAP_FIELDS = ("placement", "sha256", "bytes", "mode", "category", "destination", "source")
ALLOW_FIELDS = ("sha256", "category", "destination", "source")
METADATA = {
    "PACKAGE-README-DRAFT.md": "README.md",
    "PREDECESSOR-BINDINGS.tsv": "PREDECESSOR-BINDINGS.tsv",
    "EVIDENCE-GAPS.md": "EVIDENCE-GAPS.md",
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
        ".parsed.txt",
        ".wire.txt",
        "/__pycache__/",
        ".pyc",
    )
    if any(token in lowered for token in forbidden):
        raise SystemExit(f"forbidden selected artifact: {row['source']}")
    if source.suffix == ".out" and "raw-trace-safety-approved" not in row["category"]:
        raise SystemExit(f"raw trace lacks export-safety category: {row['source']}")
    if source.suffix == ".intern" and int(row["bytes"]) > 1024 * 1024:
        raise SystemExit(f"large canonical body selected: {row['source']}")


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


def load_secret_values(paths: list[Path]) -> list[bytes]:
    values: list[bytes] = []
    for path in paths:
        if path.is_symlink() or not path.is_file():
            raise SystemExit(f"invalid secret-value file: {path}")
        for value in path.read_bytes().splitlines():
            value = value.strip()
            if value and len(value) < 8:
                raise SystemExit(f"secret value is too short in {path}")
            if value:
                values.append(value)
    return list(dict.fromkeys(values))


def scan_secrets(rows: list[dict[str, str]], source_root: Path, values: list[bytes]) -> None:
    hits: list[str] = []
    for row in rows:
        data = regular_source(source_root, row["source"]).read_bytes()
        if any(value in data for value in values):
            hits.append(row["source"])
    if hits:
        raise SystemExit("recoverable secret found in selected artifact(s): " + ", ".join(hits))
    print(f"secret exact-value scan: values={len(values)}; hits=0")


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
    parser.add_argument("--secret-values-file", action="append", default=[], type=Path)
    parser.add_argument("--no-recoverable-secret-values", action="store_true")
    args = parser.parse_args()
    source_root = args.source_root.resolve()
    if not source_root.is_dir():
        parser.error("--source-root must be an existing directory")
    rows = load_rows(source_root)
    values = load_secret_values(args.secret_values_file)
    if args.build and not values and not args.no_recoverable_secret_values:
        parser.error("--build requires secret values or --no-recoverable-secret-values")
    scan_secrets(rows, source_root, values)
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

#!/usr/bin/env python3
"""Validate the package plan or build its deterministic ancillary archive.

The default operation is deliberately read-only. Pass --build and an output
root only after the evidence set is frozen and archive creation is authorized.
"""

from __future__ import annotations

import argparse
import csv
import gzip
import hashlib
import io
import os
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


def sha256(path: Path) -> str:
    value = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            value.update(block)
    return value.hexdigest()


def safe_name(value: str, label: str) -> PurePosixPath:
    path = PurePosixPath(value)
    if not value or path.is_absolute() or ".." in path.parts or "." in path.parts:
        raise SystemExit(f"unsafe {label}: {value!r}")
    if path.as_posix() != value:
        raise SystemExit(f"non-canonical {label}: {value!r}")
    return path


def source_path(value: str, source_root: Path) -> Path:
    relative = safe_name(value, "source")
    candidate = source_root.joinpath(*relative.parts)
    current = source_root
    for part in relative.parts:
        current = current / part
        try:
            mode = current.lstat().st_mode
        except FileNotFoundError as error:
            raise SystemExit(f"missing source: {candidate}") from error
        if stat.S_ISLNK(mode):
            raise SystemExit(f"symlink source component: {current}")
    if not candidate.is_file() or not stat.S_ISREG(candidate.stat().st_mode):
        raise SystemExit(f"source is not a regular file: {candidate}")
    return candidate


def load_plan(source_root: Path) -> list[dict[str, str]]:
    with ALLOWLIST.open(newline="") as stream:
        allow_reader = csv.DictReader(stream, delimiter="\t")
        if tuple(allow_reader.fieldnames or ()) != ALLOW_FIELDS:
            raise SystemExit("invalid allowlist header")
        allow = list(allow_reader)
    with PACKAGE_MAP.open(newline="") as stream:
        map_reader = csv.DictReader(stream, delimiter="\t")
        if tuple(map_reader.fieldnames or ()) != MAP_FIELDS:
            raise SystemExit("invalid package-map header")
        rows = list(map_reader)
    if not rows:
        raise SystemExit("empty package map")

    expected = {(r["sha256"], r["category"], r["destination"], r["source"]) for r in allow}
    actual = {(r["sha256"], r["category"], r["destination"], r["source"]) for r in rows}
    if len(expected) != len(allow) or len(actual) != len(rows) or actual != expected:
        raise SystemExit("package map is not a one-to-one partition of ALLOWLIST.tsv")

    destinations: set[str] = set()
    for row in rows:
        if row["placement"] not in {"archive", "loose"}:
            raise SystemExit(f"invalid placement: {row['placement']}")
        safe_name(row["destination"], "destination")
        if row["destination"] in destinations:
            raise SystemExit(f"duplicate destination: {row['destination']}")
        destinations.add(row["destination"])
        if row["mode"] not in {"0644", "0755"}:
            raise SystemExit(f"invalid mode: {row['mode']}")
        if len(row["sha256"]) != 64 or any(c not in "0123456789abcdef" for c in row["sha256"]):
            raise SystemExit(f"invalid SHA-256: {row['sha256']}")
        source = source_path(row["source"], source_root)
        if source.stat().st_size != int(row["bytes"]):
            raise SystemExit(f"size mismatch: {source}")
        if sha256(source) != row["sha256"]:
            raise SystemExit(f"hash mismatch: {source}")
    return sorted(rows, key=lambda row: row["destination"])


def build(rows: list[dict[str, str]], source_root: Path, output_root: Path) -> None:
    output_root.mkdir(parents=True, exist_ok=True)
    bundle = output_root / BUNDLE_NAME
    hash_file = output_root / BUNDLE_HASH_NAME
    for target in (bundle, hash_file):
        if target.exists() and (target.is_symlink() or not target.is_file()):
            raise SystemExit(f"unsafe output target: {target}")

    fd, temporary_name = tempfile.mkstemp(prefix=f".{BUNDLE_NAME}.", dir=output_root)
    os.close(fd)
    temporary = Path(temporary_name)
    try:
        with temporary.open("wb") as raw:
            with gzip.GzipFile(filename="", mode="wb", fileobj=raw, compresslevel=9, mtime=0) as compressed:
                with tarfile.open(fileobj=compressed, mode="w", format=tarfile.GNU_FORMAT) as archive:
                    for row in rows:
                        if row["placement"] != "archive":
                            continue
                        source = source_path(row["source"], source_root)
                        info = tarfile.TarInfo(row["destination"])
                        info.size = int(row["bytes"])
                        info.mode = int(row["mode"], 8)
                        info.uid = info.gid = 0
                        info.uname = info.gname = ""
                        info.mtime = 0
                        info.type = tarfile.REGTYPE
                        with source.open("rb") as content:
                            archive.addfile(info, content)
        os.replace(temporary, bundle)
        value = sha256(bundle)
        hash_file.write_text(f"{value}  {BUNDLE_NAME}\n")
    finally:
        temporary.unlink(missing_ok=True)
    print(f"built {bundle} ({bundle.stat().st_size} bytes)")
    print(f"wrote {hash_file}")


def main() -> None:
    parser = argparse.ArgumentParser()
    actions = parser.add_mutually_exclusive_group(required=True)
    actions.add_argument("--check-plan", action="store_true")
    actions.add_argument("--build", action="store_true")
    parser.add_argument(
        "--source-root",
        required=True,
        type=Path,
        help="root containing the ignored source corpus paths recorded in PACKAGE-MAP.tsv",
    )
    parser.add_argument("--output-root", type=Path)
    args = parser.parse_args()
    source_root = args.source_root.resolve()
    if not source_root.is_dir():
        parser.error("--source-root must be an existing directory")
    rows = load_plan(source_root)
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

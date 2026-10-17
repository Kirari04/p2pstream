#!/usr/bin/env python3
"""Independently verify loose evidence and every ancillary archive entry."""

from __future__ import annotations

import argparse
import csv
import hashlib
import re
import tarfile
from pathlib import Path, PurePosixPath

HERE = Path(__file__).resolve().parent
DEFAULT_MAP = HERE / "PACKAGE-MAP.tsv"
BUNDLE_NAME = "ancillary-source-contracts.tar.gz"
BUNDLE_HASH_NAME = "ANCILLARY-BUNDLE.sha256"
FIELDS = ("placement", "sha256", "bytes", "mode", "category", "destination", "source")
HEX64 = re.compile(r"^[0-9a-f]{64}$")


def digest_stream(stream) -> str:
    value = hashlib.sha256()
    for block in iter(lambda: stream.read(1024 * 1024), b""):
        value.update(block)
    return value.hexdigest()


def digest_path(path: Path) -> str:
    with path.open("rb") as stream:
        return digest_stream(stream)


def safe_name(value: str) -> PurePosixPath:
    path = PurePosixPath(value)
    if not value or path.is_absolute() or ".." in path.parts or "." in path.parts:
        raise SystemExit(f"unsafe destination: {value!r}")
    if path.as_posix() != value:
        raise SystemExit(f"non-canonical destination: {value!r}")
    return path


def load_map(path: Path) -> list[dict[str, str]]:
    with path.open(newline="") as stream:
        reader = csv.DictReader(stream, delimiter="\t")
        if tuple(reader.fieldnames or ()) != FIELDS:
            raise SystemExit("invalid package-map header")
        rows = list(reader)
    destinations: set[str] = set()
    for row in rows:
        safe_name(row["destination"])
        if row["destination"] in destinations:
            raise SystemExit(f"duplicate destination: {row['destination']}")
        destinations.add(row["destination"])
        if row["placement"] not in {"archive", "loose"}:
            raise SystemExit(f"invalid placement: {row['placement']}")
        if row["mode"] not in {"0644", "0755"} or not HEX64.fullmatch(row["sha256"]):
            raise SystemExit(f"invalid map record: {row['destination']}")
        if int(row["bytes"]) < 0:
            raise SystemExit(f"invalid size: {row['destination']}")
    if not rows:
        raise SystemExit("empty package map")
    return rows


def verify_bundle_hash(root: Path) -> Path:
    bundle = root / BUNDLE_NAME
    hash_path = root / BUNDLE_HASH_NAME
    words = hash_path.read_text().split()
    if len(words) != 2 or not HEX64.fullmatch(words[0]) or words[1] != BUNDLE_NAME:
        raise SystemExit(f"invalid {BUNDLE_HASH_NAME}")
    if digest_path(bundle) != words[0]:
        raise SystemExit("ancillary bundle SHA-256 mismatch")
    return bundle


def verify(root: Path, rows: list[dict[str, str]]) -> None:
    loose = {r["destination"]: r for r in rows if r["placement"] == "loose"}
    archived = {r["destination"]: r for r in rows if r["placement"] == "archive"}
    for name, row in loose.items():
        path = root.joinpath(*PurePosixPath(name).parts)
        if path.is_symlink() or not path.is_file():
            raise SystemExit(f"missing or non-regular loose artifact: {name}")
        if path.stat().st_size != int(row["bytes"]) or digest_path(path) != row["sha256"]:
            raise SystemExit(f"loose artifact mismatch: {name}")
        if (path.stat().st_mode & 0o777) != int(row["mode"], 8):
            raise SystemExit(f"loose artifact mode mismatch: {name}")

    bundle = verify_bundle_hash(root)
    seen: set[str] = set()
    with tarfile.open(bundle, mode="r:gz") as archive:
        for member in archive:
            safe_name(member.name)
            if member.name in seen:
                raise SystemExit(f"duplicate archive member: {member.name}")
            seen.add(member.name)
            row = archived.get(member.name)
            if row is None:
                raise SystemExit(f"unexpected archive member: {member.name}")
            if not member.isfile() or member.issym() or member.islnk():
                raise SystemExit(f"non-regular archive member: {member.name}")
            if any((member.uid, member.gid, member.mtime)) or member.uname or member.gname:
                raise SystemExit(f"non-deterministic archive metadata: {member.name}")
            if member.mode != int(row["mode"], 8) or member.size != int(row["bytes"]):
                raise SystemExit(f"archive metadata mismatch: {member.name}")
            stream = archive.extractfile(member)
            if stream is None or digest_stream(stream) != row["sha256"]:
                raise SystemExit(f"archive entry mismatch: {member.name}")
    missing = set(archived) - seen
    if missing:
        raise SystemExit(f"missing archive members: {len(missing)}")
    print(f"verified {len(archived)} archive entries and {len(loose)} loose artifacts")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--map", type=Path, default=DEFAULT_MAP)
    parser.add_argument("--package-root", type=Path, default=HERE)
    parser.add_argument("--check-plan", action="store_true")
    args = parser.parse_args()
    rows = load_map(args.map)
    archive_count = sum(r["placement"] == "archive" for r in rows)
    loose_count = len(rows) - archive_count
    if args.check_plan:
        print(f"valid package map: archive={archive_count}; loose={loose_count}")
        return
    verify(args.package_root.resolve(), rows)


if __name__ == "__main__":
    main()

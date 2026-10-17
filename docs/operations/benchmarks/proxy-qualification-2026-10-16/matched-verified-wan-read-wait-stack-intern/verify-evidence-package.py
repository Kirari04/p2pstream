#!/usr/bin/env python3
"""Verify every loose and archived artifact in a built evidence package."""

from __future__ import annotations

import argparse
import csv
import hashlib
import re
import tarfile
from pathlib import Path, PurePosixPath

BUNDLE_NAME = "ancillary-source-contracts.tar.gz"
BUNDLE_HASH_NAME = "ANCILLARY-BUNDLE.sha256"
FIELDS = ("placement", "sha256", "bytes", "mode", "category", "destination", "source")
HEX64 = re.compile(r"^[0-9a-f]{64}$")


def digest_stream(stream) -> str:
    value = hashlib.sha256()
    for block in iter(lambda: stream.read(1024 * 1024), b""):
        value.update(block)
    return value.hexdigest()


def digest(path: Path) -> str:
    with path.open("rb") as stream:
        return digest_stream(stream)


def safe_name(value: str) -> PurePosixPath:
    path = PurePosixPath(value)
    if not value or path.is_absolute() or "." in path.parts or ".." in path.parts or path.as_posix() != value:
        raise SystemExit(f"unsafe package path: {value!r}")
    return path


def load_map(path: Path) -> list[dict[str, str]]:
    with path.open(newline="") as stream:
        reader = csv.DictReader(stream, delimiter="\t")
        if tuple(reader.fieldnames or ()) != FIELDS:
            raise SystemExit("invalid package-map header")
        rows = list(reader)
    seen: set[str] = set()
    for row in rows:
        safe_name(row["destination"])
        if row["destination"] in seen:
            raise SystemExit(f"duplicate destination: {row['destination']}")
        seen.add(row["destination"])
        if row["placement"] not in {"archive", "loose"} or row["mode"] not in {"0644", "0755"}:
            raise SystemExit(f"invalid map record: {row['destination']}")
        if not HEX64.fullmatch(row["sha256"]) or int(row["bytes"]) < 0:
            raise SystemExit(f"invalid map binding: {row['destination']}")
    if not rows:
        raise SystemExit("empty package map")
    return rows


def verify_root_index(root: Path) -> None:
    index = root / "SHA256SUMS"
    expected: dict[str, str] = {}
    for line in index.read_text().splitlines():
        value, marker, name = line.partition("  ")
        if marker != "  " or not HEX64.fullmatch(value):
            raise SystemExit("invalid root checksum record")
        safe_name(name)
        if name in expected:
            raise SystemExit(f"duplicate root checksum record: {name}")
        expected[name] = value
    actual = {p.relative_to(root).as_posix() for p in root.rglob("*") if p.is_file() and p != index}
    if actual != set(expected):
        raise SystemExit(f"root checksum coverage mismatch: expected={len(expected)} actual={len(actual)}")
    for name, value in expected.items():
        path = root.joinpath(*PurePosixPath(name).parts)
        if path.is_symlink() or digest(path) != value:
            raise SystemExit(f"root checksum mismatch: {name}")


def verify(root: Path, rows: list[dict[str, str]]) -> None:
    loose = {r["destination"]: r for r in rows if r["placement"] == "loose"}
    archived = {r["destination"]: r for r in rows if r["placement"] == "archive"}
    for name, row in loose.items():
        path = root.joinpath(*PurePosixPath(name).parts)
        if path.is_symlink() or not path.is_file():
            raise SystemExit(f"missing loose artifact: {name}")
        if path.stat().st_size != int(row["bytes"]) or digest(path) != row["sha256"]:
            raise SystemExit(f"loose artifact mismatch: {name}")
        if (path.stat().st_mode & 0o777) != int(row["mode"], 8):
            raise SystemExit(f"loose mode mismatch: {name}")
    words = (root / BUNDLE_HASH_NAME).read_text().split()
    if len(words) != 2 or not HEX64.fullmatch(words[0]) or words[1] != BUNDLE_NAME:
        raise SystemExit("invalid ancillary bundle hash record")
    bundle = root / BUNDLE_NAME
    if digest(bundle) != words[0]:
        raise SystemExit("ancillary bundle hash mismatch")
    seen: set[str] = set()
    with tarfile.open(bundle, mode="r:gz") as archive:
        for member in archive:
            safe_name(member.name)
            if member.name in seen or member.name not in archived:
                raise SystemExit(f"unexpected/duplicate archive member: {member.name}")
            seen.add(member.name)
            row = archived[member.name]
            if not member.isfile() or member.issym() or member.islnk():
                raise SystemExit(f"non-regular archive member: {member.name}")
            if any((member.uid, member.gid, member.mtime)) or member.uname or member.gname:
                raise SystemExit(f"non-deterministic archive metadata: {member.name}")
            if member.mode != int(row["mode"], 8) or member.size != int(row["bytes"]):
                raise SystemExit(f"archive metadata mismatch: {member.name}")
            stream = archive.extractfile(member)
            if stream is None or digest_stream(stream) != row["sha256"]:
                raise SystemExit(f"archive content mismatch: {member.name}")
    if seen != set(archived):
        raise SystemExit(f"missing archive members: {len(set(archived) - seen)}")
    verify_root_index(root)
    print(f"verified {len(archived)} archive entries and {len(loose)} loose artifacts")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--package-root", required=True, type=Path)
    parser.add_argument("--check-plan", action="store_true")
    args = parser.parse_args()
    root = args.package_root.resolve()
    rows = load_map(root / "PACKAGE-MAP.tsv")
    if args.check_plan:
        print(f"valid package map: archive={sum(r['placement']=='archive' for r in rows)}; loose={sum(r['placement']=='loose' for r in rows)}")
        return
    verify(root, rows)


if __name__ == "__main__":
    main()

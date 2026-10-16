#!/usr/bin/env python3
"""Validate the prospective, non-exporting WAN read-wait allowlist."""

from __future__ import annotations

import argparse
import csv
import hashlib
import json
import re
from pathlib import Path, PurePosixPath


LIST = Path(__file__).with_name("ALLOWLIST.tsv")
SOURCE_ROOT_PATHS = (
    "tmp/continuation-20261016/wan-read-wait-source",
    "tmp/continuation-20261016/wan-read-wait-capacity-source",
    "tmp/continuation-20261016/wan-read-wait-zero-pc-analyzer-source",
)

ALLOWED_BUILD_SOURCES = {
    "build/source/internal/server/proxy_qualification_test.go",
    "build/source/internal/server/proxy_qualification_wan_read_wait_trace_contract_test.go",
    "build/source/internal/server/proxy_qualification_wan_read_wait_trace_test.go",
}

ALLOWED_TRACE_SOURCES = {
    "analyzer/runs/real-parser-conformance-1/control.trace",
}

FORBIDDEN_PARTS = {
    "bearer",
    "credential",
    "credentials",
    "private-key",
    "private_key",
    "readiness",
}

FORBIDDEN_SUFFIXES = {".key", ".db", ".sqlite", ".sqlite3", ".trace"}

FORBIDDEN_NAME_FRAGMENTS = {"bearer", "credential", "private-key", "private_key", ".ready"}

FORBIDDEN_CONTENT = (
    re.compile(rb"P2PSTREAM_QUALIFICATION_WAN_READ_WAIT_TRACE_BEARER\s*=\s*[0-9a-f]{64}"),
    re.compile(rb"Authorization:\s*Bearer\s+[0-9a-f]{64}", re.IGNORECASE),
    re.compile(rb"-----BEGIN (?:RSA |EC |OPENSSH |DSA |ENCRYPTED )?PRIVATE KEY-----"),
)

CLOSED_SECRET_RUN_PATHS = (
    "tmp/continuation-20261016/wan-read-wait-capacity-source/runs/tiny-read-wait-capacity-smoke-1",
    "tmp/continuation-20261016/wan-read-wait-capacity-source/runs/tiny-read-wait-capacity-smoke-3",
    "tmp/continuation-20261016/wan-read-wait-capacity-source/runs/tiny-read-wait-capacity-smoke3-h2-only-1",
)

TRACE_BEARER_PATTERNS = (
    re.compile(
        rb"P2PSTREAM_QUALIFICATION_WAN_READ_WAIT_TRACE_BEARER\s*=\s*[\"']?([0-9a-f]{64})",
        re.IGNORECASE,
    ),
    re.compile(rb"Authorization:\s*Bearer\s+([0-9a-f]{64})", re.IGNORECASE),
)


def digest(path: Path) -> str:
    value = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            value.update(block)
    return value.hexdigest()


def exact_closed_secret_screen(source_root_base: Path, sources: list[Path]) -> None:
    """Compare selected bytes with exact secrets from excluded closed-run files.

    Public route IDs are deliberately not collected. Only explicitly labelled
    trace bearers and authentication-token fields qualify as secrets here.
    """

    secrets: dict[str, set[bytes]] = {
        "trace-bearer": set(),
        "readiness-auth": set(),
        "readiness-value": set(),
    }
    for relative in CLOSED_SECRET_RUN_PATHS:
        run = source_root_base / relative
        if not run.is_dir():
            raise SystemExit(f"missing closed secret-source run: {run}")
        for path in run.rglob("*"):
            if not path.is_file():
                continue
            content = path.read_bytes()
            for pattern in TRACE_BEARER_PATTERNS:
                secrets["trace-bearer"].update(pattern.findall(content))
        for path in run.rglob("*.ready.json"):
            value = json.loads(path.read_text())

            def collect(item: object) -> None:
                if isinstance(item, dict):
                    for key, child in item.items():
                        if key.lower() in {"token", "auth_token", "authentication_token"}:
                            if isinstance(child, str) and child:
                                secrets["readiness-auth"].add(child.encode())
                        if key.lower() in {"url", "admin_url", "management_url", "management_ca_file"}:
                            if isinstance(child, str) and child:
                                secrets["readiness-value"].add(child.encode())
                        collect(child)
                elif isinstance(item, list):
                    for child in item:
                        collect(child)

            collect(value)

    hits: dict[str, list[str]] = {kind: [] for kind in secrets}
    for source in sources:
        content = source.read_bytes()
        for kind, values in secrets.items():
            if any(secret in content for secret in values):
                hits[kind].append(source.relative_to(source_root_base).as_posix())

    for kind, paths in hits.items():
        if paths:
            print(f"exact secret hits ({kind}): {len(paths)}")
            for path in paths:
                print(path)
            raise SystemExit("selected artifact contains an exact closed-run secret")
    print(
        "exact secret screen: "
        f"trace-bearer values={len(secrets['trace-bearer'])}, "
        f"readiness-auth values={len(secrets['readiness-auth'])}, "
        f"readiness values={len(secrets['readiness-value'])}, hits=0"
    )


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--source-root",
        required=True,
        type=Path,
        help="root containing the ignored source corpus paths recorded in ALLOWLIST.tsv",
    )
    args = parser.parse_args()
    source_root_base = args.source_root.resolve()
    if not source_root_base.is_dir():
        parser.error("--source-root must be an existing directory")
    source_roots = tuple(source_root_base / value for value in SOURCE_ROOT_PATHS)
    rows = list(csv.DictReader(LIST.open(newline=""), delimiter="\t"))
    if not rows or set(rows[0]) != {"sha256", "category", "destination", "source"}:
        raise SystemExit("invalid allowlist header")

    destinations: set[str] = set()
    sources: set[str] = set()
    for row in rows:
        source = source_root_base / row["source"]
        destination = PurePosixPath(row["destination"])
        if source in sources or row["destination"] in destinations:
            raise SystemExit(f"duplicate mapping: {row}")
        sources.add(source)
        destinations.add(row["destination"])
        if destination.is_absolute() or ".." in destination.parts:
            raise SystemExit(f"unsafe destination: {destination}")
        if not source.is_file():
            raise SystemExit(f"missing or external source: {source}")
        source_root = next(
            (candidate for candidate in source_roots if source.resolve().is_relative_to(candidate.resolve())),
            None,
        )
        if source_root is None:
            raise SystemExit(f"missing or external source: {source}")
        relative = source.relative_to(source_root).as_posix()
        if relative.startswith("build/") and relative not in ALLOWED_BUILD_SOURCES:
            raise SystemExit(f"broad build artifact selected: {relative}")
        lowered = {part.lower() for part in destination.parts}
        if lowered & FORBIDDEN_PARTS or (
            destination.suffix.lower() in FORBIDDEN_SUFFIXES
            and relative not in ALLOWED_TRACE_SOURCES
        ):
            raise SystemExit(f"sensitive runtime artifact name: {destination}")
        if any(fragment in destination.name.lower() for fragment in FORBIDDEN_NAME_FRAGMENTS):
            raise SystemExit(f"sensitive runtime artifact name: {destination}")
        content = source.read_bytes()
        if any(pattern.search(content) for pattern in FORBIDDEN_CONTENT):
            raise SystemExit(f"credential or private-key content: {source}")
        if digest(source) != row["sha256"]:
            raise SystemExit(f"hash mismatch: {source}")

    exact_closed_secret_screen(source_root_base, sorted(sources))
    print(f"validated {len(rows)} prospective files; no files exported")


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""Stream pinned Go parsed trace text into a bounded lossless stack container."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import struct
import sys

from stack_intern_format import (
    FOOTER,
    HEADER_RE,
    MAGIC,
    MAX_CANONICAL_BYTES,
    MAX_DEFINITION_BYTES,
    MAX_EVENT_BYTES,
    MAX_EVENT_CONTINUATIONS,
    MAX_EVENTS,
    MAX_EXPANDED_BYTES,
    MAX_LINE_BYTES,
    MAX_STACK_DEFINITIONS,
    RECORD_HEADER,
    STACK_META,
    TAG_DEFINITION,
    TAG_FOOTER,
    TAG_LITERAL,
    TAG_REFERENCE,
)


class InternError(Exception):
    pass


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb", buffering=1024 * 1024) as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def open_input(path: str):
    if path == "-":
        return sys.stdin.buffer, False
    return Path(path).open("rb", buffering=1024 * 1024), True


def canonicalize(input_name: str, output_path: Path, ledger_path: Path) -> None:
    if output_path == ledger_path:
        raise InternError("output and ledger paths must differ")
    if output_path.exists() or ledger_path.exists():
        raise InternError("output and ledger must both be fresh")
    output_created = False
    ledger_created = False
    source, close_source = open_input(input_name)
    try:
        descriptor = os.open(output_path, os.O_RDWR | os.O_CREAT | os.O_EXCL, 0o600)
        output_created = True
        with os.fdopen(descriptor, "w+b", buffering=1024 * 1024) as output:
            canonical_hash = hashlib.sha256()
            expanded_hash = hashlib.sha256()
            dictionaries: dict[bytes, tuple[int, bytes]] = {}
            expanded_bytes = 0
            definition_bytes = 0
            event_count = 0
            record_count = 0
            continuation_count = 0
            event_bytes = 0
            literal = bytearray()
            pending: bytes | None = None

            def raw_line() -> bytes | None:
                nonlocal expanded_bytes
                line = source.readline(MAX_LINE_BYTES + 1)
                if not line:
                    return None
                if len(line) > MAX_LINE_BYTES or not line.endswith(b"\n"):
                    raise InternError("parsed input line exceeds fixed line bound")
                expanded_bytes += len(line)
                if expanded_bytes > MAX_EXPANDED_BYTES:
                    raise InternError("logical expanded text exceeds fixed 4 GiB bound")
                expanded_hash.update(line)
                return line

            def account(line: bytes) -> bool:
                nonlocal event_count, continuation_count, event_bytes
                if HEADER_RE.match(line):
                    event_count += 1
                    if event_count > MAX_EVENTS:
                        raise InternError("event count exceeds fixed bound")
                    continuation_count = 0
                    event_bytes = len(line)
                    return True
                if event_count == 0:
                    raise InternError("continuation before first event")
                continuation_count += 1
                event_bytes += len(line)
                if continuation_count > MAX_EVENT_CONTINUATIONS:
                    raise InternError("event continuation count exceeds fixed bound")
                if event_bytes > MAX_EVENT_BYTES:
                    raise InternError("event byte count exceeds fixed bound")
                return False

            def raw_record(tag: int, payload: bytes) -> None:
                nonlocal record_count
                header = RECORD_HEADER.pack(tag, len(payload))
                if output.tell() + len(header) + len(payload) + RECORD_HEADER.size + FOOTER.size > MAX_CANONICAL_BYTES:
                    raise InternError("physical canonical file exceeds fixed 512 MiB bound")
                output.write(header)
                output.write(payload)
                canonical_hash.update(header)
                canonical_hash.update(payload)
                record_count += 1

            def flush_literal() -> None:
                if literal:
                    raw_record(TAG_LITERAL, bytes(literal))
                    literal.clear()

            def add_literal(line: bytes) -> None:
                if literal and len(literal) + len(line) > MAX_LINE_BYTES:
                    flush_literal()
                literal.extend(line)

            def emit_stack(body: bytes) -> None:
                nonlocal definition_bytes
                flush_literal()
                digest = hashlib.sha256(body).digest()
                previous = dictionaries.get(digest)
                if previous is not None:
                    identifier, previous_body = previous
                    if previous_body != body:
                        raise InternError("SHA-256 stack collision with unequal exact bytes")
                    raw_record(TAG_REFERENCE, STACK_META.pack(identifier, digest, len(body)))
                    return
                if len(dictionaries) >= MAX_STACK_DEFINITIONS:
                    raise InternError("stack definition count exceeds fixed bound")
                if definition_bytes + len(body) > MAX_DEFINITION_BYTES:
                    raise InternError("decoded stack dictionary exceeds fixed 64 MiB bound")
                identifier = len(dictionaries) + 1
                dictionaries[digest] = (identifier, body)
                definition_bytes += len(body)
                raw_record(TAG_DEFINITION, STACK_META.pack(identifier, digest, len(body)) + body)

            output.write(MAGIC)
            canonical_hash.update(MAGIC)
            while True:
                line = pending
                pending = None
                if line is None:
                    line = raw_line()
                if line is None:
                    break
                account(line)
                add_literal(line)
                stripped = line.rstrip(b"\n")
                if stripped not in (b"Stack=", b"TransitionStack="):
                    continue
                body = bytearray()
                while True:
                    following = raw_line()
                    if following is None:
                        emit_stack(bytes(body))
                        break
                    if HEADER_RE.match(following):
                        emit_stack(bytes(body))
                        pending = following
                        break
                    if following.rstrip(b"\n") in (b"Stack=", b"TransitionStack="):
                        emit_stack(bytes(body))
                        pending = following
                        break
                    account(following)
                    if following == b"\n":
                        emit_stack(bytes(body))
                        add_literal(following)
                        break
                    body.extend(following)
                    if len(body) > MAX_EVENT_BYTES:
                        raise InternError("stack body exceeds fixed event byte bound")
                if following is None:
                    break

            if event_count == 0:
                raise InternError("parsed trace has no events")
            flush_literal()
            prefooter_hash = canonical_hash.digest()
            footer = FOOTER.pack(
                expanded_bytes,
                event_count,
                len(dictionaries),
                definition_bytes,
                record_count,
                expanded_hash.digest(),
                prefooter_hash,
            )
            footer_header = RECORD_HEADER.pack(TAG_FOOTER, len(footer))
            if output.tell() + len(footer_header) + len(footer) > MAX_CANONICAL_BYTES:
                raise InternError("physical canonical file exceeds fixed 512 MiB bound")
            output.write(footer_header)
            output.write(footer)
            output.flush()
            os.fsync(output.fileno())

        canonical_bytes = output_path.stat().st_size
        canonical_sha256 = sha256_file(output_path)
        ledger = {
            "schema_version": 1,
            "format": "p2pstream-parsed-stack-intern-v1",
            "canonical_bytes": canonical_bytes,
            "canonical_sha256": canonical_sha256,
            "expanded_input_bytes": expanded_bytes,
            "expanded_input_sha256": expanded_hash.hexdigest(),
            "event_count": event_count,
            "definition_count": len(dictionaries),
            "definition_bytes": definition_bytes,
            "record_count": record_count,
            "canonical_prefooter_sha256": prefooter_hash.hex(),
            "fixed_bounds": {
                "max_canonical_input_bytes": MAX_CANONICAL_BYTES,
                "max_expanded_input_bytes": MAX_EXPANDED_BYTES,
                "max_line_bytes": MAX_LINE_BYTES,
                "max_events": MAX_EVENTS,
                "max_event_continuations": MAX_EVENT_CONTINUATIONS,
                "max_event_bytes": MAX_EVENT_BYTES,
                "max_stack_definitions": MAX_STACK_DEFINITIONS,
                "max_definition_bytes": MAX_DEFINITION_BYTES,
            },
        }
        encoded = (json.dumps(ledger, indent=2, sort_keys=True) + "\n").encode()
        ledger_descriptor = os.open(ledger_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        ledger_created = True
        with os.fdopen(ledger_descriptor, "wb") as ledger_stream:
            ledger_stream.write(encoded)
            ledger_stream.flush()
            os.fsync(ledger_stream.fileno())
    except Exception:
        if ledger_created:
            ledger_path.unlink(missing_ok=True)
        if output_created:
            output_path.unlink(missing_ok=True)
        raise
    finally:
        if close_source:
            source.close()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", default="-")
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--ledger", required=True, type=Path)
    arguments = parser.parse_args()
    try:
        canonicalize(arguments.input, arguments.output, arguments.ledger)
    except (InternError, OSError) as error:
        print(f"stack interning failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

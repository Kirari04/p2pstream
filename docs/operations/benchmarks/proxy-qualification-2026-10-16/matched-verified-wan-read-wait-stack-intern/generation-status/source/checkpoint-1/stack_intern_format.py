#!/usr/bin/env python3
"""Bounded lossless container for pinned Go parsed trace stack interning."""

from __future__ import annotations

import hashlib
from pathlib import Path
import re
import struct
from typing import Iterator


MAGIC = b"P2PSTREAM-PARSED-STACK-INTERN\x00\x01\n"
TAG_LITERAL = 1
TAG_DEFINITION = 2
TAG_REFERENCE = 3
TAG_FOOTER = 255
RECORD_HEADER = struct.Struct(">BQ")
STACK_META = struct.Struct(">I32sQ")
FOOTER = struct.Struct(">QQIQQ32s32s")

MAX_CANONICAL_BYTES = 512 * 1024 * 1024
MAX_EXPANDED_BYTES = 4_294_967_296
MAX_LINE_BYTES = 1024 * 1024
MAX_EVENTS = 5_000_000
MAX_EVENT_CONTINUATIONS = 4096
MAX_EVENT_BYTES = 4 * 1024 * 1024
MAX_STACK_DEFINITIONS = 4096
MAX_DEFINITION_BYTES = 64 * 1024 * 1024

HEADER_RE = re.compile(
    rb"^M=(-?\d+) P=(-?\d+) G=(-?\d+) ([A-Za-z]+) Time=(\d+)(?: (.*))?\n?$"
)


class InvalidCanonicalTrace(Exception):
    pass


def _body_lines(body: bytes) -> Iterator[bytes]:
    cursor = 0
    while cursor < len(body):
        newline = body.find(b"\n", cursor)
        if newline < 0:
            raise InvalidCanonicalTrace("stack definition has unterminated line")
        line = body[cursor:newline + 1]
        if len(line) > MAX_LINE_BYTES:
            raise InvalidCanonicalTrace("stack definition line exceeds fixed bound")
        yield line
        cursor = newline + 1


def _validate_stack_body(body: bytes) -> None:
    lines = list(_body_lines(body))
    if len(lines) % 2:
        raise InvalidCanonicalTrace("stack definition has incomplete frame pair")
    for index, line in enumerate(lines):
        if index % 2 == 0:
            if not line.startswith(b"\t") or line.startswith(b"\t\t"):
                raise InvalidCanonicalTrace("stack definition has invalid function line")
        elif not line.startswith(b"\t\t"):
            raise InvalidCanonicalTrace("stack definition has invalid file line")


def _literal_lines(payload: bytes) -> Iterator[bytes]:
    if not payload:
        raise InvalidCanonicalTrace("empty literal record")
    yield from _body_lines(payload)


class CanonicalReader:
    """Expand one canonical file while validating every record and footer."""

    def __init__(self, path: Path):
        self.path = path
        self.summary: dict | None = None

    def iter_lines(self) -> Iterator[bytes]:
        definitions: dict[int, tuple[bytes, bytes]] = {}
        digests: dict[bytes, int] = {}
        definition_bytes = 0
        record_count = 0
        expanded_bytes = 0
        expanded_hash = hashlib.sha256()
        prefooter_hash = hashlib.sha256()
        event_count = 0
        footer_seen = False
        expect_stack_record = False

        def emit(payload: bytes, allow_empty: bool = False) -> Iterator[bytes]:
            nonlocal expanded_bytes, event_count
            if not payload and allow_empty:
                return
            for line in _literal_lines(payload):
                expanded_bytes += len(line)
                if expanded_bytes > MAX_EXPANDED_BYTES:
                    raise InvalidCanonicalTrace("logical expanded text exceeds fixed 4 GiB bound")
                expanded_hash.update(line)
                if HEADER_RE.match(line):
                    event_count += 1
                    if event_count > MAX_EVENTS:
                        raise InvalidCanonicalTrace("event count exceeds fixed bound")
                yield line

        with self.path.open("rb", buffering=1024 * 1024) as stream:
            magic = stream.read(len(MAGIC))
            if magic != MAGIC:
                raise InvalidCanonicalTrace("canonical magic/version mismatch")
            prefooter_hash.update(magic)
            while True:
                header = stream.read(RECORD_HEADER.size)
                if not header:
                    raise InvalidCanonicalTrace("canonical footer is missing")
                if len(header) != RECORD_HEADER.size:
                    raise InvalidCanonicalTrace("truncated canonical record header")
                tag, length = RECORD_HEADER.unpack(header)
                if tag not in (TAG_LITERAL, TAG_DEFINITION, TAG_REFERENCE, TAG_FOOTER):
                    raise InvalidCanonicalTrace("unknown canonical record tag")
                if length > MAX_CANONICAL_BYTES:
                    raise InvalidCanonicalTrace("canonical record length exceeds physical file bound")
                if tag == TAG_LITERAL and (length == 0 or length > MAX_LINE_BYTES):
                    raise InvalidCanonicalTrace("literal record size exceeds fixed bound")
                if tag == TAG_DEFINITION and length > STACK_META.size + MAX_EVENT_BYTES:
                    raise InvalidCanonicalTrace("stack definition record exceeds fixed bound")
                if tag == TAG_REFERENCE and length != STACK_META.size:
                    raise InvalidCanonicalTrace("stack reference record has invalid size")
                if tag == TAG_FOOTER and length != FOOTER.size:
                    raise InvalidCanonicalTrace("canonical footer has invalid size")
                payload = stream.read(length)
                if len(payload) != length:
                    raise InvalidCanonicalTrace("truncated canonical record payload")
                if tag == TAG_FOOTER:
                    if footer_seen:
                        raise InvalidCanonicalTrace("duplicate canonical footer")
                    footer_seen = True
                    if expect_stack_record:
                        raise InvalidCanonicalTrace("canonical footer follows an unresolved stack marker")
                    (
                        expected_expanded_bytes,
                        expected_events,
                        expected_definitions,
                        expected_definition_bytes,
                        expected_records,
                        expected_expanded_hash,
                        expected_prefooter_hash,
                    ) = FOOTER.unpack(payload)
                    if expected_prefooter_hash != prefooter_hash.digest():
                        raise InvalidCanonicalTrace("canonical pre-footer hash mismatch")
                    if stream.read(1):
                        raise InvalidCanonicalTrace("data follows canonical footer")
                    if expected_expanded_bytes != expanded_bytes:
                        raise InvalidCanonicalTrace("expanded byte ledger mismatch")
                    if expected_expanded_hash != expanded_hash.digest():
                        raise InvalidCanonicalTrace("expanded hash ledger mismatch")
                    if expected_events != event_count:
                        raise InvalidCanonicalTrace("event count ledger mismatch")
                    if expected_definitions != len(definitions):
                        raise InvalidCanonicalTrace("definition count ledger mismatch")
                    if expected_definition_bytes != definition_bytes:
                        raise InvalidCanonicalTrace("definition byte ledger mismatch")
                    if expected_records != record_count:
                        raise InvalidCanonicalTrace("record count ledger mismatch")
                    self.summary = {
                        "expanded_input_bytes": expanded_bytes,
                        "expanded_input_sha256": expanded_hash.hexdigest(),
                        "event_count": event_count,
                        "definition_count": len(definitions),
                        "definition_bytes": definition_bytes,
                        "record_count": record_count,
                        "canonical_prefooter_sha256": prefooter_hash.hexdigest(),
                    }
                    return

                if footer_seen:
                    raise InvalidCanonicalTrace("record follows canonical footer")
                prefooter_hash.update(header)
                prefooter_hash.update(payload)
                record_count += 1

                if tag == TAG_LITERAL:
                    if expect_stack_record:
                        raise InvalidCanonicalTrace("literal record replaces required stack record")
                    lines = list(_literal_lines(payload))
                    for index, line in enumerate(lines):
                        if line.rstrip(b"\n") in (b"Stack=", b"TransitionStack="):
                            if index != len(lines) - 1:
                                raise InvalidCanonicalTrace("stack marker is not final in literal record")
                            expect_stack_record = True
                        yield from emit(line)
                    continue
                if not expect_stack_record:
                    raise InvalidCanonicalTrace("stack record lacks preceding stack marker")
                expect_stack_record = False
                if len(payload) < STACK_META.size:
                    raise InvalidCanonicalTrace("short stack record")
                identifier, digest, body_length = STACK_META.unpack(payload[:STACK_META.size])
                body = payload[STACK_META.size:]
                if identifier == 0 or body_length > MAX_EVENT_BYTES:
                    raise InvalidCanonicalTrace("invalid stack identifier or size")
                if tag == TAG_DEFINITION:
                    if identifier != len(definitions) + 1:
                        raise InvalidCanonicalTrace("stack definitions are not sequential")
                    if len(body) != body_length:
                        raise InvalidCanonicalTrace("stack definition length mismatch")
                    if digest in digests or identifier in definitions:
                        raise InvalidCanonicalTrace("stack definition is redefined")
                    if hashlib.sha256(body).digest() != digest:
                        raise InvalidCanonicalTrace("stack definition digest mismatch")
                    _validate_stack_body(body)
                    if len(definitions) >= MAX_STACK_DEFINITIONS:
                        raise InvalidCanonicalTrace("stack definition count exceeds fixed bound")
                    definition_bytes += len(body)
                    if definition_bytes > MAX_DEFINITION_BYTES:
                        raise InvalidCanonicalTrace("decoded stack dictionary exceeds fixed 64 MiB bound")
                    definitions[identifier] = (digest, body)
                    digests[digest] = identifier
                    yield from emit(body, allow_empty=True)
                    continue

                if len(body) != 0:
                    raise InvalidCanonicalTrace("stack reference carries unexpected body")
                definition = definitions.get(identifier)
                if definition is None:
                    raise InvalidCanonicalTrace("dangling stack reference")
                expected_digest, defined_body = definition
                if digest != expected_digest or body_length != len(defined_body):
                    raise InvalidCanonicalTrace("stack reference metadata mismatch")
                yield from emit(defined_body, allow_empty=True)


def strict_ledger_keys() -> set[str]:
    return {
        "schema_version",
        "format",
        "canonical_bytes",
        "canonical_sha256",
        "expanded_input_bytes",
        "expanded_input_sha256",
        "event_count",
        "definition_count",
        "definition_bytes",
        "record_count",
        "canonical_prefooter_sha256",
        "fixed_bounds",
    }


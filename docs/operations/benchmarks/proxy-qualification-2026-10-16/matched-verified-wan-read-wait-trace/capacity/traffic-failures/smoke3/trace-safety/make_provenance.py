#!/usr/bin/env python3
import hashlib
import json
from pathlib import Path


ROOT = Path("/home/user/.codex/worktrees/astra-proxy-performance/p2pstream")
REVIEW = ROOT / "tmp/continuation-20261016/wan-read-wait-capacity-source/review/h1-smoke3-trace-export-safety-1"
RUN = ROOT / "tmp/continuation-20261016/wan-read-wait-capacity-source/runs/tiny-read-wait-capacity-smoke-3/h1"
SOURCE = ROOT / "tmp/continuation-20261016/wan-read-wait-capacity-source"
GOROOT = Path("/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64")


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def record(path: Path):
    return {"path": str(path), "bytes": path.stat().st_size, "sha256": sha256(path)}


index_path = RUN / "read-wait-reductions/index.json"
index = json.loads(index_path.read_text(encoding="utf-8"))
entries = []
for entry in index["entries"]:
    sequence = int(entry["sequence"])
    raw = Path(entry["raw_path"])
    parsed = Path(entry["parsed_path"])
    reduction = Path(entry["reduction_path"])
    parser_status = Path(entry["parser_status_path"])
    wire = REVIEW / f"sequence-{sequence}.wire.txt"
    entries.append({
        "sequence": sequence,
        "protocol": entry["public_protocol"],
        "raw": record(raw),
        "raw_index_hash_match": sha256(raw) == entry["raw_sha256"],
        "parsed": record(parsed),
        "parsed_index_hash_match": sha256(parsed) == entry["parsed_sha256"],
        "parser_status": record(parser_status),
        "parser_status_value": int(parser_status.read_text(encoding="ascii").strip()),
        "reduction": record(reduction),
        "reduction_index_hash_match": sha256(reduction) == entry["reduction_sha256"],
        "wire": record(wire),
        "wire_status": record(REVIEW / f"sequence-{sequence}.wire.status"),
        "wire_stderr": record(REVIEW / f"sequence-{sequence}.wire.stderr.log"),
        "server_binary_sha256_from_index": entry["server_binary_sha256"],
        "runner_sha256_from_index": entry["runner_sha256"],
        "server_source_ref_from_index": entry["server_source_ref"],
    })

source_files = [
    SOURCE / "generated/qualify-proxy-agent-growth-wan-read-wait.sh",
    SOURCE / "build/bin/current-server-wan-read-wait-capacity-audited.test",
    SOURCE / "build/source/internal/server/proxy_qualification_test.go",
    SOURCE / "build/source/internal/server/proxy_qualification_wan_read_wait_trace_test.go",
    SOURCE / "build/source/internal/server/proxy_qualification_wan_read_wait_trace_contract_test.go",
    SOURCE / "run-capped-trace-parser.py",
    SOURCE / "analyzer/analyze_parsed_trace.py",
    GOROOT / "bin/go",
    GOROOT / "src/runtime/traceruntime.go",
    GOROOT / "src/runtime/trace/annotation.go",
    GOROOT / "src/internal/trace/tracev2/events.go",
    GOROOT / "src/internal/trace/raw/event.go",
    GOROOT / "src/internal/trace/event.go",
    GOROOT / "src/cmd/trace/main.go",
]

provenance = {
    "schema_version": 1,
    "purpose": "immutable source, tool, raw trace, parsed trace, reduction, and wire-review binding",
    "source_run": str(RUN),
    "index": record(index_path),
    "source_and_tool_files": [record(path) for path in source_files],
    "review_sources": [
        record(REVIEW / "audit_trace_export.py"),
        record(REVIEW / "make_provenance.py"),
    ],
    "review_outputs": [
        record(REVIEW / "audit.invocation.txt"),
        record(REVIEW / "audit.status"),
        record(REVIEW / "audit.stdout.log"),
        record(REVIEW / "audit.stderr.log"),
        record(REVIEW / "export-safety-report.json"),
    ],
    "entries": entries,
    "original_run_modified": False,
    "endpoint_or_traffic_run": False,
}

destination = REVIEW / "PROVENANCE.json"
with destination.open("x", encoding="utf-8") as output:
    json.dump(provenance, output, indent=2, sort_keys=True)
    output.write("\n")

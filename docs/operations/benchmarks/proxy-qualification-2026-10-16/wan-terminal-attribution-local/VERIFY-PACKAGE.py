#!/usr/bin/env python3
"""File/hash/content audit only; never invokes build, test, or runtime commands."""
from pathlib import Path
import hashlib
import json
import re
import sys

package = Path(__file__).resolve().parent


def digest(data):
    return hashlib.sha256(data).hexdigest()


def load(name):
    return json.loads((package / name).read_text())


def require(condition, rule, path=""):
    if not condition:
        # Report only rule and path, never matched content.
        raise SystemExit(json.dumps({"passed": False, "rule": rule, "path": str(path)}))


exports = load("EXPORT-MAP.json")
source_root = Path(exports["source_root"])
destinations = set()
for row in exports["copies"]:
    name = row["destination"]
    require(name not in destinations, "duplicate export", name)
    destinations.add(name)
    target = package / name
    require(target.is_file() and not target.is_symlink(), "regular export", name)
    data = target.read_bytes()
    require(len(data) == row["bytes"] and digest(data) == row["sha256"], "copy hash/size", name)
    original = source_root / row["source"]
    require(original.is_file() and not original.is_symlink(), "original regular file", row["source"])
    require(digest(original.read_bytes()) == row["sha256"], "source hash", row["source"])

binding = load("FULL-SOURCE-BINDING.json")
original_map = source_root / binding["original_materialization_map"]["source"]
require(digest(original_map.read_bytes()) == binding["original_materialization_map"]["sha256"], "original materialization map hash")
inventory = package / binding["final_inventory"]["export"]
require(digest(inventory.read_bytes()) == binding["final_inventory"]["sha256"], "final inventory hash")
inventory_lines = inventory.read_text().splitlines()
require(len(inventory_lines) == 459, "final source cardinality")
actual_source_bytes = 0
for line in inventory_lines:
    expected, name = line.split("  ", 1)
    original = source_root / "tmp/wan-terminal-attribution-20261017" / name
    data = original.read_bytes()
    require(digest(data) == expected, "actual bound input hash", name)
    actual_source_bytes += len(data)
require(actual_source_bytes == 8164026, "actual source bytes")

m2 = load("successor/PREDECLARED-COMMANDS-2.json")
m3 = load("successor/PREDECLARED-COMMANDS-3.json")
require(m2["resolved_build_settings_command"]["expected"]["GOENV"] == "off", "original preflight expectation")
require(m3["resolved_build_settings_command"]["expected"]["GOENV"] == "", "corrected preflight expectation")
m2["resolved_build_settings_command"]["expected"]["GOENV"] = ""
m3.pop("preflight_contract_correction")
require(m2 == m3, "only expected GOENV plus provenance changed")
first = load("local-window-1/RESULT.json")
require(first["stopped"] and first["stop_at"] == "preflight" and len(first["executions"]) == 1, "stopped first preflight preserved")
require(not first["executions"][0]["accepted"], "first preflight rejected")
second = load("local-window-2/RESULT.json")
require(second["complete"] and not second["stopped"] and second["no_wan"], "completed local-only second window")
require(second["authorized_manifest_sha256"] == digest((package / "successor/PREDECLARED-COMMANDS-3.json").read_bytes()), "actual frozen command3 binding")
require([r["name"] for r in second["executions"]] == ["preflight", "python-contracts", "normal", "race"], "exact approved executions")
for record in second["executions"]:
    require(record["accepted"] and record["exit_status"] == 0 and not record["forced"] and not record["remaining_group_members"], "natural zero owned join", record["name"])
    for when in ["before", "after"]:
        require(record[when]["source_manifest_sha256"] == digest(inventory.read_bytes()) and not record[when]["changed_sources"] and record[when]["toolchain_hash_matches"], "execution source/tool binding", record["name"])
for mode in ["normal", "race"]:
    text = (package / "local-window-2" / (mode + ".stdout.log")).read_text()
    counts = [len(re.findall(pattern, text, re.M)) for pattern in [r"^=== RUN   ", r"^\s*--- PASS: ", r"^\s*--- FAIL: ", r"^\s*--- SKIP: "]]
    require(counts == [170, 170, 0, 0], "raw Go log counts", mode)
python_log = (package / "local-window-2/python-contracts.stderr.log").read_text()
require("Ran 7 tests" in python_log and python_log.rstrip().endswith("OK"), "raw Python summary")

# Exact known fixture payload fragments stay in memory and are never printed.
fixture_root = source_root / "tmp/wan-terminal-attribution-20261017/test-source/third_party/yamux/testdata"
fixture_payloads = []
for name in ["cert.pem", "key.pem"]:
    data = (fixture_root / name).read_bytes()
    fixture_payloads.extend(line for line in data.splitlines() if len(line) >= 32 and not line.startswith(b"-----"))
require(bool(fixture_payloads), "known fixture scan inputs present")
instructions = Path("/home/user/Documents/p2pstream/AGENTS.md").read_text()
password = re.search(r"password\s+`([^`]+)`", instructions)
require(password is not None, "local credential scan input present")
private_marker = re.compile(br"(?m)^-----BEGIN (?:[A-Z ]*PRIVATE KEY|CERTIFICATE)-----")
prohibited = {".pem", ".key", ".crt", ".db", ".sqlite", ".sqlite3", ".zip", ".gz", ".xz", ".tar", ".exe", ".bin", ".o", ".a", ".so"}
require(not list(package.rglob("*.go")), "no discoverable Go artifact files")
all_files = sorted(p for p in package.rglob("*") if p.is_file())
expected_names = destinations | set(exports["generated"])
actual_names = {str(p.relative_to(package)) for p in all_files}
allowed_absent = {"VALIDATION.json", "SHA256SUMS"} if "--finalize" in sys.argv else set()
require(actual_names <= expected_names and expected_names - actual_names <= allowed_absent, "exact curated package membership")
for path in all_files:
    name = str(path.relative_to(package)); data = path.read_bytes()
    require(not path.is_symlink() and path.suffix.lower() not in prohibited, "prohibited export format", name)
    require(b"\0" not in data, "binary NUL content", name)
    require(private_marker.search(data) is None, "PEM material", name)
    require(not any(value in data for value in fixture_payloads), "known fixture payload", name)
    require(password.group(1).encode() not in data, "known local credential", name)

result = {"passed": True, "audit_scope": "File-copy/hash/log consistency and nonprinting fixture-content audit only; no build/test/runtime invocation.", "copies_verified_against_actual_sources": len(exports["copies"]), "actual_final_source_files_rehashed": len(inventory_lines), "actual_final_source_bytes": actual_source_bytes, "original_materialization_map_hash_verified": True, "exact_manifest_correction_verified": True, "stopped_first_preflight_preserved": True, "accepted_second_window_log_counts_verified": {"python": 7, "normal": 170, "race": 170}, "nonprinting_fixture_scan": {"passed": True, "rules": ["prohibited file formats", "binary NUL", "PEM private key/certificate material", "exact known fixture payload fragments", "known local management credential"], "matched_content_printed": False}, "go_source_artifacts_exported_as_text": sum(r["destination"].endswith(".go.txt") for r in exports["copies"]), "go_files_remaining": 0, "copy_bytes": sum(r["bytes"] for r in exports["copies"]), "package_membership_exact": True}
if "--finalize" in sys.argv:
    (package / "VALIDATION.json").write_text(json.dumps(result, indent=2) + "\n")
    checksums = []
    for path in sorted(p for p in package.rglob("*") if p.is_file() and p.name != "SHA256SUMS"):
        checksums.append(digest(path.read_bytes()) + "  " + str(path.relative_to(package)))
    # Nested historical SHA256SUMS are ordinary provenance files too.
    for path in sorted(package.rglob("SHA256SUMS")):
        if path != package / "SHA256SUMS":
            checksums.append(digest(path.read_bytes()) + "  " + str(path.relative_to(package)))
    (package / "SHA256SUMS").write_text("\n".join(sorted(checksums, key=lambda s: s.split("  ", 1)[1])) + "\n")
manifest_entries = (package / "SHA256SUMS").read_text().splitlines()
seen = set()
for line in manifest_entries:
    expected, name = line.split("  ", 1)
    require(name not in seen, "unique package manifest entry", name)
    seen.add(name)
    require(digest((package / name).read_bytes()) == expected, "package manifest hash", name)
require(seen == {str(p.relative_to(package)) for p in package.rglob("*") if p.is_file() and p != package / "SHA256SUMS"}, "complete package manifest")
print(json.dumps({"passed": True, "copy_hashes_verified": result["copies_verified_against_actual_sources"], "actual_input_hashes_verified": len(inventory_lines), "package_files": len(seen) + 1, "package_bytes": sum(p.stat().st_size for p in package.rglob("*") if p.is_file()), "fixture_secret_scan_passed": True, "root_manifest_sha256": digest((package / "SHA256SUMS").read_bytes())}))

#!/usr/bin/env python3
"""Offline schema contracts from preserved original pressure evidence.

The executable fixture inputs are inert text, never Go binaries. Historical
measurement rows keep their original field values/order/payloads; only local
fixture provenance and newly explicit formerly-omitted zero telemetry change.
"""
import copy
import hashlib
import importlib.util
import json
import os
import re
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
SCRIPTS = HERE / "proposal/scripts"
HISTORICAL = ROOT / "docs/operations/benchmarks/proxy-qualification-2026-10-16/agent-grown-pool-pressure"
VALIDATOR = SCRIPTS / "validate-owned-pressure.py"
FIXTURE_REF = "a" * 40
LEGACY_REF = "3ec137c3ad3d07028e867042fb00d3933912d7ab"
CONTRACTS = HERE / "contracts"
CONTRACTS.mkdir(exist_ok=True)
REQUIRED_INPUTS = (
    "archives/current.tar", "archives/legacy.tar", "bin/current-server.test", "bin/current-agent.test", "bin/legacy-agent.test",
    "sources/current/internal/agent/testdata/proxy_qualification_external_legacy_test.go",
    "provenance/qualify-proxy-agent-growth-pressure-owned.sh", "provenance/validate-owned-pressure.py",
    "provenance/qualify-proxy-pressure-checkpoint.sh", "provenance/go-build-settings.json",
)


def write_json(path, data):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, sort_keys=True, indent=2) + "\n")


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def fixture(root):
    changes = []
    for name in REQUIRED_INPUTS:
        path = root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        if name.startswith("provenance/") and (SCRIPTS / path.name).is_file():
            shutil.copyfile(SCRIPTS / path.name, path)
        else:
            path.write_text("INERT OFFLINE FIXTURE INPUT: " + name + "\n")
    tool = root / "inert-go-binary"
    tool.write_text("NOT EXECUTABLE: pinned toolchain fixture\n")
    manifest = {"schema_version": 1, "current_ref": FIXTURE_REF, "legacy_ref": LEGACY_REF,
                "go_binary": str(tool), "go_binary_sha256": sha(tool),
                "sha256": {name: sha(root / name) for name in REQUIRED_INPUTS}}
    write_json(root / "provenance/build-manifest.json", manifest)
    rows = [json.loads(line) for line in (HISTORICAL / "results.jsonl").read_text().splitlines() if line]
    for index, row in enumerate(rows):
        if row.get("measurement_stage") in ("warmup", "retained-warmup", "pressure-recovery", "canceled-upload", "pressure-rejection"):
            for key, value in (("dropped_event_delta", 0), ("event_queue_after_drain", 0), ("event_drain_error", "")):
                if key not in row:
                    row[key] = value
                    changes.append({"row": index, "field": key, "from": "ABSENT (historical omitempty)", "to": value})
        if isinstance(row.get("provenance"), dict):
            replacements = {"server_source_ref": FIXTURE_REF, "aggregate_agent_source_ref": FIXTURE_REF, "legacy_agent_source_ref": LEGACY_REF,
                            "server_binary_sha256": manifest["sha256"]["bin/current-server.test"],
                            "aggregate_agent_binary_sha256": manifest["sha256"]["bin/current-agent.test"],
                            "legacy_agent_binary_sha256": manifest["sha256"]["bin/legacy-agent.test"],
                            "harness_sha256": manifest["sha256"]["provenance/qualify-proxy-agent-growth-pressure-owned.sh"]}
            for key, value in replacements.items():
                if key not in row["provenance"]:
                    continue
                changes.append({"row": index, "field": "provenance." + key, "from": row["provenance"].get(key), "to": value})
                row["provenance"][key] = value
    attempt = root / "attempt-1"
    attempt.mkdir()
    (attempt / "results.jsonl").write_text("".join(json.dumps(row) + "\n" for row in rows))
    shutil.copyfile(HISTORICAL / "summary.json", attempt / "results.summary.json")
    labels = ("current-server-aggregate-v1-1-agent", "current-server-aggregate-v1-1-server", "origin",
              "agent-namespace", "router-namespace", "canceled-upload-load")
    lifecycle = [{"event": "namespace-helpers-start", "runner_namespace": "fixture-net-1",
                  "agent": {"pid": 2, "start_ticks": 2, "namespace": "fixture-net-2"},
                  "router": {"pid": 3, "start_ticks": 3, "namespace": "fixture-net-3"}}]
    lifecycle += [{"event": "process-exit", "label": label, "pid": index + 1, "status": 0,
                   "forced": False, "group_joined": True, "invalidates_success": False} for index, label in enumerate(labels)]
    (attempt / "results.lifecycle.jsonl").write_text("".join(json.dumps(row) + "\n" for row in lifecycle))
    write_json(attempt / "results.status.json", {"exit_status": 0, "emergency_cleanup": False})
    write_json(attempt / "supervisor-lifecycle.json", {"runner_status": 0, "group_joined": True, "forced": False})
    write_json(root / "FIXTURE-ONLY-SUBSTITUTIONS.json", {
        "historical_results_sha256": sha(HISTORICAL / "results.jsonl"),
        "classification": "synthetic offline contract; no build, traffic or real lifecycle proof",
        "inert_inputs": list(REQUIRED_INPUTS) + ["inert-go-binary"],
        "synthetic_fields": ["manifest", "namespace/process lifecycle", "inner/outer final statuses"],
        "measurement_row_substitutions": changes})
    return rows


class PressureContracts(unittest.TestCase):
    def run_case(self, name, mutate=None, expected=None):
        with tempfile.TemporaryDirectory(prefix="pressure-offline-") as directory:
            root = Path(directory)
            rows = fixture(root)
            manifest_sha = sha(root / "provenance/build-manifest.json")
            if mutate:
                mutate(root, rows)
                (root / "attempt-1/results.jsonl").write_text("".join(json.dumps(row) + "\n" for row in rows))
            result = subprocess.run(["python3", str(VALIDATOR), str(root), manifest_sha], text=True, capture_output=True, timeout=5)
            write_json(CONTRACTS / f"{name}.json", {"exit_status": result.returncode, "stderr": result.stderr, "stdout": result.stdout})
            if expected is None:
                self.assertEqual(result.returncode, 0, result.stderr)
            else:
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(expected, result.stderr)
            return result

    def test_scope_contract_extracted_function_module_parses(self):
        contract = (SCRIPTS / "contract-pressure-scopes.sh").read_text()
        extractor = next(line for line in contract.splitlines() if line.startswith("extract() {"))
        names = "cleanup register_owned_group process_start_state assert_namespace_helper wait_ready join_owned_group stop_process cgroup_snapshot"
        def module(extractor):
            return subprocess.run(["bash", "-c", extractor + "\nfor name in " + names + '; do extract "$name"; done'],
                                  env={**os.environ, "runner": str(SCRIPTS / "qualify-proxy-agent-growth-pressure-owned.sh")},
                                  capture_output=True, text=True, timeout=5)
        result = module(extractor)
        self.assertEqual(result.returncode, 0, result.stderr)
        for name in names.split():
            self.assertIn(name + "() {", result.stdout)
        parsed = subprocess.run(["bash", "-n"], input=result.stdout, capture_output=True, text=True)
        self.assertEqual(parsed.returncode, 0, parsed.stderr)
        (CONTRACTS / "scope-extracted-functions.sh").write_text(result.stdout)
        # Keep the observed failure reproducible: the old prefix match cuts
        # cgroup_snapshot's embedded Python dictionary before its heredoc end.
        old = module(extractor.replace('copy && $0 == "}" {exit}', 'copy && /^}/ {exit}'))
        parsed_old = subprocess.run(["bash", "-n"], input=old.stdout, capture_output=True, text=True)
        self.assertNotEqual(parsed_old.returncode, 0)
        write_json(CONTRACTS / "scope-extracted-syntax.json", {
            "corrected_exit_status": parsed.returncode, "previous_extractor_exit_status": parsed_old.returncode,
            "previous_extractor_stderr": parsed_old.stderr})

    def test_outer_grace_exceeds_inner_cleanup_and_cache_is_shared(self):
        runner = (SCRIPTS / "qualify-proxy-agent-growth-pressure-owned.sh").read_text()
        wrapper = (SCRIPTS / "qualify-proxy-pressure-checkpoint.sh").read_text()
        cleanup = runner.split("cleanup() {", 1)[1].split("\ntrap cleanup EXIT", 1)[0]
        inner_windows = re.findall(r"attempt<([0-9]+).*?sleep ([.0-9]+)", cleanup, re.S)
        inner_seconds = sum(int(count) * float(sleep) for count, sleep in inner_windows)
        finish = wrapper.split("finish() {", 1)[1].split("\ntrap finish EXIT", 1)[0]
        count, sleep = re.search(r"attempt<([0-9]+).*?sleep ([.0-9]+)", finish, re.S).groups()
        term_grace = int(count) * float(sleep)
        timeout_grace = int(re.search(r"--kill-after=([0-9]+)s", wrapper)[1])
        self.assertEqual(inner_seconds, 10)
        self.assertGreaterEqual(term_grace, inner_seconds + 5)
        self.assertGreaterEqual(timeout_grace, inner_seconds + 5)
        self.assertNotIn("GOCACHE=", wrapper)

    def test_preserved_original_order_payloads_fields(self):
        self.run_case("preserved-original-pass")

    def test_missing_duplicate_required_stage(self):
        for stage in ("pressure-baseline", "pressure-critical", "pressure-released", "pressure-recovered",
                      "pressure-rejection", "pressure-control-roundtrip", "canceled-upload", "pressure-result"):
            for duplicate in (False, True):
                def mutate(root, rows, stage=stage, duplicate=duplicate):
                    target = next(row for row in rows if row.get("measurement_stage") == stage)
                    rows.append(copy.deepcopy(target)) if duplicate else rows.remove(target)
                self.run_case(f"stage-{stage}-{'duplicate' if duplicate else 'missing'}", mutate, "missing unique")

    def test_transfer_missing_duplicate_order_payload(self):
        for name, operation in (("missing", lambda rows: rows.pop(0)),
                                ("duplicate", lambda rows: rows.insert(0, copy.deepcopy(rows[0]))),
                                ("order", lambda rows: rows.__setitem__(slice(0, 2), list(reversed(rows[:2])))),
                                ("payload", lambda rows: rows[0].__setitem__("configured_payload_bytes", 4 << 20))):
            self.run_case(f"transfer-{name}", lambda root, rows: operation(rows), "original four warmups")

    def test_acceptance_exact_booleans(self):
        for name, acceptance in (("empty", {}), ("nonboolean", {key: 1 for key in json.loads((HISTORICAL / "summary.json").read_text())["acceptance"]})):
            def mutate(root, rows):
                path = root / "attempt-1/results.summary.json"
                data = json.loads(path.read_text())
                data["acceptance"] = acceptance
                write_json(path, data)
            self.run_case(f"acceptance-{name}", mutate, "pressure acceptance schema")

    def test_every_bound_actual_input_is_rehashed(self):
        for name in REQUIRED_INPUTS:
            self.run_case("tamper-" + name.replace("/", "_"), lambda root, rows, name=name: (root / name).write_text("tampered\n"), "bound input hash mismatch")
        self.run_case("tamper-go", lambda root, rows: (root / "inert-go-binary").write_text("changed\n"), "pinned Go binary hash mismatch")

    def test_source_and_manifest_mismatch(self):
        self.run_case("row-source-mismatch", lambda root, rows: rows[0]["provenance"].__setitem__("server_source_ref", "b" * 40), "server_source_ref input mismatch")
        self.run_case("sample-source-mismatch", lambda root, rows: next(row for row in rows if row.get("measurement_stage") == "pressure-critical")["provenance"].__setitem__("server_source_ref", "b" * 40), "pressure sample provenance mismatch")
        self.run_case("row-binary-hash-mismatch", lambda root, rows: rows[0]["provenance"].__setitem__("server_binary_sha256", "b" * 64), "server_binary_sha256 hash mismatch")
        def mutate(root, rows):
            path = root / "provenance/build-manifest.json"
            data = json.loads(path.read_text())
            data["current_ref"] = "b" * 40
            write_json(path, data)
            for row in rows:
                if "provenance" in row:
                    row["provenance"]["server_source_ref"] = "b" * 40
        self.run_case("editable-manifest-and-row", mutate, "build manifest changed after launch")

    def test_strict_events_and_statuses(self):
        for key, value in (("dropped_event_delta", None), ("event_queue_after_drain", False), ("event_queue_after_drain", 0.0), ("event_drain_error", None)):
            self.run_case(f"telemetry-{key}-{value}", lambda root, rows, key=key, value=value: rows[0].__setitem__(key, value), "transfer status/event gate failed")
        self.run_case("response-extra", lambda root, rows: rows[0]["status_classes"].__setitem__("5xx", 1), "transfer status/event gate failed")

    def test_lifecycle_missing_duplicate_forced(self):
        for name in ("missing", "duplicate", "forced"):
            def mutate(root, rows, name=name):
                path = root / "attempt-1/results.lifecycle.jsonl"
                data = [json.loads(line) for line in path.read_text().splitlines()]
                if name == "missing": data.pop()
                elif name == "duplicate": data.append(copy.deepcopy(data[-1]))
                else: data[-1]["forced"] = True
                path.write_text("".join(json.dumps(row) + "\n" for row in data))
            self.run_case(f"lifecycle-{name}", mutate, "nonnatural" if name == "forced" else "missing/duplicate")


if __name__ == "__main__":
    unittest.main()

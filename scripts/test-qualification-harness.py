#!/usr/bin/env python3
"""Offline contracts for qualification validation and bounded child joins.

Extract the shipped shell/Python/jq code so these checks exercise the actual
runner gates without building binaries or starting Docker/network workloads.
"""
import ast
import copy
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
PERFORMANCE = (ROOT / "scripts/qualify-proxy-performance.sh").read_text()
STANDING = (ROOT / "scripts/qualify-proxy-agent-standing.sh").read_text()
CAPACITY = (ROOT / "scripts/qualify-proxy-capacity-checkpoint.sh").read_text()


def shell_function(source, name):
    return re.search(rf"^{name}\(\) \{{\n.*?^\}}", source, re.M | re.S)[0]


def python_block(source, invocation):
    return source.split(invocation + " <<'PY'\n", 1)[1].split("\nPY\n", 1)[0]


STANDING_PY = python_block(STANDING, 'python3 - "$output_dir" "$status16" "$status32"')
CAPACITY_PY = python_block(CAPACITY, 'python3 - "$output_root/results" "$summary" "candidate-$candidate_ref"')
namespace = {}
# Load only pure validation functions and acceptance key data, not the runners.
for source in (STANDING_PY, CAPACITY_PY):
    tree = ast.parse(source)
    nodes = [node for node in tree.body if isinstance(node, ast.FunctionDef)
             or isinstance(node, ast.Assign) and any(isinstance(target, ast.Name)
             and target.id == "expected_acceptance" for target in node.targets)]
    exec(compile(ast.Module(body=nodes, type_ignores=[]), "<runner-contracts>", "exec"), namespace)


def standing_row(agents):
    process = {"cgroup_memory_current_bytes": 1024, "runtime": {
        "agent_capacity": {"session_resource_bytes": 1024}}}
    sample = {"parallel_cgroup_memory_current_sum_bytes": 1024,
              "sample_span_seconds": 0.01,
              "server_process": {**process, "runtime": {"agent_tunnels": [
                  {"session_resource_bytes": 1024} for _ in range(agents)]}},
              "agent_processes": [copy.deepcopy(process) for _ in range(agents)]}
    return {"acceptance": dict.fromkeys(namespace["expected_acceptance"], True),
            "label": f"aggregate-standing-{agents}", "scenario": "agent-standing-pool",
            "external_agents": agents, "transport_mode": "aggregate-v1",
            "bulk_sessions_per_agent": 4, "physical_sessions_per_agent": 5,
            "initial": sample, "final": copy.deepcopy(sample),
            "health_roundtrips": [{"requested_agent_index": n} for n in range(1, agents + 1)]}


def event_row():
    return {"attempted": 512, "persisted_event_delta": 512, "errors": 0,
            "status_classes": {"2xx": 512}, "event_stats_captured": True,
            "dropped_event_delta": 0, "event_queue_after_drain": 0, "event_drain_error": ""}


class StandingContracts(unittest.TestCase):
    def test_acceptance_keys_match_producer(self):
        producer = PERFORMANCE.split("capture_external_standing() {", 1)[1].split("<<'PY'\n", 1)[1].split("\nPY\n", 1)[0]
        acceptance = next(node.value for node in ast.parse(producer).body
                          if isinstance(node, ast.Assign) and any(isinstance(target, ast.Name)
                          and target.id == "acceptance" for target in node.targets))
        keys = {ast.literal_eval(key) for key in acceptance.keys}
        self.assertEqual(len(keys), 19)
        self.assertEqual(keys, namespace["expected_acceptance"])

    def test_exact_contract(self):
        valid = standing_row(16)
        self.assertTrue(namespace["standing_contract"](valid, 16))
        mutations = []
        for key in ("acceptance", "scenario", "label", "external_agents", "health_roundtrips"):
            row = copy.deepcopy(valid)
            del row[key]
            mutations.append(row)
        for acceptance in ({}, {**valid["acceptance"], "unexpected": True},
                           dict.fromkeys(valid["acceptance"], 1)):
            mutations.append({**valid, "acceptance": acceptance})
        mutations.extend([{**valid, "external_agents": 32}, {**valid, "label": "wrong"},
                          {**valid, "health_roundtrips": [{"requested_agent_index": 1}] * 16}])
        row = copy.deepcopy(valid)
        row["final"]["agent_processes"].pop()
        mutations.append(row)
        for row in mutations:
            with self.subTest(row=row):
                self.assertFalse(namespace["standing_contract"](row, 16))

    def test_summary_rejects_empty_acceptance_and_malformed_rows(self):
        for malformed in (False, True):
            with tempfile.TemporaryDirectory() as path:
                root = Path(path)
                for agents in (16, 32):
                    row = standing_row(agents)
                    row["acceptance"] = {}
                    (root / f"standing-{agents}.jsonl").write_text("{broken\n" if malformed else json.dumps(row) + "\n")
                result = subprocess.run(["python3", "-", path, "0", "0"], input=STANDING_PY, text=True, capture_output=True)
                self.assertNotEqual(result.returncode, 0)
                summary = json.loads((root / "summary.json").read_text())
                self.assertEqual(summary["acceptance"], {"agents_16": False, "agents_32": False})
                self.assertEqual(summary["cohorts"]["16"]["row_count"], 1)

    def test_summary_retains_malformed_measurement_failure(self):
        with tempfile.TemporaryDirectory() as path:
            root = Path(path)
            for agents in (16, 32):
                row = standing_row(agents)
                del row["initial"]["parallel_cgroup_memory_current_sum_bytes"]
                (root / f"standing-{agents}.jsonl").write_text(json.dumps(row) + "\n")
            result = subprocess.run(["python3", "-", path, "0", "0"], input=STANDING_PY, text=True, capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            summary = json.loads((root / "summary.json").read_text())
            self.assertFalse(summary["acceptance"]["agents_16"])
            self.assertIn("malformed standing measurements", summary["cohorts"]["16"]["error"])

    def test_summary_success_and_nonzero_runner(self):
        for status, expected in (("0", True), ("7", False)):
            with tempfile.TemporaryDirectory() as path:
                root = Path(path)
                for agents in (16, 32):
                    (root / f"standing-{agents}.jsonl").write_text(json.dumps(standing_row(agents)) + "\n")
                result = subprocess.run(["python3", "-", path, status, "0"], input=STANDING_PY, text=True, capture_output=True)
                self.assertEqual(result.returncode == 0, expected, result.stderr)
                self.assertEqual(json.loads((root / "summary.json").read_text())["acceptance"]["agents_16"], expected)


class CapacityContracts(unittest.TestCase):
    def test_required_event_fields_and_exact_response_classes(self):
        valid = event_row()
        self.assertTrue(namespace["events_conserved"](valid))
        for field in ("dropped_event_delta", "event_queue_after_drain", "event_drain_error", "persisted_event_delta"):
            for value in (None, False, 0.0):
                row = {**valid, field: value}
                self.assertFalse(namespace["events_conserved"](row), (field, value))
            row = valid.copy()
            del row[field]
            self.assertFalse(namespace["events_conserved"](row), field)
        for classes in ({}, {"2xx": 512, "5xx": 1}, {"2xx": 511}, {"2xx": 512.0}, {"2xx": 512, "bogus": 0}):
            self.assertFalse(namespace["events_conserved"]({**valid, "status_classes": classes}), classes)

    def test_required_cgroup_zero_fields(self):
        valid = {"container": "test", "cgroup_inode": 1, "cgroup_memory_max_bytes": 4294967296,
                 "cgroup_memory_events_captured": True, "oom_delta": 0, "oom_kill_delta": 0}
        self.assertTrue(namespace["process_cgroup_ok"](valid))
        for field in ("oom_delta", "oom_kill_delta"):
            for value in (None, False, 0.0, 1):
                self.assertFalse(namespace["process_cgroup_ok"]({**valid, field: value}))
            row = valid.copy()
            del row[field]
            self.assertFalse(namespace["process_cgroup_ok"](row))

    def test_strict_integer_load_schema(self):
        contract = PERFORMANCE.split("<<'PYCONTRACT'\n", 1)[1].split('\nPYCONTRACT\n', 1)[0]
        valid = {**event_row(), "useful_completed": 512, "errors": 0,
                 "server_process": {"oom_delta": 0, "oom_kill_delta": 0}, "agent_processes": []}
        with tempfile.TemporaryDirectory() as path:
            source = Path(path) / "row.json"
            for field, value in (("dropped_event_delta", 0), ("dropped_event_delta", 0.0),
                                 ("event_queue_after_drain", False), ("persisted_event_delta", 512.0)):
                source.write_text(json.dumps({**valid, field: value}))
                result = subprocess.run(["python3", "-", str(source), "1"], input=contract, text=True, capture_output=True)
                self.assertEqual(result.returncode == 0, type(value) is int, result.stderr)

    def test_external_jq_gate(self):
        gate = PERFORMANCE.split('        def count:', 1)[1].split('\n      \' "$result"', 1)[0]
        gate = 'def count:' + gate
        tunnel = {"connected": True, "flow_control": "aggregate-v1", "control_connected": True, "bulk_sessions": 4}
        process = {"container": "test", "cgroup_memory_max_bytes": 4294967296,
                   "cgroup_memory_events_captured": True, "oom_delta": 0, "oom_kill_delta": 0}
        valid = {**event_row(), "useful_completed": 512, "errors": 0,
                 "agent_tunnels_initial": [tunnel], "agent_tunnels_high_water": [tunnel], "agent_tunnels_final": [tunnel],
                 "server_process": {**process, "cgroup_inode": 1},
                 "agent_processes": [{**process, "cgroup_inode": 2}]}
        def accepted(row, scenario="recovery", diagnostics=0):
            command = ["jq", "-e", "--arg", "mode", "aggregate-v1", "--arg", "scenario", scenario]
            for key, value in {"agents": 1, "bulk": 4, "agent_ballast_bytes": 0, "boundary_diagnostics": diagnostics, "constrained": 1}.items():
                command += ["--argjson", key, str(value)]
            return subprocess.run(command + [gate], input=json.dumps(row), text=True, capture_output=True)
        result = accepted(valid)
        self.assertEqual(result.returncode, 0, result.stderr)
        for field in ("dropped_event_delta", "event_queue_after_drain", "event_drain_error"):
            row = copy.deepcopy(valid)
            del row[field]
            self.assertNotEqual(accepted(row).returncode, 0, field)
        for owner in ("server_process", "agent_processes"):
            row = copy.deepcopy(valid)
            process = row[owner] if owner == "server_process" else row[owner][0]
            del process["oom_delta"]
            self.assertNotEqual(accepted(row).returncode, 0, owner)
        row = {**valid, "status_classes": {"2xx": 512, "5xx": 1}}
        self.assertNotEqual(accepted(row).returncode, 0)
        diagnostic = {**valid, "status_classes": {}, "useful_completed": 0, "errors": 512,
                      "persisted_event_delta": 0, "admission_rejections": {"public_connection_resource": 512}}
        result = accepted(diagnostic, "capacity-512-h1", diagnostics=1)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotEqual(accepted(diagnostic, "capacity-512-h1").returncode, 0)
        del diagnostic["dropped_event_delta"]
        self.assertNotEqual(accepted(diagnostic, "capacity-512-h1", diagnostics=1).returncode, 0)


class LifecycleContracts(unittest.TestCase):
    def run_join(self, child, docker_state=None, forced=False):
        helper = shell_function(PERFORMANCE, "record_process_exit") + "\n" + shell_function(PERFORMANCE, "stop_process")
        # Shorten only the timeout for an offline shell fixture. Runtime code
        # retains its ten-second natural-stop window.
        if forced:
            helper = helper.replace('SECONDS + 10', 'SECONDS + 1')
        script = 'set -Eeuo pipefail\nchildren=()\ncontainers=()\ndeclare -A process_containers=()\n' + helper
        with tempfile.TemporaryDirectory() as path:
            script += '\n' + child + ' &\npid=$!\nchildren+=("$pid")\n'
            if docker_state is not None:
                script += 'process_containers[$pid]=fixture\ncontainers+=(fixture)\n'
                script += 'docker() { if [[ $1 == inspect ]]; then printf "%s\\n" "$FIXTURE_STATE"; fi; }\nexport -f docker\n'
                # timeout starts a subprocess, so run the mocked docker function
                # via a shell instead of relying on an executable fixture.
                script += 'timeout() { shift 3; "$@"; }\n'
            script += 'if stop_process "$pid" "$FIXTURE_ROOT/stop"; then status=0; else status=$?; fi\nprintf "status=%s children=%s\\n" "$status" "${#children[@]}"\ncat "$FIXTURE_ROOT/stop.exit.json"\n'
            env = {**os.environ, "FIXTURE_ROOT": path, "FIXTURE_STATE": json.dumps(docker_state)}
            return subprocess.run(["bash", "-c", script], env=env, capture_output=True, text=True, timeout=10)

    def test_emergency_cleanup_cannot_report_success(self):
        helper = shell_function(PERFORMANCE, "cleanup")
        with tempfile.TemporaryDirectory() as path:
            script = ('set -Eeuo pipefail\nchildren=()\nstops=()\ncontainers=()\n'
                      + helper + '\ntrap cleanup EXIT\nbash -c "sleep 0.1" &\nchildren+=("$!")\n')
            result = subprocess.run(["bash", "-c", script], env={**os.environ, "run_dir": path},
                                    capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 1, result.stderr)
            self.assertEqual(json.loads((Path(path) / "status.json").read_text())["exit_status"], 1)

    def test_emergency_container_cleanup_is_one_owned_bounded_batch(self):
        with tempfile.TemporaryDirectory() as path:
            script = ('set -Eeuo pipefail\nchildren=()\nstops=()\ncontainers=(owned-a owned-b owned-c)\n'
                      'timeout() { printf "%s\\n" "$*" >>"$run_dir/timeout.args"; shift 3; "$@"; }\n'
                      'docker() { printf "%s\\n" "$*" >>"$run_dir/docker.args"; return 7; }\n'
                      + shell_function(PERFORMANCE, "cleanup") + '\ntrap cleanup EXIT\n')
            result = subprocess.run(["bash", "-c", script], env={**os.environ, "run_dir": path},
                                    capture_output=True, text=True, timeout=5)
            self.assertEqual(result.returncode, 1, result.stderr)
            self.assertEqual((Path(path) / "timeout.args").read_text().splitlines(),
                             ["-k 1 10 docker rm -f owned-a owned-b owned-c"])
            self.assertEqual((Path(path) / "docker.args").read_text().splitlines(),
                             ["rm -f owned-a owned-b owned-c"])
            self.assertEqual(json.loads((Path(path) / "status.json").read_text()),
                             {"exit_status": 1, "emergency_cleanup": True})

    def test_standing_cleanup_cannot_report_nominal_success(self):
        with tempfile.TemporaryDirectory() as path:
            script = ('set -Eeuo pipefail\nactive_runner_pid=fixture\n'
                      'stop_active_runner() { active_runner_pid=""; }\n'
                      + shell_function(STANDING, "finish") + '\ntrap finish EXIT\n')
            result = subprocess.run(["bash", "-c", script], env={**os.environ, "output_dir": path},
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, 1, result.stderr)
            self.assertEqual(json.loads((Path(path) / "status.json").read_text())["exit_status"], 1)

    def test_capacity_failure_keeps_attempt_identity(self):
        with tempfile.TemporaryDirectory() as path:
            root = Path(path)
            (root / "scripts").mkdir()
            output = root / "output"
            (output / "raw").mkdir(parents=True)
            (output / "results").mkdir()
            runner = root / "scripts/qualify-proxy-performance.sh"
            runner.write_text('#!/bin/bash\necho partial-output\necho failure-diagnostic >&2\nexit 7\n')
            runner.chmod(0o755)
            script = ('set -Eeuo pipefail\n' + shell_function(CAPACITY, "finish") + '\ntrap finish EXIT\n'
                      + shell_function(CAPACITY, "run_attempt") + '\nrun_attempt fixture\n')
            result = subprocess.run(["bash", "-c", script], cwd=root,
                                    env={**os.environ, "output_root": str(output)}, capture_output=True, text=True)
            self.assertEqual(result.returncode, 7, result.stderr)
            self.assertEqual(json.loads((output / "status.json").read_text())["exit_status"], 7)
            attempts = [json.loads(line) for line in (output / "attempts.jsonl").read_text().splitlines()]
            self.assertEqual([record["state"] for record in attempts], ["started", "finished"])
            self.assertEqual(attempts[-1]["exit_status"], 7)
            self.assertTrue(Path(attempts[-1]["raw_directory"]).is_dir())
            self.assertIn("failure-diagnostic", (output / "results/fixture.stderr.log").read_text())

    def test_natural_zero_and_nonzero(self):
        for status in (0, 7):
            result = self.run_join(f"bash -c 'exit {status}'")
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn(f"status={status} children=0", result.stdout)
            record = json.loads(result.stdout.splitlines()[-1])
            self.assertEqual(record["shell_exit_status"], status)
            self.assertFalse(record["forced_cleanup"])
            self.assertEqual(record["natural_exit_verified"], status == 0)

    def test_forced_join_is_rejected_even_if_child_handles_term(self):
        result = self.run_join("bash -c 'trap \"exit 0\" TERM; while :; do sleep 0.05; done'", forced=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("status=1", result.stdout)
        self.assertIn("required forced cleanup", result.stderr)
        record = json.loads(result.stdout.splitlines()[-1])
        self.assertTrue(record["forced_cleanup"])
        self.assertFalse(record["natural_exit_verified"])

    def test_container_state_is_authoritative(self):
        for state, status in (({"Running": False, "ExitCode": 0, "OOMKilled": False}, 0),
                              ({"Running": False, "ExitCode": 7, "OOMKilled": False}, 1),
                              ({"Running": True, "ExitCode": 0, "OOMKilled": False}, 1)):
            result = self.run_join("bash -c 'exit 0'", docker_state=state)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn(f"status={status}", result.stdout)


if __name__ == "__main__":
    unittest.main()

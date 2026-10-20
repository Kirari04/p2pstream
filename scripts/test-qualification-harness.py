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
import shlex
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


# Pure shipped compact-mode validators, without invoking namespace or Go roles.
GROWTH = (ROOT / "scripts/qualify-proxy-agent-growth.sh").read_text()
STARTUP_PY = GROWTH.split("  python3 - \"$@\" <<'STARTUP_PY'\n", 1)[1].split("\nSTARTUP_PY\n", 1)[0]
startup_namespace = {}
startup_tree = ast.parse(STARTUP_PY)
startup_nodes = [node for node in startup_tree.body if isinstance(node, (ast.Import, ast.ImportFrom, ast.FunctionDef))]
exec(compile(ast.Module(body=startup_nodes, type_ignores=[]), "<startup-contracts>", "exec"), startup_namespace)


def startup_fixture():
    phases = [("warmup", direction, n) for direction in ("download", "upload") for n in range(1, 5)]
    phases += [("retained-warmup", direction, 1) for direction in ("download", "upload")]
    lanes = [{"lane": n, "aggregate": True, "receive_limit_bytes": 16777216,
              "receive_available_bytes": 16777216, "total_units": 4096, "free_units": 4096}
             for n in range(5)]
    return [{"agent_tunnels_final": [{"flow_control": "aggregate-v1", "connected": True,
                                     "control_connected": True, "lanes": copy.deepcopy(lanes)}],
             "agent_runtime_after": {"tunnel_sessions": copy.deepcopy(lanes)}, "measurement_stage": stage, "transfer_direction": direction, "repetition": n,
             "transport_mode": "aggregate-v1", "protocol": "h2", "observed_http_major": 2,
             "observed_origin_http_major": 1, "attempted": 1, "useful_completed": 1,
             "errors": 0, "status_classes": {"2xx": 1}, "configured_payload_bytes": 268435456,
             "response_bytes": 268435456 if direction == "download" else 32,
             "event_stats_captured": True, "persisted_event_delta": 1, "wall_seconds": 10,
             "dropped_event_delta": 0, "event_queue_after_drain": 0, "event_drain_error": "",
             "compact_affinities": {"pre": {"roles": {role: {"pid": i+10, "cpu": i, "start_ticks": i+100} for i, role in enumerate(["server", "agent", "origin"])}},
                                    "post": {"roles": {role: {"pid": i+10, "cpu": i, "start_ticks": i+100} for i, role in enumerate(["server", "agent", "origin"])}},
                                    "load": {"roles": {"load": {"pid": 20, "cpu": 3, "start_ticks": 200}}}},
             "server_process": {"role": "server", "pid": 10, "cpu_set": "0", "cpu_seconds": 1, "sampling_seconds": 10},
             "agent_processes": [{"role": role, "pid": i+11, "cpu_set": str(i+1), "cpu_seconds": 1, "sampling_seconds": 10}
                                 for i, role in enumerate(("agent", "origin"))],
             "whole_driver_cpu": {"status": 0, "elapsed_seconds": 10, "user_seconds": 1, "system_seconds": 1}}
            for stage, direction, n in phases]


class RetainedStartupContracts(unittest.TestCase):
    def test_auxiliary_binary_options_and_safe_paths(self):
        select = shell_function(GROWTH, "select_auxiliary_server_binary")
        case = re.search(r"      --auxiliary-server-binary\)\n(.*?) ;;\n", GROWTH, re.S)[1]
        parser = 'auxiliary_server_explicit=0; auxiliary_server_binary=""; startup_mode=$1; shift; while (($#)); do case "$1" in --auxiliary-server-binary)\n' + case + '\n;; *) exit 3;; esac; done'
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            server, auxiliary = root / "server", root / "fixed auxiliary; literal $(name)"
            for binary in (server, auxiliary):
                binary.write_text("#!/usr/bin/bash\nexit 0\n"); binary.chmod(0o700)
            for mode, requested, expected in [(0, "", str(server)), (1, "", str(server)), (1, str(auxiliary), str(auxiliary))]:
                command = select + '\nselect_auxiliary_server_binary "$1" "$2" "$3"'
                result = subprocess.run(["bash", "-c", command, "contract", str(mode), str(server), requested], text=True, capture_output=True, timeout=5)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout, expected + "\n")
            nonexec = root / "not-executable"; nonexec.write_text("data")
            newline = root / "bad\nname"; newline.write_text("x"); newline.chmod(0o700)
            for mode, requested in [(0, str(auxiliary)), (1, str(root / "missing")), (1, str(nonexec)), (1, str(root)), (1, str(newline))]:
                result = subprocess.run(["bash", "-c", select + '\nselect_auxiliary_server_binary "$1" "$2" "$3"', "contract", str(mode), str(server), requested], text=True, capture_output=True, timeout=5)
                self.assertEqual(result.returncode, 2, result.stderr)
            for mode, args in [(1, ["--auxiliary-server-binary"]), (1, ["--auxiliary-server-binary", ""]),
                               (1, ["--auxiliary-server-binary", "--output"]), (0, ["--auxiliary-server-binary", str(auxiliary)]),
                               (1, ["--auxiliary-server-binary", str(auxiliary), "--auxiliary-server-binary", str(auxiliary)])]:
                result = subprocess.run(["bash", "-c", parser, "contract", str(mode), *args], text=True, capture_output=True, timeout=5)
                self.assertEqual(result.returncode, 2, result.stderr)
            result = subprocess.run(["bash", "-c", parser + '\nprintf "%s\\n" "$auxiliary_server_binary"', "contract", "1", "--auxiliary-server-binary", str(auxiliary)], text=True, capture_output=True, timeout=5)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout, str(auxiliary) + "\n")

    def test_auxiliary_binary_routes_origin_driver_but_not_server(self):
        origin = re.search(r"^origin_command=.*\nif \(\(startup_mode\)\);.*\n", GROWTH, re.M)[0]
        endpoint = re.findall(r"^      taskset -c .*TestExternalProxyQualificationServerProcess.*$", GROWTH, re.M)
        self.assertEqual(len(endpoint), 2)
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder); bins = root / "bin"; bins.mkdir()
            server, auxiliary = root / "selected server", root / "fixed auxiliary"
            for binary in (server, auxiliary):
                binary.write_text('#!/usr/bin/bash\nprintf "%s\\n" "$0 $*" >> "$CALL_LOG"\n')
                binary.chmod(0o700)
            # Strip the time wrapper only; execute the shipped taskset/binary
            # command using a tiny local taskset stand-in, with no real Go role.
            for name, body in {"setsid": 'shift 6; exec "$@"', "taskset": '[[ $1 == -c ]]; shift 2; exec "$@"'}.items():
                tool = bins / name; tool.write_text("#!/usr/bin/bash\nset -e\n" + body + "\n"); tool.chmod(0o700)
            env = {"PATH": str(bins) + ":/usr/bin:/bin", "CALL_LOG": str(root / "calls")}
            code = '\n'.join([shell_function(GROWTH, "startup_affinity_snapshot"), shell_function(GROWTH, "run_growth_load"),
                'startup_python() { if [[ $1 == load-snapshot ]]; then printf "%s\\n" "$4" > "$CHECK_BINARY"; else printf "%s\\n" "$3" >> "$SNAPSHOTS"; fi; }',
                'startup_mode=1; children=(); load_groups=(); server_pid=101; agent_pid=102; origin_pid=103;',
                'server_cpu_set=8; agent_cpu_set=9; origin_cpu_set=10; load_cpu_set=11; transfer_timeout_seconds=1; overall_timeout_seconds=1;',
                'server_binary=$1; aggregate_agent_binary=$1; auxiliary_server_binary=$2; CHECK_BINARY=$3; SNAPSHOTS=$4;',
                origin, '"${origin_command[@]}" origin-marker', 'run_growth_load "$5"',
                endpoint[1].rstrip().removesuffix('\\'), 'startup_affinity_snapshot ignored 777'])
            result = subprocess.run(["bash", "-e", "-c", code, "contract", str(server), str(auxiliary), str(root / "binary-check"), str(root / "snapshots"), str(root / "load")], env=env, text=True, capture_output=True, timeout=5)
            self.assertEqual(result.returncode, 0, result.stderr)
            calls = (root / "calls").read_text().splitlines()
            self.assertEqual(len(calls), 3)
            self.assertEqual(calls[0], str(auxiliary) + " origin-marker")
            self.assertTrue(calls[1].startswith(str(auxiliary) + " -test.run ^TestProxyQualificationLoad$ "))
            self.assertTrue(calls[2].startswith(str(server) + " -test.run ^TestExternalProxyQualificationServerProcess$ "))
            self.assertEqual((root / "binary-check").read_text(), str(auxiliary) + "\n")
            snapshots = [json.loads(line) for line in (root / "snapshots").read_text().splitlines()]
            for roles in snapshots:
                self.assertEqual(roles["server"][2], str(server))
                self.assertEqual(roles["agent"][2], str(server))
                self.assertEqual(roles["origin"][2], str(auxiliary))
            self.assertEqual(snapshots[-1]["load"][2], str(auxiliary))
            receipt = json.loads((root / "load.load-exit.json").read_text())
            self.assertEqual(receipt["status"], 0)
            self.assertTrue(receipt["joined"])
            self.assertFalse(receipt["forced"])

    def test_compact_options_and_noncontiguous_cpu_mapping(self):
        parse = startup_namespace["compact_options"]
        self.assertEqual(parse(["--retained-startup-ci"], [3, 5, 7, 9, 11]),
                         {"server": 3, "agent": 5, "origin": 7, "load": 9})
        args = ["--retained-startup-ci", "--server-cpus", "9", "--agent-cpus", "7",
                "--origin-cpus", "5", "--load-cpus", "3"]
        self.assertEqual(parse(args, [3, 5, 7, 9])["server"], 9)
        invalid = [[], ["--retained-startup-ci"] * 2,
                   ["--retained-startup-ci", "--server-cpus", "3"],
                   args[:-1] + ["7"], args[:-1] + ["11"], args[:-1] + ["3-5"],
                   args[:-1] + ["-1"], args[:-1] + ["01"], args + ["--load-cpus", "3"]]
        invalid += [["--retained-startup-ci", flag, "1"] for flag in
                    ["--runs", "--rtt-ms", "--warmup-mib", "--steady-mib", "--required-pool-mib",
                     "--transfer-repetitions", "--pressure", "--tiny-response", "--smoke",
                     "--legacy-agent-binary", "--legacy-agent-source-ref"]]
        for values in invalid:
            with self.subTest(values=values), self.assertRaises(ValueError):
                parse(values, [3, 5, 7, 9])
        with self.assertRaises(ValueError):
            parse(["--retained-startup-ci"], [3, 5, 7])

    def test_quota_walk_rejects_restrictive_ancestor(self):
        walk = startup_namespace["quota_chain"]
        files = {Path("/cg/job/leaf/cpu.max"): "max 100000",
                 Path("/cg/job/cpu.max"): "400000 100000",
                 Path("/cg/cgroup.controllers"): "cpu memory pids",
                 Path("/cg/cgroup.subtree_control"): "cpu memory pids",
                 Path("/cg/job/cgroup.controllers"): "cpu memory pids",
                 Path("/cg/job/cgroup.subtree_control"): "cpu memory pids",
                 Path("/cg/job/leaf/cgroup.controllers"): "cpu memory pids"}
        def reader(values):
            def read(path):
                if path not in values: raise FileNotFoundError(path)
                return values[path]
            return read
        rows = walk("/cg/job/leaf", "/cg", reader(files))
        self.assertEqual(len(rows), 3)
        self.assertEqual(rows[-1]["interface_state"], "hierarchy_root_exempt")
        self.assertEqual(rows[-1]["true_root_proof"]["cgroup.type_absence"], "FileNotFoundError")
        for root_type in ["domain", "threaded", "domain threaded", ""]:
            with self.subTest(root_type=root_type), self.assertRaises(ValueError):
                walk("/cg/job/leaf", "/cg", reader({**files, Path("/cg/cgroup.type"): root_type}))
        for unreadable in [Path("/cg/cgroup.type"), Path("/cg/cgroup.controllers"), Path("/cg/cgroup.subtree_control")]:
            def denied(path):
                if path == unreadable: raise PermissionError(path)
                return reader(files)(path)
            with self.subTest(unreadable=unreadable), self.assertRaises(PermissionError):
                walk("/cg/job/leaf", "/cg", denied)
        for value in ["399999 100000", "0 100000", "max 0", "x 100000", "max", "-1 100000"]:
            with self.subTest(value=value), self.assertRaises(ValueError):
                walk("/cg/job/leaf", "/cg", reader({**files, Path("/cg/job/cpu.max"): value}))
        with self.assertRaises(ValueError):
            walk("/outside", "/cg", reader(files))
        unavailable = dict(files); del unavailable[Path("/cg/job/leaf/cpu.max")]
        with self.assertRaises(ValueError):
            walk("/cg/job/leaf", "/cg", reader(unavailable))
        disabled = {**unavailable, Path("/cg/job/cgroup.subtree_control"): "memory pids",
                    Path("/cg/job/leaf/cgroup.controllers"): "memory pids"}
        self.assertEqual(walk("/cg/job/leaf", "/cg", reader(disabled))[0]["interface_state"], "CPU_controller_disabled_here")
        # Disabled leaf does not erase its still-enabled two-CPU ancestor limit.
        with self.assertRaises(ValueError):
            walk("/cg/job/leaf", "/cg", reader({**disabled, Path("/cg/job/cpu.max"): "200000 100000"}))
        unknown = dict(disabled); del unknown[Path("/cg/job/cgroup.subtree_control")]
        with self.assertRaises(FileNotFoundError):
            walk("/cg/job/leaf", "/cg", reader(unknown))
        with self.assertRaises(ValueError):
            walk("/cg/job/leaf", "/cg", reader({**disabled, Path("/cg/job/leaf/cpu.max"): "max 100000"}))

    def test_neighbor_and_shaping_readbacks(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            records = {
                "server-links.json": [{"ifname": "growth-server", "address": "aa"}],
                "agent-links.json": [{"ifname": "growth-agent", "address": "bb"}],
                "router-links.json": [{"ifname": "growth-rs", "address": "cc"}, {"ifname": "growth-rr", "address": "dd"}],
                "server-neighbors.json": [{"dev": "growth-server", "dst": "10.211.0.2", "lladdr": "cc", "state": ["REACHABLE"]}],
                "agent-neighbors.json": [{"dev": "growth-agent", "dst": "10.212.0.1", "lladdr": "dd", "state": ["REACHABLE"]}],
                "router-neighbors.json": [{"dev": "growth-rs", "dst": "10.211.0.1", "lladdr": "aa", "state": ["REACHABLE"]}, {"dev": "growth-rr", "dst": "10.212.0.2", "lladdr": "bb", "state": ["REACHABLE"]}]}
            for dev in ["growth-server", "growth-agent", "growth-rs", "growth-rr"]:
                records[dev + "-unshaped.json"] = [{"kind": "noqueue"}]
            shaped = [{"kind": "netem", "root": True, "options": {"limit": 100000, "delay": {"delay": .04, "jitter": 0, "correlation": 0}, "rate": {"rate": 125000000}, "ecn": False, "gap": 0}}]
            for dev in ["growth-rs", "growth-rr"]:
                records[dev + "-shaped.json"] = copy.deepcopy(shaped)
            def install(values):
                for name, data in values.items():
                    (root / name).write_text(json.dumps(data))
            install(records)
            startup_namespace["network_check"](root)
            # Unfiltered capture may include unrelated interfaces; required peers
            # still need explicit dev/dst/MAC identity rather than assumed dev.
            unrelated = copy.deepcopy(records)
            for side in ("server", "agent"):
                unrelated[side + "-neighbors.json"].append({"dev": "unrelated0", "dst": "192.0.2.1", "lladdr": "ee", "state": ["REACHABLE"]})
            install(unrelated)
            startup_namespace["network_check"](root)
            for side in ("server", "agent"):
                missing_dev = copy.deepcopy(records)
                del missing_dev[side + "-neighbors.json"][0]["dev"]
                install(missing_dev)
                with self.subTest(missing_dev=side), self.assertRaises(KeyError):
                    startup_namespace["network_check"](root)
            for key, value in [("limit", 1), ("ecn", True), ("loss", .001), ("reorder", {"probability": .1})]:
                mutated = copy.deepcopy(records)
                mutated["growth-rs-shaped.json"][0]["options"][key] = value
                install(mutated)
                with self.subTest(key=key), self.assertRaises(ValueError):
                    startup_namespace["network_check"](root)
            for key, value in [("lladdr", "wrong-peer"), ("state", ["FAILED"])]:
                mutated = copy.deepcopy(records); mutated["server-neighbors.json"][0][key] = value
                install(mutated)
                with self.assertRaises(ValueError):
                    startup_namespace["network_check"](root)

    def test_exact_ten_rows_and_required_event_zeros(self):
        result = startup_namespace["startup_summary"](startup_fixture())
        self.assertTrue(result["numerical_ratio_gate_pass"])
        self.assertTrue(result["reference_stable"])
        self.assertFalse(result["CPU_confounded"])
        self.assertFalse(result["performance_qualified"])
        for rows in [startup_fixture()[:-1], startup_fixture() + [startup_fixture()[0]],
                     [startup_fixture()[0]] * 10, startup_fixture()[::-1]]:
            with self.assertRaises(ValueError):
                startup_namespace["startup_summary"](rows)

    def test_ratio_boundary_is_exact_not_decimal_rounding(self):
        rows = startup_fixture()
        for row in rows:
            row["wall_seconds"] = 9 if row["measurement_stage"] == "warmup" else 10
        self.assertTrue(startup_namespace["startup_summary"](rows)["numerical_ratio_gate_pass"])
        rows[-1]["wall_seconds"] = 10.000001
        self.assertFalse(startup_namespace["startup_summary"](rows)["numerical_ratio_gate_pass"])

    def test_control_spread_boundary_and_inconclusive(self):
        rows = startup_fixture()
        for i in (0, 1, 4, 5):
            rows[i]["wall_seconds"] = 11
        self.assertTrue(startup_namespace["startup_summary"](rows)["reference_stable"])
        rows[0]["wall_seconds"] = 11.000001
        self.assertFalse(startup_namespace["startup_summary"](rows)["reference_stable"])

    def test_typed_body_protocol_event_and_finite_inputs(self):
        mutations = {"attempted": [True, 0, "1"], "useful_completed": [0], "errors": [1],
                     "repetition": [True, 0], "configured_payload_bytes": [1, True], "response_bytes": [1],
                     "protocol": ["h1"], "observed_http_major": [1, True], "observed_origin_http_major": [2],
                     "event_stats_captured": [False, 1], "persisted_event_delta": [0, True],
                     "dropped_event_delta": [1, True], "event_queue_after_drain": [1, False],
                     "event_drain_error": ["failure", None], "event_stats_error": ["capture failed"], "status_classes": [{"2xx": True}, {"2xx": 1, "5xx": 1}],
                     "wall_seconds": [0, -1, True, "10", float("nan"), float("inf")]}
        for key, values in mutations.items():
            for value in values:
                rows = startup_fixture(); rows[0][key] = value
                with self.subTest(key=key, value=value), self.assertRaises((ValueError, KeyError, TypeError)):
                    startup_namespace["startup_summary"](rows)
        for key in ["event_stats_captured", "persisted_event_delta", "observed_origin_http_major", "response_bytes", "wall_seconds", "dropped_event_delta", "event_queue_after_drain", "event_drain_error"]:
            rows = startup_fixture(); del rows[0][key]
            with self.subTest(missing=key), self.assertRaises(KeyError):
                startup_namespace["startup_summary"](rows)

    def test_retained_ledger_reader_preserves_live_pending_contract(self):
        rows = startup_fixture()
        for endpoint in [rows[-1]["agent_tunnels_final"][0]["lanes"], rows[-1]["agent_runtime_after"]["tunnel_sessions"]]:
            endpoint[1]["pending_refund_bytes"] = 4096
            endpoint[1]["receive_available_bytes"] -= 4096
        self.assertTrue(startup_namespace["startup_summary"](rows)["correctness"])
        for mutation in ("credit", "pool", "lane", "typed"):
            rows = startup_fixture(); lane = rows[-1]["agent_runtime_after"]["tunnel_sessions"][1]
            if mutation == "credit": lane["receive_available_bytes"] -= 4096
            elif mutation == "pool": lane["free_units"] -= 1
            elif mutation == "lane": lane["lane"] = 2
            else: lane["buffered_units"] = False
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                startup_namespace["startup_summary"](rows)

    def test_CPU_confounded_and_missing_role_receipts(self):
        rows = startup_fixture(); rows[0]["server_process"]["cpu_seconds"] = 8
        self.assertTrue(startup_namespace["startup_summary"](rows)["CPU_confounded"])
        rows = startup_fixture(); rows[0]["whole_driver_cpu"]["user_seconds"] = 7
        self.assertTrue(startup_namespace["startup_summary"](rows)["CPU_confounded"])
        for key, value in [("sampling_seconds", 0), ("sampling_seconds", float("inf")), ("cpu_seconds", True), ("cpu_seconds", 0), ("pid", 99), ("cpu_set", "3")]:
            rows = startup_fixture(); rows[0]["server_process"][key] = value
            with self.assertRaises(ValueError):
                startup_namespace["startup_summary"](rows)
        rows = startup_fixture(); del rows[0]["server_process"]["cpu_seconds"]
        with self.assertRaises(KeyError):
            startup_namespace["startup_summary"](rows)
        rows = startup_fixture(); rows[0]["whole_driver_cpu"]["user_seconds"] = 0; rows[0]["whole_driver_cpu"]["system_seconds"] = 0
        with self.assertRaises(ValueError):
            startup_namespace["startup_summary"](rows)
        rows = startup_fixture(); rows[0]["agent_processes"] = []
        with self.assertRaises(ValueError):
            startup_namespace["startup_summary"](rows)
        for value in [1, True, None]:
            rows = startup_fixture(); rows[0]["whole_driver_cpu"]["status"] = value
            with self.assertRaises(ValueError):
                startup_namespace["startup_summary"](rows)

    def test_adapter_rejects_dirty_or_nonignored_untracked_source(self):
        adapter = (ROOT / "scripts/test-proxy-retained-startup.sh").read_text()
        helper = shell_function(adapter, "source_clean")
        for dirty, untracked, expected in [(0, "", 0), (1, "", 1), (0, "new.go", 1)]:
            code = helper + '\ngit() { if [[ $1 == diff ]]; then return "$DIRTY"; else printf "%s" "$UNTRACKED"; fi; }\nsource_clean\n'
            result = subprocess.run(["bash", "-c", code], env={**os.environ, "DIRTY": str(dirty), "UNTRACKED": untracked}, capture_output=True, text=True, timeout=2)
            self.assertEqual(result.returncode, expected)

    def test_natural_stop_distinguishes_nonzero_and_forced(self):
        helper = shell_function(GROWTH, "startup_stop_process")
        for kind in ("zero", "nonzero", "forced"):
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as folder:
                child = "exit 0" if kind == "zero" else "exit 7"
                code = "set -u\nchildren=()\n" + helper
                if kind == "forced":
                    # Shorten only the grace interval, never alter the shipped verdict.
                    code = code.replace("SECONDS + 10", "SECONDS")
                    child = 'trap "exit 0" TERM; : >"$FIXTURE_STOP.ready"; while :; do sleep .05; done'
                code += '\nbash -c ' + shlex.quote(child) + ' &\npid=$!\nchildren+=("$pid")\n'
                if kind == "forced":
                    code += 'for ((i=0;i<100;i++)); do [[ -f $FIXTURE_STOP.ready ]] && break; sleep .01; done\n[[ -f $FIXTURE_STOP.ready ]] || exit 9\n'
                code += 'if startup_stop_process "$pid" "$FIXTURE_STOP"; then status=0; else status=$?; fi\n'
                code += 'printf "status=%s children=%s\\n" "$status" "${#children[@]}"\ncat "$FIXTURE_STOP.exit.json"\n'
                result = subprocess.run(["bash", "-c", code], env={**os.environ, "FIXTURE_STOP": str(Path(folder) / "stop")}, capture_output=True, text=True, timeout=5)
                self.assertEqual(result.returncode, 0, result.stderr)
                record = json.loads(result.stdout.splitlines()[-1])
                self.assertTrue(record["joined"])
                self.assertEqual(record["forced"], kind == "forced")
                self.assertIn("status=0" if kind == "zero" else ("status=7" if kind == "nonzero" else "status=1"), result.stdout)
                self.assertEqual(record["status"], 7 if kind == "nonzero" else 0)


if __name__ == "__main__":
    unittest.main()

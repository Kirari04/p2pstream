#!/usr/bin/env python3
"""Run one bounded production-versus-native-Go HTTP/H1/c16 profiling pair."""
import argparse
from datetime import datetime, timezone
import gzip
import hashlib
import json
import math
import os
from pathlib import Path
import re
import shutil
import signal
import socket
import ssl
import subprocess
import sys
import tempfile
import time
from urllib.parse import urlparse

EXPECTED_BASE = "248bad099682878fe398c0ebaad235787ef47858"
EXPECTED_BASELINE_RUNNER_SHA256 = "ebda90041f65bcdd317b0cf9aae10c6b334190d35653ce64b2147697375f7a50"
EXPECTED_BASELINE_MANIFEST_SHA256 = "bc915ed4822c9bfca83b78083b071aa01e3dd9db851d214cbcdf11e32271bfdf"
EXPECTED_BASELINE_CHECKPOINT_SHA256 = "c0e8ab560d6e2a961ab91871fd459bfc2bf4b0e09e30fa7e094666e234f918fe"
PROXY_CPUS = 4
MEMORY_BYTES = 4 << 30
SMOKE_TIMEOUT_SECONDS = 125
LOAD_TIMEOUT_SECONDS = 125
DOCKER_TIMEOUT_SECONDS = 30
OVERALL_TIMEOUT_SECONDS = 1200
QUALIFICATION_ENV_PREFIX = "P2PSTREAM_QUALIFICATION_"


def sha(path):
    digest = hashlib.sha256()
    with Path(path).open("rb") as source:
        for block in iter(lambda: source.read(1 << 20), b""):
            digest.update(block)
    return digest.hexdigest()


def utc():
    return datetime.now(timezone.utc).isoformat()


def resolve_command(command, env=None, cwd=None):
    requested = [str(value) for value in command]
    if not requested:
        raise ValueError("empty command")
    program = requested[0]
    if os.path.isabs(program):
        resolved = Path(program).resolve(strict=True)
    elif os.sep in program:
        if cwd is None:
            raise ValueError("relative command with a slash requires an explicit child cwd")
        child_cwd = Path(cwd).resolve(strict=True)
        resolved = (child_cwd / program).resolve(strict=True)
    else:
        if env is not None:
            path = env.get("PATH")
            if path is None:
                raise FileNotFoundError(f"child environment has no PATH for {program!r}")
        else:
            path = os.environ.get("PATH")
        found = shutil.which(program, path=path)
        if found is None:
            raise FileNotFoundError(f"executable not found on child PATH: {program!r}")
        resolved = Path(found).resolve(strict=True)
    if not resolved.is_file() or not os.access(resolved, os.X_OK):
        raise PermissionError(f"command is not an executable file: {resolved}")
    return requested, [str(resolved)] + requested[1:]


def clean_qualification_environment(source):
    return {key: value for key, value in source.items() if not key.startswith(QUALIFICATION_ENV_PREFIX)}


def qualification_child_environment(source, smoke=None):
    environment = clean_qualification_environment(source)
    if smoke is not None:
        environment["P2PSTREAM_QUALIFICATION_NATIVE_GO_SMOKE"] = "1" if smoke else "0"
    return environment


def attribution_resource_envelope(role):
    if role == "production":
        return "p2pstream-native-go-attribution:4GiB,no-swap"
    if role == "native":
        return "native-go-attribution:4GiB,no-swap"
    raise ValueError(f"unknown attribution role: {role!r}")


def attribution_load_environment(source, smoke):
    environment = qualification_child_environment(source, smoke=smoke)
    environment["P2PSTREAM_QUALIFICATION_NATIVE_GO_ATTRIBUTION"] = "1"
    return environment


def parse_container_inventory(returncode, stdout):
    if returncode != 0:
        raise ValueError(f"container inventory failed with exit {returncode}")
    rows = []
    for line in stdout.splitlines():
        fields = line.split("\t")
        if len(fields) != 2 or not re.fullmatch(r"[0-9a-f]{64}", fields[0]) or not fields[1]:
            raise ValueError(f"malformed container inventory row: {line!r}")
        rows.append(dict(id=fields[0], name=fields[1]))
    if len({row["id"] for row in rows}) != len(rows) or len({row["name"] for row in rows}) != len(rows):
        raise ValueError("duplicate container ID or name in inventory")
    return rows


def validate_owned_container_absent(inventory, name, container_id=None):
    for row in inventory:
        if row["name"] == name or (container_id is not None and row["id"] == container_id):
            raise ValueError(f"owned container remains: {name} ({container_id or 'unknown ID'})")


def validate_attribution_result_configuration(result, concurrency, duration, warmup):
    if (result.get("protocol") != "h1" or result.get("concurrency") != concurrency
            or result.get("configured_duration_seconds", 0) != duration
            or result.get("warmup_seconds", 0) != warmup):
        raise ValueError("load result configuration differs from the requested cell")


def expected_profile_cells():
    return (("production", "http", 16), ("native", "http", 16))


def validate_profile_row_chronology(rows):
    actual = tuple((row.get("role"), row.get("public_scheme"), row.get("concurrency")) for row in rows)
    if actual != expected_profile_cells():
        raise ValueError("rows do not match the exact production-then-native HTTP/H1/c16 pair")


def profile_output_names(label):
    if not re.fullmatch(r"[a-z0-9][a-z0-9-]*", label):
        raise ValueError(f"unsafe profile label: {label!r}")
    return {"cpu": label + ".cpu.pprof", "mem": label + ".mem.pprof"}


def profile_test_flags(label):
    names = profile_output_names(label)
    return ["-test.cpuprofile=/qualification/" + names["cpu"],
            "-test.memprofile=/qualification/" + names["mem"]]


def warmup_timed_completions(result):
    warmup = result.get("warmup_requests")
    timed = result.get("useful_completed")
    if (not isinstance(warmup, int) or isinstance(warmup, bool) or warmup < 1
            or not isinstance(timed, int) or isinstance(timed, bool) or timed < 1):
        raise ValueError("profile normalization requires positive integer warmup and timed completions")
    return warmup + timed


def _read_varint(data, offset):
    value = 0
    shift = 0
    while offset < len(data) and shift < 70:
        byte = data[offset]
        offset += 1
        value |= (byte & 0x7f) << shift
        if not byte & 0x80:
            return value, offset
        shift += 7
    raise ValueError("truncated or oversized profile varint")


def _profile_fields(data):
    offset = 0
    while offset < len(data):
        tag, offset = _read_varint(data, offset)
        field = tag >> 3
        wire = tag & 7
        if field == 0:
            raise ValueError("invalid zero profile field")
        if wire == 0:
            value, offset = _read_varint(data, offset)
        elif wire == 1:
            if offset + 8 > len(data):
                raise ValueError("truncated fixed64 profile field")
            value = data[offset:offset + 8]
            offset += 8
        elif wire == 2:
            size, offset = _read_varint(data, offset)
            if offset + size > len(data):
                raise ValueError("truncated length-delimited profile field")
            value = data[offset:offset + size]
            offset += size
        elif wire == 5:
            if offset + 4 > len(data):
                raise ValueError("truncated fixed32 profile field")
            value = data[offset:offset + 4]
            offset += 4
        else:
            raise ValueError(f"unsupported profile protobuf wire type {wire}")
        yield field, wire, value


def _packed_int64(data):
    values = []
    offset = 0
    while offset < len(data):
        value, offset = _read_varint(data, offset)
        if value >= 1 << 63:
            value -= 1 << 64
        values.append(value)
    return values


def profile_sample_totals(path):
    """Return pprof sample totals while rejecting empty, truncated, or inconsistent profiles."""
    path = Path(path)
    if not path.is_file() or path.stat().st_size < 1:
        raise ValueError(f"missing or empty profile: {path}")
    try:
        payload = gzip.decompress(path.read_bytes())
    except (OSError, EOFError) as error:
        raise ValueError(f"invalid gzip profile: {path}") from error
    sample_type_messages = []
    sample_messages = []
    strings = []
    for field, wire, value in _profile_fields(payload):
        if field == 1 and wire == 2:
            sample_type_messages.append(value)
        elif field == 2 and wire == 2:
            sample_messages.append(value)
        elif field == 6 and wire == 2:
            try:
                strings.append(value.decode("utf-8"))
            except UnicodeDecodeError as error:
                raise ValueError(f"invalid profile string table: {path}") from error
    if not sample_type_messages or not sample_messages or not strings or strings[0] != "":
        raise ValueError(f"incomplete profile protobuf: {path}")
    sample_types = []
    for message in sample_type_messages:
        indexes = {field: value for field, wire, value in _profile_fields(message) if wire == 0 and field in (1, 2)}
        if set(indexes) != {1, 2} or any(index < 0 or index >= len(strings) for index in indexes.values()):
            raise ValueError(f"invalid profile sample type: {path}")
        sample_types.append((strings[indexes[1]], strings[indexes[2]]))
    totals = [0] * len(sample_types)
    for message in sample_messages:
        values = []
        for field, wire, value in _profile_fields(message):
            if field == 2 and wire == 0:
                values.append(value if value < 1 << 63 else value - (1 << 64))
            elif field == 2 and wire == 2:
                values.extend(_packed_int64(value))
        if len(values) != len(totals):
            raise ValueError(f"profile sample width differs from sample types: {path}")
        totals = [total + value for total, value in zip(totals, values)]
    if not any(total > 0 for total in totals):
        raise ValueError(f"profile contains no positive samples: {path}")
    return {sample_type + ":" + unit: total for (sample_type, unit), total in zip(sample_types, totals)}


def process_group_alive(group):
    if group is None:
        return False
    try:
        os.killpg(group, 0)
        return True
    except ProcessLookupError:
        return False


def terminate_process_group(group, process, term_seconds=15, kill_seconds=5):
    outcome = dict(group=group, term_sent=False, kill_sent=False,
                   direct_process_reaped=False, group_alive_after_cleanup=False)
    if process_group_alive(group):
        try:
            os.killpg(group, signal.SIGTERM)
            outcome["term_sent"] = True
        except ProcessLookupError:
            pass
        deadline = time.monotonic() + term_seconds
        while process_group_alive(group) and time.monotonic() < deadline:
            if process is not None:
                process.poll()
            time.sleep(.05)
        if process_group_alive(group):
            try:
                os.killpg(group, signal.SIGKILL)
                outcome["kill_sent"] = True
            except ProcessLookupError:
                pass
    if process is not None and process.poll() is None:
        try:
            process.wait(timeout=kill_seconds)
        except subprocess.TimeoutExpired:
            pass
    deadline = time.monotonic() + kill_seconds
    while process_group_alive(group) and time.monotonic() < deadline:
        time.sleep(.05)
    outcome["direct_process_reaped"] = process is not None and process.returncode is not None
    outcome["group_alive_after_cleanup"] = process_group_alive(group)
    return outcome


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("bundle", help="completed build-native-go-attribution.py output root")
    parser.add_argument("--smoke-only", action="store_true")
    parser.add_argument("--output-parent", required=True,
                        help="existing disk-backed directory for the new diagnostic run")
    parser.add_argument("--lineage-manifest", default=str(Path(__file__).with_name("profile-derivative-manifest.json")))
    parser.add_argument("--lineage-manifest-sha256", required=True,
                        help="reviewed SHA-256 pin for the derivative lineage manifest")
    args = parser.parse_args()
    bundle = Path(args.bundle).resolve(strict=True)
    output_parent = Path(args.output_parent).resolve(strict=True)
    if not output_parent.is_dir():
        parser.error("output parent must be a directory")
    build_manifest_path = bundle / "manifest.json"
    if sha(build_manifest_path) != EXPECTED_BASELINE_MANIFEST_SHA256:
        parser.error("frozen baseline build manifest changed")
    build_manifest = json.loads(build_manifest_path.read_text())
    if build_manifest.get("kind") != "p2pstream-native-go-attribution-build-v1" or build_manifest.get("source_ref") != EXPECTED_BASE:
        parser.error("unexpected build bundle kind or immutable base")
    binary = Path(build_manifest["binary"]["path"]).resolve(strict=True)
    runner = Path(__file__).resolve(strict=True)
    baseline_runner = (bundle / build_manifest["runner"]["path"]).resolve(strict=True)
    lineage_manifest_path = Path(args.lineage_manifest).resolve(strict=True)
    if not re.fullmatch(r"[0-9a-f]{64}", args.lineage_manifest_sha256):
        parser.error("lineage manifest SHA-256 pin must be 64 lowercase hexadecimal characters")
    if sha(lineage_manifest_path) != args.lineage_manifest_sha256:
        parser.error("derivative lineage manifest differs from the reviewed invocation pin")
    lineage = json.loads(lineage_manifest_path.read_text())
    if (lineage.get("kind") != "p2pstream-native-go-profile-derivative-v1"
            or lineage.get("classification") != "diagnostic-only"
            or lineage.get("baseline", {}).get("source_ref") != EXPECTED_BASE
            or lineage.get("baseline", {}).get("manifest_sha256") != EXPECTED_BASELINE_MANIFEST_SHA256
            or lineage.get("baseline", {}).get("runner_sha256") != EXPECTED_BASELINE_RUNNER_SHA256
            or lineage.get("baseline", {}).get("source_checkpoint_sha256") != EXPECTED_BASELINE_CHECKPOINT_SHA256
            or lineage.get("derivative", {}).get("runner_sha256") != sha(runner)):
        parser.error("derivative lineage manifest does not bind the reviewed baseline and runner")
    derivative_patch = (lineage_manifest_path.parent / lineage["derivative"]["patch_path"]).resolve(strict=True)
    if (not derivative_patch.is_relative_to(lineage_manifest_path.parent)
            or sha(derivative_patch) != lineage["derivative"]["patch_sha256"]):
        parser.error("derivative patch differs from its lineage binding")
    patch = bundle / build_manifest["source_overlay"]["path"]
    checkpoint_path = bundle / "source-checkpoint.json"
    settings_path = bundle / "native-go-effective-settings.md"
    plan_path = bundle / "native-go-attribution-plan.md"
    builder_path = bundle / "builder.py.txt"
    contracts_path = bundle / "native-go-attribution-contracts.py"
    archive_path = bundle / build_manifest["source_archive"]["path"]
    if not binary.is_relative_to(bundle) or not baseline_runner.is_relative_to(bundle):
        parser.error("binary and baseline runner must be inside the immutable build bundle")
    for path, expected in ((binary, build_manifest["binary"]["sha256"]),
                           (patch, build_manifest["source_overlay"]["sha256"]),
                           (baseline_runner, build_manifest["runner"]["sha256"]),
                           (checkpoint_path, build_manifest["source_checkpoint_sha256"]),
                           (settings_path, build_manifest["effective_settings_sha256"]),
                           (plan_path, build_manifest["plan_sha256"]),
                           (builder_path, build_manifest["builder_sha256"]),
                           (contracts_path, build_manifest["contracts"]["sha256"]),
                           (archive_path, build_manifest["source_archive"]["sha256"])):
        if sha(path) != expected:
            parser.error(f"frozen build input changed: {path}")
    checkpoint = json.loads(checkpoint_path.read_text())
    checkpoint_files = checkpoint.get("files") or {}
    if (checkpoint.get("kind") != "p2pstream-native-go-attribution-source-checkpoint-v2"
            or checkpoint.get("base_ref") != EXPECTED_BASE
            or checkpoint_files.get("native-go-attribution.patch") != sha(patch)
            or checkpoint_files.get("qualify-proxy-native-go-attribution.py") != sha(baseline_runner)
            or checkpoint_files.get("build-native-go-attribution.py") != sha(builder_path)
            or checkpoint_files.get("native-go-attribution-contracts.py") != sha(contracts_path)
            or checkpoint["inputs"]["plan"]["sha256"] != sha(plan_path)
            or checkpoint["inputs"]["effective_settings"]["sha256"] != sha(settings_path)):
        parser.error("source checkpoint does not bind the runner/builder/patch/settings/plan")
    if any(build_manifest.get("applied_source_files", {}).get(path) != checkpoint_files.get(path)
           for path in ("internal/server/proxy_qualification_test.go",
                        "internal/server/native_go_attribution_qualification_linux_test.go")):
        parser.error("applied Go source hashes differ from the frozen checkpoint")
    build_info = build_manifest["binary"].get("build_info", "")
    if ("-tags" in build_info or not build_info.splitlines()
            or not build_info.splitlines()[0].endswith(": go1.26.6")
            or not build_manifest["binary"].get("build_id")):
        parser.error("binary is not the pinned ordinary Go1.26.6 build")
    if (not build_manifest.get("complete") or build_manifest.get("errors") or build_manifest.get("cleanup")
            or build_manifest["build"]["exit_code"] != 0 or build_manifest["build"]["timed_out"]
            or build_manifest["build"]["forced_group_cleanup"]):
        parser.error("build bundle is not a clean successful build")
    for tool in ("docker", "openssl", "taskset", "findmnt", "lscpu"):
        if not shutil.which(tool):
            parser.error(f"{tool} is required")
    cpus = set(os.sched_getaffinity(0))
    if not set(range(16)).issubset(cpus):
        parser.error("the fixed CPU envelope requires available CPUs 0-15")
    proxy_cpus = "0,1,2,3"
    load_cpus = ",".join(map(str, range(4, 16)))
    threads = PROXY_CPUS
    run_root = Path(tempfile.mkdtemp(prefix="native-profile-run.", dir=output_parent))
    run_root.chmod(0o700)
    temp = run_root / "tmp"
    temp.mkdir()
    env_base = qualification_child_environment(os.environ)
    for key in ("TMPDIR", "TEMP", "TMP", "GOTMPDIR"):
        env_base[key] = str(temp)
    env_base.update(GOFLAGS="", GOWORK="off", GOTOOLCHAIN="local")
    source_identity = EXPECTED_BASE + "+sha256:" + build_manifest["source_overlay"]["sha256"]
    derivative_identity = "sha256:" + sha(runner)
    results_path = run_root / "results.jsonl"
    attempted_results_path = run_root / "attempted-results.jsonl"
    smoke_path = run_root / "smoke.json"
    provenance_path = run_root / "provenance.json"
    processes = []
    process_records = []
    command_history = []
    containers = set()
    all_container_identities = {}
    container_states = {}
    logs = []
    forced_cleanup = []
    origin_stop = run_root / "origin.stop"
    origin = None
    sequence = 0
    environment_evidence = {}
    runner_shim = run_root / "runner-exec-shim.py"
    runner_shim.write_text(
        "#!/usr/bin/env python3\n"
        "import os\n"
        "import signal\n"
        "import sys\n\n"
        "mask = {int(value) for value in sys.argv[1].split(',') if value}\n"
        "signal.pthread_sigmask(signal.SIG_SETMASK, mask)\n"
        "os.execve(sys.argv[2], sys.argv[2:], os.environ)\n"
    )
    runner_shim.chmod(0o700)
    shim_python = Path(sys.executable).resolve(strict=True)

    def save_provenance(extra=None):
        provenance = dict(kind="p2pstream-native-go-profile-run-v1", classification="diagnostic-only",
                          source_ref=EXPECTED_BASE, source_identity=source_identity,
                          derivative_identity=derivative_identity,
                          build_manifest_sha256=sha(build_manifest_path),
                          baseline_runner_sha256=sha(baseline_runner), binary_sha256=sha(binary),
                          runner_sha256=sha(runner), derivative_patch_sha256=sha(derivative_patch),
                          lineage_manifest_sha256=sha(lineage_manifest_path), overlay_sha256=sha(patch),
                          runner_exec_shim=dict(path=runner_shim.name, sha256=sha(runner_shim),
                                                python=str(shim_python), python_sha256=sha(shim_python)),
                          started_utc=started_utc, proxy_cpus=proxy_cpus, load_cpus=load_cpus,
                          gomaxprocs=threads, cgroup_limit_bytes=MEMORY_BYTES, cgroup_swap_disabled=True,
                          temporary_environment={key: env_base[key] for key in ("TMPDIR", "TEMP", "TMP", "GOTMPDIR", "GOFLAGS", "GOWORK", "GOTOOLCHAIN")},
                          fixed_plan=dict(rows=2, pairs=1, roles=["production", "native"], schemes=["http"],
                                          protocol="h1", concurrency=[16], warmup_seconds=2,
                                          duration_seconds=10, idle_per_host=128, response_buffer_bytes=32 << 10,
                                          role_order=["production", "native"],
                                          profile_scope="whole timed proxy lifetime: startup, readiness, warmup, timed load, and teardown",
                                          profile_flags=["-test.cpuprofile", "-test.memprofile"],
                                          overall_timeout_seconds=OVERALL_TIMEOUT_SECONDS),
                          normalization=dict(divisor="warmup_requests + useful_completed",
                              cpu="sampled CPU nanoseconds per one million warmup-plus-timed completions",
                              allocation="cumulative allocated bytes and objects per warmup-plus-timed completion"),
                          limitations=[
                              "diagnostic-only two-arm sequential observation with no repetitions or release-gate claim",
                              "whole-lifetime profiles include startup, readiness, idle, and teardown work outside request handling",
                              "heap alloc_space is cumulative and the heap profile is emitted at normal process exit",
                              "production event persistence and native intentional omissions make the arms intentionally non-equivalent",
                              "completion normalization is a scale aid, not a causal per-request cost measurement"],
                          intentional_native_omissions=["admission and lease accounting", "route and target selection",
                              "retry and cache middleware", "WAF and access checks", "request-event persistence"],
                          cleanup=dict(forced=forced_cleanup))
        provenance["commands"] = command_history
        provenance.update(environment_evidence)
        if extra:
            provenance.update(extra)
        provenance_path.write_text(json.dumps(provenance, indent=2) + "\n")

    def launch(command, log_name, env=None):
        requested_command = [str(value) for value in command]
        history = dict(label=log_name, requested_command=requested_command, kind="launched")
        command_history.append(history)
        try:
            _, command = resolve_command(requested_command, env=env)
            history["command"] = command
        except BaseException as error:
            history["resolution_error"] = repr(error)
            raise
        log = (run_root / log_name).open("w")
        logs.append(log)
        old_mask = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGTERM, signal.SIGINT, signal.SIGALRM})
        try:
            original_mask = ",".join(str(int(signum)) for signum in sorted(old_mask, key=int))
            wrapped = [str(shim_python), str(runner_shim), original_mask] + command
            process = subprocess.Popen(wrapped, stdout=log, stderr=subprocess.STDOUT,
                                       env=env, start_new_session=True)
            processes.append(process)
            process_records.append(dict(process=process, group=process.pid, label=log_name))
            return process
        finally:
            signal.pthread_sigmask(signal.SIG_SETMASK, old_mask)

    def run_owned(command, label, timeout=DOCKER_TIMEOUT_SECONDS, env=None, input_data=None):
        requested_command = [str(value) for value in command]
        history = dict(label=label, requested_command=requested_command, kind="bounded")
        command_history.append(history)
        try:
            _, command = resolve_command(requested_command, env=env)
            history["command"] = command
        except BaseException as error:
            history["resolution_error"] = repr(error)
            raise
        old_mask = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGTERM, signal.SIGINT, signal.SIGALRM})
        try:
            original_mask = ",".join(str(int(signum)) for signum in sorted(old_mask, key=int))
            wrapped = [str(shim_python), str(runner_shim), original_mask] + command
            process = subprocess.Popen(wrapped, stdin=subprocess.PIPE if input_data is not None else None,
                                       stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env,
                                       start_new_session=True)
            record = dict(process=process, group=process.pid, label=label)
            processes.append(process)
            process_records.append(record)
        finally:
            signal.pthread_sigmask(signal.SIG_SETMASK, old_mask)
        try:
            stdout, stderr = process.communicate(input=input_data, timeout=timeout)
        except subprocess.TimeoutExpired as error:
            outcome = terminate_process_group(record["group"], process)
            forced_cleanup.append(dict(kind="command", label=label, reason="timeout", cleanup=outcome))
            raise RuntimeError(f"bounded command timed out: {label}") from error
        if process_group_alive(record["group"]):
            outcome = terminate_process_group(record["group"], process)
            forced_cleanup.append(dict(kind="command", label=label, reason="descendant-survived-leader", cleanup=outcome))
            raise RuntimeError(f"command process group survived leader: {label}")
        return subprocess.CompletedProcess(command, process.returncode, stdout, stderr)

    def docker_command(arguments, label, check=False):
        completed = run_owned(["docker"] + list(arguments), "docker-" + label)
        if check and completed.returncode:
            raise RuntimeError(f"docker command failed ({label}): {completed.stderr.decode(errors='replace')}")
        return completed

    def container_inventory(label):
        inventory = docker_command(["ps", "--all", "--no-trunc", "--format", "{{.ID}}\t{{.Names}}"],
                                   "inventory-" + label)
        try:
            return parse_container_inventory(inventory.returncode, inventory.stdout.decode())
        except ValueError as error:
            raise RuntimeError(f"container inventory failed ({label}): {error}") from error

    def require_container_absent(name, container_id):
        try:
            validate_owned_container_absent(container_inventory("absence-" + name), name, container_id)
        except ValueError as error:
            raise RuntimeError(f"container absence check failed for {name}: {error}") from error

    def inspect_container_identity(name, process):
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            if process.poll() is not None:
                raise RuntimeError(f"container {name} exited before start; see {run_root}")
            state = docker_command(["inspect", "--format", "{{.Id}}\t{{.State.Pid}}", name], "inspect-identity-" + name)
            fields = state.stdout.decode().strip().split("\t")
            if (state.returncode == 0 and len(fields) == 2 and re.fullmatch(r"[0-9a-f]{64}", fields[0])
                    and fields[1].isdigit() and int(fields[1]) > 0):
                return dict(id=fields[0], pid=fields[1])
            time.sleep(.05)
        raise TimeoutError(f"container {name} did not start")

    def docker_proxy(role, scheme, label, profile_label=None):
        name = f"p2pstream-native-go-{os.getpid()}-{label}"
        scratch = run_root / "scratch" / name
        scratch.mkdir(parents=True, mode=0o700)
        ready = run_root / f"{label}.ready.json"
        stop_file = run_root / f"{label}.stop"
        role_name = "proxy" if role == "production" else "stdlib"
        container_env = dict(GOMAXPROCS=str(threads), TMPDIR="/tmp", TEMP="/tmp", TMP="/tmp", GOTMPDIR="/tmp",
                             GOFLAGS="", GOWORK="off", GOTOOLCHAIN="local",
                             P2PSTREAM_QUALIFICATION_ROLE=role_name,
                             P2PSTREAM_QUALIFICATION_READY="/qualification/" + ready.name,
                             P2PSTREAM_QUALIFICATION_STOP="/qualification/" + stop_file.name,
                             P2PSTREAM_QUALIFICATION_ORIGIN_URL=origin_url,
                             P2PSTREAM_QUALIFICATION_CERT="/qualification/public.crt.pem",
                             P2PSTREAM_QUALIFICATION_KEY="/qualification/public.key.pem",
                             P2PSTREAM_QUALIFICATION_BALLAST_MIB="0",
                             P2PSTREAM_QUALIFICATION_NATIVE_GO_ATTRIBUTION="1",
                             P2PSTREAM_QUALIFICATION_NATIVE_GO_PUBLIC_MODE=scheme)
        if role == "native":
            container_env.update(P2PSTREAM_QUALIFICATION_STDLIB_MAX_IDLE="128",
                                 P2PSTREAM_QUALIFICATION_STDLIB_SHARDS="1",
                                 P2PSTREAM_QUALIFICATION_STDLIB_POOL="1")
        invocation = ["docker", "run", "--name", name, "--network", "host",
                      "--user", f"{os.getuid()}:{os.getgid()}", "--memory", "4g", "--memory-swap", "4g",
                      "--cpuset-cpus", proxy_cpus, "--pids-limit", "8192", "--read-only", "--cap-drop", "ALL",
                      "--security-opt", "no-new-privileges", "--mount", f"type=bind,source={scratch},target=/tmp",
                      "--mount", f"type=bind,source={binary},target=/proxy,readonly",
                      "--mount", f"type=bind,source={run_root},target=/qualification"]
        for key, value in container_env.items():
            invocation += ["--env", f"{key}={value}"]
        invocation += ["debian:trixie-slim", "/proxy", "-test.run", "^TestProxyQualificationProcess$", "-test.timeout", "15m"]
        profile_paths = None
        if profile_label is not None:
            names = profile_output_names(profile_label)
            profile_paths = {kind: run_root / name for kind, name in names.items()}
            if any(path.exists() for path in profile_paths.values()):
                raise RuntimeError(f"refusing pre-existing profile output for {profile_label}")
            invocation += profile_test_flags(profile_label)
        containers.add(name)
        all_container_identities[name] = None
        process = launch(invocation, f"{label}.proxy.log")
        container_states[name] = dict(process=process, stop=stop_file)
        identity = inspect_container_identity(name, process)
        all_container_identities[name] = identity["id"]
        container_states[name]["id"] = identity["id"]
        state = await_ready(ready, process)
        parsed = urlparse(state["url"])
        if parsed.scheme != scheme or not parsed.port:
            raise RuntimeError(f"proxy readiness scheme mismatch: {state}")
        await_socket(parsed.hostname, parsed.port, process)
        return dict(name=name, container_id=identity["id"], process=process, pid=identity["pid"],
                    ready=state, stop=stop_file, role=role, scheme=scheme,
                    profile_label=profile_label, profile_paths=profile_paths)

    def await_ready(path, process):
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            if process.poll() is not None:
                raise RuntimeError(f"process exited before readiness; see {run_root}")
            try:
                return json.loads(path.read_text())
            except (FileNotFoundError, json.JSONDecodeError):
                time.sleep(.05)
        raise TimeoutError(f"no readiness file {path}")

    def await_socket(host, port, process):
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            if process.poll() is not None:
                raise RuntimeError(f"proxy exited before socket readiness; see {run_root}")
            try:
                with socket.create_connection((host, port), timeout=.5):
                    return
            except OSError:
                time.sleep(.05)
        raise TimeoutError(f"socket {host}:{port} did not become ready")

    def stop_proxy(state):
        state["stop"].touch()
        try:
            code = state["process"].wait(timeout=15)
        except subprocess.TimeoutExpired as error:
            forced_cleanup.append(dict(kind="proxy", name=state["name"], reason="natural-stop-timeout"))
            docker_command(["rm", "-f", state["name"]], "forced-remove-" + state["name"])
            containers.discard(state["name"])
            require_container_absent(state["name"], state["container_id"])
            raise RuntimeError(f"proxy did not stop naturally: {state['name']}") from error
        if code != 0:
            raise RuntimeError(f"proxy exited {code}: {state['name']}")
        if process_group_alive(state["process"].pid):
            outcome = terminate_process_group(state["process"].pid, state["process"])
            forced_cleanup.append(dict(kind="proxy", name=state["name"], reason="descendant-survived-leader", cleanup=outcome))
            raise RuntimeError(f"proxy process group survived leader: {state['name']}")
        docker_command(["rm", state["name"]], "remove-" + state["name"], check=True)
        require_container_absent(state["name"], state["container_id"])
        containers.discard(state["name"])
        container_states.pop(state["name"], None)

    def stop_origin():
        nonlocal origin
        if origin is None:
            return
        if origin.poll() is not None:
            if origin.returncode != 0:
                raise RuntimeError(f"origin exited early with {origin.returncode}")
            return
        origin_stop.touch()
        try:
            code = origin.wait(timeout=15)
        except subprocess.TimeoutExpired as error:
            forced_cleanup.append(dict(kind="origin", pid=origin.pid, reason="natural-stop-timeout"))
            raise RuntimeError("origin did not stop naturally") from error
        if code != 0:
            raise RuntimeError(f"origin exited {code}")
        if process_group_alive(origin.pid):
            outcome = terminate_process_group(origin.pid, origin)
            forced_cleanup.append(dict(kind="origin", pid=origin.pid, reason="descendant-survived-leader", cleanup=outcome))
            raise RuntimeError("origin process group survived leader")

    def validate_profile_outputs(state, result):
        paths = state.get("profile_paths")
        label = state.get("profile_label")
        if not label or not paths or set(paths) != {"cpu", "mem"}:
            raise RuntimeError("timed proxy did not declare both profile outputs")
        totals = {}
        reports = {}
        for kind, path in paths.items():
            try:
                totals[kind] = profile_sample_totals(path)
            except ValueError as error:
                raise RuntimeError(f"invalid {kind} profile for {label}: {error}") from error
            arguments = [go, "tool", "pprof", "-top", "-nodecount=80"]
            if kind == "mem":
                arguments += ["-sample_index=alloc_space"]
            arguments += [binary, path]
            report = run_owned(arguments, f"pprof-{label}-{kind}")
            if report.returncode or not report.stdout.strip():
                raise RuntimeError(f"go tool pprof rejected {kind} profile for {label}")
            report_path = run_root / f"{label}.{kind}.top.txt"
            report_path.write_bytes(report.stdout)
            reports[kind] = dict(path=path.name, sha256=sha(path), bytes=path.stat().st_size,
                                 sample_totals=totals[kind], report_path=report_path.name,
                                 report_sha256=sha(report_path))
        completions = warmup_timed_completions(result)
        cpu_ns = totals["cpu"].get("cpu:nanoseconds")
        alloc_bytes = totals["mem"].get("alloc_space:bytes")
        alloc_objects = totals["mem"].get("alloc_objects:count")
        if not isinstance(cpu_ns, int) or cpu_ns <= 0:
            raise RuntimeError(f"CPU profile lacks positive cpu:nanoseconds samples for {label}")
        if not isinstance(alloc_bytes, int) or alloc_bytes <= 0:
            raise RuntimeError(f"heap profile lacks positive alloc_space:bytes samples for {label}")
        if not isinstance(alloc_objects, int) or alloc_objects <= 0:
            raise RuntimeError(f"heap profile lacks positive alloc_objects:count samples for {label}")
        return dict(scope="whole timed proxy process lifetime", warmup_requests=result["warmup_requests"],
                    timed_completions=result["useful_completed"],
                    warmup_plus_timed_completions=completions,
                    cpu_sample_nanoseconds_per_million_completions=cpu_ns * 1_000_000 / completions,
                    allocated_bytes_per_completion=alloc_bytes / completions,
                    allocated_objects_per_completion=alloc_objects / completions,
                    profiles=reports)

    def run_load(state, concurrency, duration, warmup, smoke, label):
        nonlocal sequence
        sequence += 1
        result_file = run_root / f"{sequence:03d}-{label}.json"
        target_path = "direct" if state["role"] == "production" else "direct-control"
        load_env = attribution_load_environment(env_base, smoke=smoke)
        load_env.update(GOMAXPROCS=str(threads), P2PSTREAM_QUALIFICATION_ROLE="load",
                        P2PSTREAM_QUALIFICATION_URL=state["ready"]["url"] + "/direct/small",
                        P2PSTREAM_QUALIFICATION_SCENARIO="h1-keepalive-duration",
                        P2PSTREAM_QUALIFICATION_PROTOCOL="h1", P2PSTREAM_QUALIFICATION_CONCURRENCY=str(concurrency),
                        P2PSTREAM_QUALIFICATION_REQUESTS=str(max(concurrency, 1)),
                        P2PSTREAM_QUALIFICATION_OFFERED_RPS="0",
                        P2PSTREAM_QUALIFICATION_DURATION_SECONDS=str(duration),
                        P2PSTREAM_QUALIFICATION_WARMUP_SECONDS=str(warmup),
                        P2PSTREAM_QUALIFICATION_LABEL=label, P2PSTREAM_QUALIFICATION_PATH=target_path,
                        P2PSTREAM_QUALIFICATION_PIDS=state["pid"],
                        P2PSTREAM_QUALIFICATION_SERVER_PROCESS=json.dumps({"role": state["role"], "pid": int(state["pid"]),
                            "admin_url": state["ready"].get("admin_url", ""), "cpu_set": proxy_cpus, "container": state["name"]}),
                        P2PSTREAM_QUALIFICATION_ADMIN_URL=state["ready"].get("admin_url", ""),
                        P2PSTREAM_QUALIFICATION_PROXY_CPU_SET=proxy_cpus,
                        P2PSTREAM_QUALIFICATION_LOAD_CPU_SET=load_cpus,
                        P2PSTREAM_QUALIFICATION_RESOURCE_ENVELOPE=attribution_resource_envelope(state["role"]),
                        P2PSTREAM_QUALIFICATION_BALLAST_MIB="0", P2PSTREAM_QUALIFICATION_RESULT=str(result_file))
        timeout = SMOKE_TIMEOUT_SECONDS if smoke else LOAD_TIMEOUT_SECONDS
        def preserve_attempt(outcome, error=None):
            request = dict(attempt_sequence=sequence, role=state["role"], public_scheme=state["scheme"],
                           concurrency=concurrency, configured_duration_seconds=duration,
                           warmup_seconds=warmup, protocol="h1", smoke=smoke, label=label)
            record = dict(runner_request=request, runner_outcome=outcome)
            if error is not None:
                record["runner_error"] = repr(error)
            try:
                raw = json.loads(result_file.read_text())
                raw["runner_request"] = request
                raw["runner_outcome"] = outcome
                if error is not None:
                    raw["runner_error"] = repr(error)
                record = raw
            except (FileNotFoundError, json.JSONDecodeError) as decode_error:
                record["raw_result_error"] = repr(decode_error)
            with attempted_results_path.open("a") as attempted:
                attempted.write(json.dumps(record, separators=(",", ":")) + "\n")
            return record

        load = launch(["taskset", "-c", load_cpus, str(binary), "-test.run", "^TestProxyQualificationLoad$", "-test.timeout", "2m"],
                      result_file.name + ".log", load_env)
        try:
            code = load.wait(timeout=timeout)
        except subprocess.TimeoutExpired as error:
            outcome = terminate_process_group(load.pid, load)
            forced_cleanup.append(dict(kind="load", pid=load.pid, reason="bounded-load-timeout", cleanup=outcome))
            preserve_attempt("bounded-load-timeout", error)
            raise RuntimeError(f"load exceeded {timeout}s: {result_file}") from error
        if process_group_alive(load.pid):
            outcome = terminate_process_group(load.pid, load)
            forced_cleanup.append(dict(kind="load", pid=load.pid, reason="descendant-survived-leader", cleanup=outcome))
            preserve_attempt("descendant-survived-leader")
            raise RuntimeError(f"load process group survived leader: {result_file}")
        if code:
            preserve_attempt("nonzero-exit", RuntimeError(f"exit {code}"))
            raise RuntimeError(f"load exited {code}: {result_file}")
        result = preserve_attempt("completed")
        if (result.get("errors") != 0 or result.get("attempted", 0) < 1
                or result.get("useful_completed") != result.get("attempted")
                or result.get("response_bytes") != 256 * result.get("attempted", 0)
                or result.get("status_codes") != {"200": result.get("attempted")}
                or not math.isfinite(result.get("wall_seconds", 0)) or result.get("wall_seconds", 0) <= 0
                or not math.isfinite(result.get("useful_requests_per_second", 0)) or result.get("useful_requests_per_second", 0) <= 0
                or not math.isclose(result.get("useful_requests_per_second", 0), result.get("useful_completed", 0) / result.get("wall_seconds", 1), rel_tol=1e-9)
                or result.get("observed_http_major") != 1 or result.get("observed_origin_http_major") != 1):
            raise RuntimeError(f"load correctness failed: {result_file}")
        try:
            validate_attribution_result_configuration(result, concurrency, duration, warmup)
        except ValueError as error:
            raise RuntimeError(f"load timing/configuration changed: {result_file}: {error}") from error
        if state["role"] == "production":
            if (not result.get("event_stats_captured") or result.get("persisted_event_delta") != result["attempted"]
                    or result.get("dropped_event_delta", 0) or result.get("event_queue_after_drain", 0)
                    or result.get("event_drain_error") or result.get("event_stats_error")):
                raise RuntimeError(f"production event conservation failed: {result_file}")
        elif result.get("event_stats_captured"):
            raise RuntimeError(f"native role unexpectedly exposed production event accounting: {result_file}")
        process = result.get("server_process") or {}
        if (process.get("role") != state["role"] or process.get("container") != state["name"]
                or process.get("cpu_set") != proxy_cpus or process.get("cgroup_memory_max_bytes") != MEMORY_BYTES
                or not process.get("cgroup_memory_events_captured") or process.get("oom_delta", 0)
                or process.get("oom_kill_delta", 0)):
            raise RuntimeError(f"proxy cgroup/CPU envelope validation failed: {result_file}")
        return result

    started_utc = utc()
    save_provenance()
    failure = None
    old_sigterm = signal.getsignal(signal.SIGTERM)
    old_sigint = signal.getsignal(signal.SIGINT)
    old_sigalrm = signal.getsignal(signal.SIGALRM)

    def interrupt(signum, frame):
        if signum == signal.SIGALRM:
            raise TimeoutError(f"overall {OVERALL_TIMEOUT_SECONDS}s attribution bound expired")
        if signum == signal.SIGINT:
            raise KeyboardInterrupt()
        raise SystemExit(128 + signum)

    signal.signal(signal.SIGTERM, interrupt)
    signal.signal(signal.SIGINT, interrupt)
    signal.signal(signal.SIGALRM, interrupt)
    signal.setitimer(signal.ITIMER_REAL, OVERALL_TIMEOUT_SECONDS)
    try:
        go = Path(build_manifest["go"]["path"]).resolve(strict=True)
        if (str(go) != "/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin/go"
                or sha(go) != build_manifest["go"]["sha256"]):
            raise RuntimeError("pinned Go executable identity changed")
        go_version = run_owned([go, "version"], "go-version")
        build_id = run_owned([go, "tool", "buildid", binary], "go-buildid")
        if (go_version.returncode or go_version.stdout.decode().strip() != build_manifest["go"]["version"]
                or build_id.returncode or build_id.stdout.decode().strip() != build_manifest["binary"]["build_id"]):
            raise RuntimeError("Go version or binary BuildID differs from the build manifest")
        docker_command(["info"], "info", check=True)
        filesystem_command = run_owned(["findmnt", "-no", "FSTYPE", "--target", run_root], "findmnt")
        if filesystem_command.returncode:
            raise RuntimeError("findmnt failed")
        filesystem = filesystem_command.stdout.decode().strip()
        if filesystem in ("tmpfs", "ramfs"):
            raise RuntimeError("evidence and container scratch must be disk-backed")
        lscpu = run_owned(["lscpu", "-e=CPU,CORE,SOCKET,NODE,ONLINE"], "lscpu")
        image = docker_command(["image", "inspect", "--format", "{{.Id}}", "debian:trixie-slim"], "image-id", check=True)
        if lscpu.returncode:
            raise RuntimeError("lscpu failed")
        environment_evidence.update(scratch_filesystem=filesystem,
                                    cpu_topology=lscpu.stdout.decode(),
                                    docker_image_id=image.stdout.decode().strip(),
                                    go_version=go_version.stdout.decode().strip(),
                                    binary_build_id=build_id.stdout.decode().strip())
        save_provenance()
        cert = run_root / "public.crt.pem"
        key = run_root / "public.key.pem"
        certificate = run_owned(["openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256", "-nodes",
                                 "-subj", "/CN=public.test", "-days", "1", "-keyout", str(key), "-out", str(cert)], "certificate")
        if certificate.returncode:
            raise RuntimeError("fixture certificate generation failed")
        cert.chmod(0o600)
        key.chmod(0o600)
        shared_certificate_sha256 = hashlib.sha256(ssl.PEM_cert_to_DER_cert(cert.read_text())).hexdigest()
        environment_evidence["shared_fixture_certificate_sha256"] = shared_certificate_sha256
        ready = run_root / "origin.ready.json"
        origin_env = dict(env_base)
        origin_env.update(GOMAXPROCS=str(threads), P2PSTREAM_QUALIFICATION_ROLE="origin",
                          P2PSTREAM_QUALIFICATION_READY=str(ready), P2PSTREAM_QUALIFICATION_STOP=str(origin_stop),
                          P2PSTREAM_QUALIFICATION_ORIGIN_TLS="0")
        origin = launch(["taskset", "-c", load_cpus, str(binary), "-test.run", "^TestProxyQualificationProcess$", "-test.timeout", "20m"],
                        "origin.log", origin_env)
        origin_url = await_ready(ready, origin)["url"]
        smoke_rows = []
        for role in ("production", "native"):
            for scheme in ("http", "https"):
                state = docker_proxy(role, scheme, f"smoke-{role}-{scheme}")
                try:
                    result = run_load(state, 1, 0, 0, True, f"smoke-{role}-{scheme}")
                    if "role" in result or "public_scheme" in result:
                        raise RuntimeError("raw smoke result collides with runner role/scheme annotation")
                    result.update(role=role, public_scheme=scheme)
                    smoke_rows.append(result)
                finally:
                    stop_proxy(state)
        for row in smoke_rows:
            tls_values = (row.get("observed_tls_version", 0), row.get("observed_tls_cipher_suite", 0),
                          row.get("observed_tls_negotiated_protocol", ""), row.get("observed_tls_peer_certificate_sha256", ""))
            if row["public_scheme"] == "http":
                if tls_values != (0, 0, "", ""):
                    raise RuntimeError("plaintext smoke unexpectedly reported TLS state")
            elif not tls_values[0] or not tls_values[1] or not re.fullmatch(r"[0-9a-f]{64}", tls_values[3]):
                raise RuntimeError("HTTPS smoke did not report complete TLS state")
        https = {row["role"]: row for row in smoke_rows if row["public_scheme"] == "https"}
        tls_fields = ("observed_tls_version", "observed_tls_cipher_suite", "observed_tls_negotiated_protocol", "observed_tls_peer_certificate_sha256")
        if any(https[role].get("observed_tls_peer_certificate_sha256") != shared_certificate_sha256 for role in ("production", "native")):
            raise RuntimeError("production/native HTTPS smoke did not serve the generated shared fixture certificate")
        if any(https["production"].get(field, "") != https["native"].get(field, "") for field in tls_fields):
            raise RuntimeError("production/native HTTPS smoke negotiated different TLS/certificate/ALPN state")
        smoke = dict(schema_version=1, rows=smoke_rows, compared_fields=list(tls_fields),
                     expected_peer_certificate_sha256=shared_certificate_sha256,
                     alpn_contract="actual H1-driver value must match across roles; empty is permitted",
                     passed=True, finished_utc=utc())
        smoke_path.write_text(json.dumps(smoke, indent=2) + "\n")
        if args.smoke_only:
            stop_origin()
            save_provenance(dict(smoke_sha256=sha(smoke_path), finished_utc=utc(), rows=0))
            return
        rows = []
        for role in ("production", "native"):
            scheme = "http"
            concurrency = 16
            label = f"profile-{role}-http-h1-c16"
            state = docker_proxy(role, scheme, label, profile_label=label)
            result = None
            try:
                result = run_load(state, concurrency, 10, 2, False, label)
            finally:
                stop_proxy(state)
            profile_record = validate_profile_outputs(state, result)
            for reserved in ("role", "proxy_kind", "public_scheme", "source_identity", "derivative_identity",
                             "sequence", "profile"):
                if reserved in result:
                    raise RuntimeError(f"raw result unexpectedly contains runner annotation {reserved!r}")
            result.update(role=role, proxy_kind=role, public_scheme=scheme,
                          source_identity=source_identity, derivative_identity=derivative_identity,
                          sequence=len(rows) + 1, profile=profile_record)
            rows.append(result)
            print(f"{len(rows)}/2 {role} http h1 c16: exact={result['useful_completed']} errors=0 profiles=valid", flush=True)
        try:
            validate_profile_row_chronology(rows)
        except ValueError as error:
            raise RuntimeError(f"incomplete or reordered profiling pair: {error}") from error
        results_path.write_text("".join(json.dumps(row, separators=(",", ":")) + "\n" for row in rows))
        production, native = rows
        summary = dict(kind="p2pstream-native-go-profile-summary-v1", classification="diagnostic-only",
                       scope="one matched production-to-native HTTP/H1/c16 profiling pair; no release-gate claim",
                       row_count=2, exact_correctness=True,
                       role_order=["production", "native"], public_scheme="http", protocol="h1", concurrency=16,
                       warmup_seconds=2, duration_seconds=10,
                       production_to_native_rps_ratio=(production["useful_requests_per_second"]
                                                        / native["useful_requests_per_second"]),
                       normalization_divisor="warmup_requests + useful_completed",
                       profile_scope="whole timed proxy lifetime: startup, readiness, warmup, timed load, and teardown",
                       limitations=[
                           "single sequential pair has no repetition or statistical confidence",
                           "whole-lifetime CPU and allocation samples include work outside request handling",
                           "alloc_space is cumulative; the heap profile is emitted at normal process exit",
                           "normalized values are scale aids and do not isolate causal per-request costs"],
                       smoke_sha256=sha(smoke_path), completed_utc=utc())
        (run_root / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
        stop_origin()
        save_provenance(dict(smoke_sha256=sha(smoke_path), results_sha256=sha(results_path),
                             summary_sha256=sha(run_root / "summary.json"), rows=2, finished_utc=utc()))
        print(run_root)
    except BaseException as error:
        failure = error
        raise
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        signal.signal(signal.SIGINT, signal.SIG_IGN)
        signal.signal(signal.SIGALRM, signal.SIG_IGN)
        cleanup_errors = []
        try:
            origin_stop.touch(exist_ok=True)
            for name in list(containers):
                state = container_states.get(name, {})
                stop_file = state.get("stop")
                process = state.get("process")
                container_id = state.get("id") or all_container_identities.get(name)
                if stop_file is not None:
                    stop_file.touch(exist_ok=True)
                if process is not None and process.poll() is None:
                    try:
                        process.wait(timeout=15)
                    except subprocess.TimeoutExpired:
                        pass
                ordinary = process is not None and process.returncode == 0
                try:
                    if ordinary:
                        docker_command(["rm", name], "cleanup-remove-" + name, check=True)
                    else:
                        forced_cleanup.append(dict(kind="container", name=name, reason="natural-stop-failed"))
                        docker_command(["rm", "-f", name], "cleanup-forced-remove-" + name, check=True)
                    require_container_absent(name, container_id)
                except BaseException as error:
                    cleanup_errors.append(repr(error))
                    if ordinary:
                        forced_cleanup.append(dict(kind="container", name=name, reason="ordinary-remove-or-absence-failed"))
                    try:
                        docker_command(["rm", "-f", name], "cleanup-retry-forced-remove-" + name)
                        require_container_absent(name, container_id)
                    except BaseException as retry_error:
                        cleanup_errors.append(repr(retry_error))
                containers.discard(name)
                container_states.pop(name, None)
            if origin is not None and origin.poll() is None:
                try:
                    origin.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    forced_cleanup.append(dict(kind="origin", pid=origin.pid, reason="natural-stop-timeout"))
            elif origin is not None and origin.returncode not in (None, 0):
                forced_cleanup.append(dict(kind="origin", pid=origin.pid, reason="nonzero-exit", exit_code=origin.returncode))
            for record in reversed(process_records):
                if process_group_alive(record["group"]):
                    outcome = terminate_process_group(record["group"], record["process"])
                    forced_cleanup.append(dict(kind="process-group", label=record["label"], reason="owned-group-still-running", cleanup=outcome))
                    if outcome["group_alive_after_cleanup"]:
                        cleanup_errors.append("process group survived cleanup: " + record["label"])
            for log in logs:
                log.close()
            remaining_groups = [record["label"] for record in process_records if process_group_alive(record["group"])]
            remaining_containers = []
            try:
                final_inventory = container_inventory("final-owned")
                for name, container_id in sorted(all_container_identities.items()):
                    try:
                        validate_owned_container_absent(final_inventory, name, container_id)
                    except ValueError:
                        remaining_containers.append(dict(name=name, id=container_id))
            except BaseException as error:
                cleanup_errors.append(repr(error))
                remaining_containers = [dict(name=name, id=container_id)
                                        for name, container_id in sorted(all_container_identities.items())]
            attempted_count = len(attempted_results_path.read_text().splitlines()) if attempted_results_path.exists() else 0
            accepted_count = len(results_path.read_text().splitlines()) if results_path.exists() else 0
            outcome = dict(kind="p2pstream-native-go-attribution-outcome-v1", finished_utc=utc(),
                           failure=repr(failure) if failure is not None else None,
                           forced_cleanup=forced_cleanup, cleanup_errors=cleanup_errors,
                           remaining_process_groups=remaining_groups, remaining_containers=remaining_containers,
                           attempted_result_rows=attempted_count, accepted_result_rows=accepted_count,
                           complete=failure is None and not forced_cleanup and not cleanup_errors
                                    and not remaining_groups and not remaining_containers)
            outcome_path = run_root / "outcome.json"
            outcome_path.write_text(json.dumps(outcome, indent=2) + "\n")
            save_provenance(dict(finished_utc=outcome["finished_utc"], outcome_sha256=sha(outcome_path),
                                 attempted_results_sha256=sha(attempted_results_path) if attempted_results_path.exists() else None,
                                 invalid_reason=(repr(failure) if failure is not None else
                                                 "forced or incomplete cleanup" if not outcome["complete"] else None)))
        finally:
            signal.signal(signal.SIGTERM, old_sigterm)
            signal.signal(signal.SIGINT, old_sigint)
            signal.signal(signal.SIGALRM, old_sigalrm)
        if (forced_cleanup or cleanup_errors) and failure is None:
            raise RuntimeError(f"owned cleanup was incomplete; evidence preserved at {run_root}")


if __name__ == "__main__":
    main()

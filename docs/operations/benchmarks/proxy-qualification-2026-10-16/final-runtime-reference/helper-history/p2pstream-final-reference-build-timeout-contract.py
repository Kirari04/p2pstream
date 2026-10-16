#!/usr/bin/env python3
"""Finite contract checks for the exact final-reference build lifecycle block.

This harness never invokes Go or the reference workload. It extracts the reviewed
block byte-for-byte, substitutes a process proxy only for the compiler command,
and exercises real Linux process groups.
"""
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess as real_subprocess
import sys
import tempfile
import textwrap
import threading
import time


WRAPPER = Path("/tmp/p2pstream-final-reference.py")
WRAPPER_SHA256 = "527036f342f77211ac1cf1751c0933d4c2fdc5811cdea1159248fe0cd94798cf"
BLOCK_ARTIFACT = Path("/tmp/p2pstream-final-reference-build-block.pyfrag")
BLOCK_SHA256 = "6ec7adc9d08c3ecb5405a3afe85aacad5e83643d7f8e23600f3afbe8045c779e"


def sha_bytes(data):
    return hashlib.sha256(data).hexdigest()


def sha(path):
    return sha_bytes(Path(path).read_bytes())


def utc():
    return datetime.now(timezone.utc).isoformat()


def exact_build_block():
    source = WRAPPER.read_text()
    assert sha_bytes(source.encode()) == WRAPPER_SHA256
    start_marker = '    compiler_command = [str(go), "test", "-c", "-o", str(candidate), "./internal/server"]\n'
    end_marker = '    caddy = binaries / "caddy"\n'
    assert source.count(start_marker) == 1
    assert source.count(end_marker) == 1
    start = source.index(start_marker)
    end = source.index(end_marker, start)
    block = textwrap.dedent(source[start:end])
    assert sha_bytes(block.encode()) == BLOCK_SHA256
    assert BLOCK_ARTIFACT.read_text() == block
    assert "process.wait(timeout=600)" in block
    return compile(block, str(BLOCK_ARTIFACT), "exec")


SCENARIOS = {
    "natural0": "import sys; print('natural0', flush=True); sys.exit(0)",
    "nonzero": "import sys; print('nonzero', flush=True); sys.exit(7)",
    "timeout": """
import os, time
child = os.fork()
if child == 0:
    time.sleep(60)
    os._exit(0)
import signal, sys
def terminate(signum, frame):
    os.waitpid(child, 0)
    sys.exit(0)
signal.signal(signal.SIGTERM, terminate)
time.sleep(60)
""",
    "sigterm": "import time; time.sleep(60)",
    "sigterm_during_spawn": "import time; time.sleep(60)",
    "child_sigterm_delivery": """
import os, pathlib, signal, time
signal.signal(signal.SIGTERM, signal.SIG_DFL)
signal.signal(signal.SIGINT, signal.SIG_DFL)
pathlib.Path(os.environ["CONTRACT_READY"]).write_text("ready")
time.sleep(60)
""",
    "child_sigint_delivery": """
import os, pathlib, signal, time
signal.signal(signal.SIGTERM, signal.SIG_DFL)
signal.signal(signal.SIGINT, signal.SIG_DFL)
pathlib.Path(os.environ["CONTRACT_READY"]).write_text("ready")
time.sleep(60)
""",
    "repeated_term_lingering": """
import os, signal, time
if os.fork() == 0:
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
    while True:
        time.sleep(1)
while True:
    time.sleep(1)
""",
}


class ProcessProxy:
    def __init__(self, process, requested_waits):
        self._process = process
        self._requested_waits = requested_waits

    @property
    def pid(self):
        return self._process.pid

    @property
    def returncode(self):
        return self._process.returncode

    def poll(self):
        return self._process.poll()

    def wait(self, timeout=None):
        self._requested_waits.append(timeout)
        # Preserve and assert the source's 600-second contract while making only
        # the synthetic compiler wait finite. Cleanup waits retain source bounds.
        actual_timeout = 0.25 if timeout == 600 else timeout
        return self._process.wait(timeout=actual_timeout)


class CompilerSubprocess:
    TimeoutExpired = real_subprocess.TimeoutExpired
    STDOUT = real_subprocess.STDOUT

    def __init__(self, scenario, requested_waits, spawned, pids):
        self.scenario = scenario
        self.requested_waits = requested_waits
        self.spawned = spawned
        self.pids = pids

    def Popen(self, command, **kwargs):
        assert kwargs.pop("start_new_session") is True
        if self.scenario == "spawn_failure":
            raise OSError("synthetic compiler spawn failure")
        assert Path(command[1]).name == "build-exec-shim.py"
        test_command = command[:3] + [sys.executable, "-c", SCENARIOS[self.scenario]]
        ready_path = Path(kwargs["cwd"]) / ("ready-" + self.scenario)
        child_env = dict(kwargs["env"])
        child_env["CONTRACT_READY"] = str(ready_path)
        kwargs["env"] = child_env
        process = real_subprocess.Popen(
            test_command,
            start_new_session=True,
            **kwargs,
        )
        self.pids.append(process.pid)
        self.spawned.set()
        proxy = ProcessProxy(process, self.requested_waits)
        if self.scenario == "sigterm_during_spawn":
            # The exact block has SIGTERM masked until this returned handle and
            # its process group ID have been assigned.
            os.kill(os.getpid(), signal.SIGTERM)
        elif self.scenario == "child_sigterm_delivery":
            deadline = time.monotonic() + 2
            while not ready_path.exists() and time.monotonic() < deadline:
                time.sleep(0.01)
            assert ready_path.exists()
            os.killpg(process.pid, signal.SIGTERM)
        elif self.scenario == "child_sigint_delivery":
            deadline = time.monotonic() + 2
            while not ready_path.exists() and time.monotonic() < deadline:
                time.sleep(0.01)
            assert ready_path.exists()
            os.killpg(process.pid, signal.SIGINT)
        return proxy


def group_alive(group):
    try:
        os.killpg(group, 0)
        return True
    except ProcessLookupError:
        return False


def send_signals_after_spawn(spawned, repeated):
    assert spawned.wait(timeout=2)
    time.sleep(0.1)
    os.kill(os.getpid(), signal.SIGTERM)
    if repeated:
        time.sleep(0.2)
        os.kill(os.getpid(), signal.SIGTERM)
        time.sleep(0.2)
        os.kill(os.getpid(), signal.SIGTERM)


def run_case(compiled_block, scenario):
    requested_waits = []
    spawned = threading.Event()
    pids = []
    signal_thread = None
    before_term = signal.getsignal(signal.SIGTERM)
    before_int = signal.getsignal(signal.SIGINT)
    with tempfile.TemporaryDirectory(
        prefix="final-reference-build-contract-" + scenario + ".", delete=False,
    ) as directory:
        root = Path(directory)
        manifest_path = root / "manifest.json"
        manifest = {"workload_started": False}

        def save():
            manifest_path.write_text(json.dumps(manifest, sort_keys=True) + "\n")

        if scenario in ("sigterm", "repeated_term_lingering"):
            signal_thread = threading.Thread(
                target=send_signals_after_spawn,
                args=(spawned, scenario == "repeated_term_lingering"),
                daemon=False,
            )
            signal_thread.start()
        fake_subprocess = CompilerSubprocess(scenario, requested_waits, spawned, pids)
        namespace = {
            "candidate": root / "unused-server.test",
            "env": dict(os.environ),
            "go": Path("/synthetic/exact-go"),
            "manifest": manifest,
            "os": os,
            "Path": Path,
            "root": root,
            "save": save,
            "sha": sha,
            "signal": signal,
            "source": root,
            "subprocess": fake_subprocess,
            "sys": sys,
            "time": time,
            "utc": utc,
        }
        caught = None
        try:
            exec(compiled_block, namespace, namespace)
            manifest["workload_started"] = True
            save()
        except BaseException as exc:
            caught = exc
        finally:
            if signal_thread is not None:
                signal_thread.join(timeout=2)
                assert not signal_thread.is_alive()
            for pid in pids:
                if group_alive(pid):
                    os.killpg(pid, signal.SIGKILL)
                    raise AssertionError(f"{scenario}: harness found a surviving compiler group {pid}")

        assert signal.getsignal(signal.SIGTERM) == before_term
        assert signal.getsignal(signal.SIGINT) == before_int
        if not manifest_path.is_file():
            if caught is not None:
                raise caught
            raise AssertionError(f"{scenario}: exact build block did not write its manifest")
        persisted = json.loads(manifest_path.read_text())
        build = persisted["build"]
        assert (root / "build.log").is_file()
        assert build["log_sha256"] == sha(root / "build.log")
        assert build["timeout_seconds"] == 600
        assert build["group_alive_after_cleanup"] is False
        assert persisted["workload_started"] is (scenario == "natural0")
        if scenario != "spawn_failure":
            if scenario == "sigterm_during_spawn":
                assert requested_waits == []
            else:
                assert requested_waits[0] == 600
            assert build["process_started"] is True
            assert build["process_group_matches_leader"] is True
            assert build["direct_process_reaped"] is True
        else:
            assert requested_waits == []
            assert isinstance(caught, OSError)
            assert build["process_started"] is False
            assert build["process_group_id"] is None
            assert build["direct_process_reaped"] is None
        if scenario == "natural0":
            assert caught is None
            assert build["exit_code"] == 0
            assert build["wait_completed"] is True
            assert build["forced_group_cleanup"] is False
        elif scenario == "nonzero":
            assert isinstance(caught, RuntimeError)
            assert build["exit_code"] == 7
            assert build["wait_completed"] is True
            assert build["forced_group_cleanup"] is False
        elif scenario == "timeout":
            assert isinstance(caught, RuntimeError)
            assert build["timed_out"] is True
            assert build["forced_group_cleanup"] is True
            assert build["sigterm_sent"] is True
            assert build["sigkill_sent"] is False
        elif scenario in ("sigterm", "sigterm_during_spawn"):
            assert isinstance(caught, SystemExit)
            assert build["timed_out"] is False
            assert build["forced_group_cleanup"] is True
            assert build["sigterm_sent"] is True
            assert build["sigkill_sent"] is False
        elif scenario == "child_sigterm_delivery":
            assert isinstance(caught, RuntimeError)
            assert build["exit_code"] == -signal.SIGTERM
            assert build["wait_completed"] is True
            assert build["forced_group_cleanup"] is False
            assert build["sigkill_sent"] is False
        elif scenario == "child_sigint_delivery":
            assert isinstance(caught, RuntimeError)
            assert build["exit_code"] == -signal.SIGINT
            assert build["wait_completed"] is True
            assert build["forced_group_cleanup"] is False
            assert build["sigkill_sent"] is False
        elif scenario == "repeated_term_lingering":
            assert isinstance(caught, SystemExit)
            assert build["forced_group_cleanup"] is True
            assert build["sigterm_sent"] is True
            assert build["sigkill_sent"] is True
        return {"scenario": scenario, "evidence_root": str(root),
            "build": build, "exception": repr(caught)}


def main():
    compiled_block = exact_build_block()
    results = [run_case(compiled_block, scenario) for scenario in (
        "natural0", "nonzero", "timeout", "sigterm", "sigterm_during_spawn",
        "child_sigterm_delivery", "child_sigint_delivery",
        "repeated_term_lingering", "spawn_failure",
    )]
    print(json.dumps({"wrapper_sha256": WRAPPER_SHA256, "block_sha256": BLOCK_SHA256,
        "results": results}, sort_keys=True))


if __name__ == "__main__":
    main()

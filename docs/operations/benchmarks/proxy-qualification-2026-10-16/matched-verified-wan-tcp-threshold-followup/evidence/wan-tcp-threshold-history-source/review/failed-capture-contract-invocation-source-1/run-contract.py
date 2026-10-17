#!/usr/bin/python3
"""Run the frozen threshold-history validator contract in one owned process group."""
from __future__ import annotations

import hashlib
import importlib.util
import json
import os
from pathlib import Path, PurePosixPath
import stat
import sys
import traceback

REPO = Path("/home/user/.codex/worktrees/astra-proxy-performance/p2pstream")
CHECKPOINT = REPO / "tmp/continuation-20261016/wan-tcp-threshold-history-source/validator-capture-source-1"
CHECKPOINT_SHA = "10fb785112ba9a678e4592defe9251cab7b8077225150bb052140b999fb76b5e"
CONTRACT = CHECKPOINT / "integration/contracts/test-failed-capture-validator.py"
CONTRACT_SHA = "4d34030265f3dacee2cdb513d7a786d1cbc1d0a69779054ae3211ba0a3dab056"
OWNED_HELPER = REPO / "tmp/continuation-20261016/wan-sender-wait-build-source/review/source-checkpoint-3/run-audited-build.py"
OWNED_HELPER_SHA = "8d40407691bc70d6341f9932d7e6d8345ccb06e181ceaed4a5cdf3491e234440"
PYTHON = Path("/usr/bin/python3.14")
PYTHON_SHA = "52e0a13e60a981d8c4b6478be2ba5176f69da07948a056bf49cf6f077e30cb41"
EXPECTED_CASE_COUNT = 106
EXPECTED_CASES = CHECKPOINT / "EXPECTED-CONTRACT-CASES.json"
EXPECTED_CASES_SHA = "d38f81ece2a1fc17cc15babd7ed9f10572dd535f1d1b676d4f8ab30555325af1"

def sha(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def write_text(path: Path, value: str) -> None:
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as destination:
        destination.write(value)


def write_json(path: Path, value: object) -> None:
    write_text(path, json.dumps(value, indent=2, sort_keys=True) + "\n")


def relative(value: str) -> PurePosixPath:
    path = PurePosixPath(value)
    if path.is_absolute() or not path.parts or "." in path.parts or ".." in path.parts:
        raise ValueError(f"unsafe ledger path: {value!r}")
    return path


def checkpoint_inventory() -> dict[str, str]:
    ledger_path = CHECKPOINT / "SHA256SUMS.txt"
    if sha(ledger_path) != CHECKPOINT_SHA:
        raise ValueError("checkpoint manifest changed")
    expected: dict[str, str] = {}
    for line in ledger_path.read_text(encoding="utf-8").splitlines():
        digest, name = line.split("  ", 1)
        if len(digest) != 64 or name.startswith("./"):
            raise ValueError("malformed checkpoint manifest")
        key = relative(name).as_posix()
        if key in expected:
            raise ValueError("duplicate checkpoint manifest path")
        expected[key] = digest
    actual: dict[str, str] = {}
    for path in sorted(CHECKPOINT.rglob("*")):
        if path.is_symlink():
            raise ValueError(f"checkpoint symlink rejected: {path}")
        if path.is_file() and path != ledger_path:
            actual[path.relative_to(CHECKPOINT).as_posix()] = sha(path)
    if actual != expected:
        raise ValueError("checkpoint inventory differs")
    return actual


def exact_tool(path: Path, digest: str, *, executable: bool) -> None:
    canonical = path.resolve(strict=True)
    info = canonical.lstat()
    if path.is_symlink() or not stat.S_ISREG(info.st_mode) or sha(canonical) != digest:
        raise ValueError(f"tool changed: {path}")
    if executable and not os.access(canonical, os.X_OK):
        raise ValueError(f"tool is not executable: {path}")


def load_owned_helper():
    exact_tool(OWNED_HELPER, OWNED_HELPER_SHA, executable=False)
    spec = importlib.util.spec_from_file_location("reviewed_owned_process", OWNED_HELPER)
    if spec is None or spec.loader is None:
        raise ValueError("owned helper import failed")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def natural_lifecycle(path: Path) -> bool:
    value = json.loads(path.read_text(encoding="utf-8"))
    return (
        value.get("owned_group_identity_valid") is True
        and value.get("direct_child_joined") is True
        and value.get("timed_out") is False
        and value.get("term_attempted") is False
        and value.get("kill_attempted") is False
        and value.get("forced_cleanup") is False
        and value.get("group_joined") is True
        and type(value.get("direct_status")) is int and value.get("direct_status") == 0
        and type(value.get("wait_returncode")) is int and value.get("wait_returncode") == 0
    )


def main() -> int:
    if len(sys.argv) != 2:
        raise ValueError("usage: run-contract.py FRESH_OUTPUT")
    output = Path(sys.argv[1]).resolve()
    if output.exists() or output.is_symlink() or not output.parent.is_dir():
        raise ValueError("output must be fresh below an existing parent")
    output.mkdir(mode=0o700)
    records = output / "records"
    records.mkdir(mode=0o700)
    work = records / "contract-work"
    accepted = False
    issue = None
    before = None
    try:
        if sys.flags.isolated != 1 or not sys.dont_write_bytecode:
            raise ValueError("invocation requires Python -I -B")
        exact_tool(PYTHON, PYTHON_SHA, executable=True)
        if sha(CONTRACT) != CONTRACT_SHA or sha(EXPECTED_CASES) != EXPECTED_CASES_SHA:
            raise ValueError("contract or exact-case allowlist changed")
        before = checkpoint_inventory()
        owned = load_owned_helper()
        environment = {
            "HOME": "/home/user",
            "PATH": "/usr/bin:/bin",
            "PYTHONDONTWRITEBYTECODE": "1",
            "PYTHONHASHSEED": "0",
            "LC_ALL": "C",
            "TZ": "Europe/Zurich",
        }
        write_json(records / "inputs-pre.json", {
            "schema_version": 1,
            "checkpoint_manifest_sha256": CHECKPOINT_SHA,
            "contract_sha256": CONTRACT_SHA,
            "expected_cases_sha256": EXPECTED_CASES_SHA,
            "owned_process_helper_sha256": OWNED_HELPER_SHA,
            "python_sha256": PYTHON_SHA,
            "checkpoint_files": before,
        })
        status = owned.run(
            records,
            "tcp-state-contracts",
            [str(PYTHON), "-I", "-B", str(CONTRACT)],
            CHECKPOINT,
            environment,
            60,
        )
        if status != 0:
            raise ValueError(f"contract driver returned {status}")
        if not natural_lifecycle(records / "tcp-state-contracts.lifecycle.json"):
            raise ValueError("contract process did not join naturally")
        if (records / "tcp-state-contracts.direct.status").read_text(encoding="utf-8") != "0\n":
            raise ValueError("owned direct status differs")
        stdout = records / "tcp-state-contracts.stdout.log"
        result = json.loads(stdout.read_text(encoding="utf-8"))
        expected_cases = json.loads(EXPECTED_CASES.read_text(encoding="utf-8"))
        expected_names = expected_cases.get("case_names") if type(expected_cases) is dict else None
        observed = result.get("cases") if type(result) is dict else None
        if (type(expected_cases.get("schema_version")) is not int or expected_cases.get("schema_version") != 1
                or type(expected_cases.get("case_count")) is not int
                or expected_cases.get("case_count") != EXPECTED_CASE_COUNT
                or type(expected_names) is not list or len(expected_names) != EXPECTED_CASE_COUNT
                or len(set(expected_names)) != EXPECTED_CASE_COUNT
                or type(result) is not dict
                or type(result.get("schema_version")) is not int or result.get("schema_version") != 1
                or result.get("accepted") is not True
                or type(result.get("case_count")) is not int or result.get("case_count") != EXPECTED_CASE_COUNT
                or type(observed) is not list or len(observed) != EXPECTED_CASE_COUNT
                or any(type(row) is not dict or set(row) != {"name", "accepted"}
                       or row.get("accepted") is not True for row in observed)
                or [row.get("name") for row in observed] != expected_names):
            raise ValueError("contract result differs from the exact reviewed case allowlist")
        accepted = True
    except BaseException as error:
        issue = f"{type(error).__name__}: {error}"
        traceback.print_exc()
    finally:
        checks: dict[str, object] = {}
        try:
            after = checkpoint_inventory()
            checks["checkpoint_unchanged"] = before is not None and after == before
            checks["contract_unchanged"] = sha(CONTRACT) == CONTRACT_SHA
            checks["expected_cases_unchanged"] = sha(EXPECTED_CASES) == EXPECTED_CASES_SHA
            checks["owned_process_helper_unchanged"] = sha(OWNED_HELPER) == OWNED_HELPER_SHA
            checks["python_unchanged"] = sha(PYTHON.resolve(strict=True)) == PYTHON_SHA
            accepted = accepted and all(value is True for value in checks.values())
        except BaseException as error:
            accepted = False
            checks["finalization_error"] = f"{type(error).__name__}: {error}"
        write_json(records / "RESULT.json", {
            "schema_version": 1,
            "accepted": accepted,
            "issue": issue,
            "case_count": EXPECTED_CASE_COUNT,
            "post_checks": checks,
            "endpoint_execution": False,
            "go_execution": False,
            "wan_traffic": False,
        })
        write_text(records / "final.status", "0\n" if accepted else "1\n")
        ledger = records / "SHA256SUMS.txt"
        with ledger.open("x", encoding="utf-8") as destination:
            for path in sorted(records.rglob("*")):
                if path.is_file() and path != ledger:
                    destination.write(f"{sha(path)}  ./{path.relative_to(records).as_posix()}\n")
        write_text(output / "FINAL", "complete\n")
    return 0 if accepted else 1


if __name__ == "__main__":
    raise SystemExit(main())

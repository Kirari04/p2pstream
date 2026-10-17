#!/usr/bin/python3.14
"""Run only the reviewed close-cancellation overlay, normal then race."""
from __future__ import annotations

from collections import Counter
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import stat
import sys
import traceback

REPO = Path("/home/user/.codex/worktrees/astra-proxy-performance/p2pstream")
BASE = REPO / "tmp/continuation-20261016/wan-tcp-threshold-history-source"
HERE = Path(__file__).resolve().parent
REPRO = BASE / "close-race-reproduction-source-1"
BUILD = BASE / "build-source-1/runs/audited-build-attempt-3"
SOURCE = BUILD / "source"
RUNS = BASE / "runner-source-1/runs"
HELPER = REPO / "tmp/continuation-20261016/wan-sender-wait-build-source/review/source-checkpoint-3/run-audited-build.py"
HELPER_SHA = "8d40407691bc70d6341f9932d7e6d8345ccb06e181ceaed4a5cdf3491e234440"
EXPECTED_ROOT = "TestQualificationTCPThresholdHistorySessionCloseCancellation"


def sha(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


if HELPER.is_symlink() or sha(HELPER) != HELPER_SHA:
    raise RuntimeError("owned-process helper differs")
spec = importlib.util.spec_from_file_location("close_reproduction_owned", HELPER)
owned = importlib.util.module_from_spec(spec)
spec.loader.exec_module(owned)
fixed = json.loads((HERE / "INPUTS.json").read_text())


def ledger_inventory(root):
    ledger = owned.parse_sha_ledger(root / "SHA256SUMS.txt")
    actual = owned.inventory(root)
    actual.pop("SHA256SUMS.txt")
    if actual != ledger:
        raise ValueError(f"immutable inventory differs: {root}")
    return {"manifest_sha256": sha(root / "SHA256SUMS.txt"), "files": actual}


def fixed_inputs():
    invocation = ledger_inventory(HERE)
    reproduction = ledger_inventory(REPRO)
    if reproduction["manifest_sha256"] != fixed["source_manifest_sha256"]:
        raise ValueError("reproduction source differs")
    tools = {}
    for name, item in fixed["tools"].items():
        path = Path(item["path"])
        if path.is_symlink() or not path.is_file() or sha(path) != item["sha256"]:
            raise ValueError(f"pinned tool differs: {name}")
        tools[name] = dict(item)
    if Path(sys.executable).resolve() != Path(tools["python"]["path"]):
        raise ValueError("invocation Python differs")
    inventory = BUILD / "records/source-pre.json"
    receipt = BUILD / "audited-tcp-threshold-history-build-receipt.json"
    if sha(inventory) != fixed["source_inventory_sha256"] or sha(receipt) != fixed["build_receipt_sha256"]:
        raise ValueError("audited source authority differs")
    chain = json.loads((REPRO / "SOURCE-CHAIN.json").read_text())
    focused_inputs = {}
    for item in chain["inputs"]:
        path = Path(item["path"])
        if path.is_symlink() or not path.is_file() or path.stat().st_size != item["bytes"] or sha(path) != item["sha256"]:
            raise ValueError(f"reproduction input differs: {path}")
        focused_inputs[str(path)] = {"sha256": item["sha256"], "bytes": item["bytes"]}
    target = SOURCE / "internal/agent/qualification_close_cancellation_reproduction_test.go"
    overlay = {"Replace": {str(target): str(REPRO / "overlay/internal/agent/qualification_close_cancellation_reproduction_test.go")}}
    if target.exists() or target.is_symlink() or json.loads((REPRO / "overlay.json").read_text()) != overlay:
        raise ValueError("overlay target is not the exact new virtual test file")
    return {"invocation": invocation, "reproduction": reproduction, "tools": tools,
            "focused_source_inputs": focused_inputs, "audited_source_inventory_sha256": sha(inventory),
            "audited_build_receipt_sha256": sha(receipt)}


def require_natural(records, name, status):
    value = json.loads((records / f"{name}.lifecycle.json").read_text())
    required = {"owned_group_identity_valid": True, "direct_child_joined": True,
                "group_joined": True, "timed_out": False, "term_attempted": False,
                "kill_attempted": False, "forced_cleanup": False}
    if (any(value.get(key) is not expected for key, expected in required.items())
            or type(value.get("direct_status")) is not int or value["direct_status"] != status
            or type(value.get("wait_returncode")) is not int or value["wait_returncode"] != status
            or (records / f"{name}.direct.status").read_text() != f"{status}\n"):
        raise ValueError(f"{name} did not join naturally with its direct status")


def audit_tests(records, name):
    events = [json.loads(line) for line in (records / f"{name}.stdout.log").read_text().splitlines()]
    runs, passes, package_passes = [], [], 0
    for event in events:
        if type(event) is not dict or event.get("Action") in {"skip", "fail"}:
            raise ValueError(f"{name} contains malformed or failed/skipped events")
        test = event.get("Test")
        if test is not None:
            if event.get("Package") != fixed["package"] or test not in fixed["expected_tests"]:
                raise ValueError(f"{name} ran an unexpected test")
            if event.get("Action") == "run":
                runs.append(test)
            elif event.get("Action") == "pass":
                passes.append(test)
        elif event.get("Action") == "pass" and event.get("Package") == fixed["package"]:
            package_passes += 1
    if (runs != fixed["expected_tests"] or Counter(passes) != Counter(fixed["expected_tests"])
            or package_passes != 1):
        raise ValueError(f"{name} did not pass the exact three test names")
    owned.write_json(records / f"{name}.exact-tests.json", {
        "schema_version": 1, "accepted": True, "run_order": runs, "passed_tests": passes,
        "test_count": 3, "package_pass_count": package_passes})


def main():
    if len(sys.argv) != 2 or not sys.flags.isolated or not sys.dont_write_bytecode:
        raise ValueError("usage: python3.14 -I -B run-reproduction.py FRESH_RESULT_ROOT")
    output = Path(sys.argv[1]).absolute()
    parent = output.parent
    info = parent.lstat()
    if (parent != RUNS or parent.is_symlink() or not stat.S_ISDIR(info.st_mode)
            or info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) != 0o700
            or info.st_dev != REPO.stat().st_dev or output.exists() or output.is_symlink()):
        raise ValueError("fresh result below owned ext4 runs parent required")
    output.mkdir(mode=0o700)
    records = output / "records"
    records.mkdir(mode=0o700)
    accepted, issue, before = False, None, None
    stages = {"normal": {"started": False}, "race": {"started": False}}
    postchecks = {}
    try:
        before = fixed_inputs()
        owned.write_json(records / "inputs-pre.json", before)
        for name in ("gopath", "gocache-normal", "gocache-race", "tmp-normal", "tmp-race"):
            (output / name).mkdir(mode=0o700)
        common = {"HOME": "/home/user", "PATH": "/usr/bin:/bin", "LC_ALL": "C", "TZ": "Europe/Zurich",
                  "GOENV": "off", "GOTOOLCHAIN": "local", "GOWORK": "off", "GOFLAGS": "",
                  "CGO_ENABLED": "1", "GOMAXPROCS": "2", "GOPATH": str(output / "gopath"),
                  "GOMODCACHE": "/home/user/go/pkg/mod", "GOPROXY": "off", "GOSUMDB": "off",
                  "CC": fixed["tools"]["cc"]["path"]}
        for name in ("normal", "race"):
            disk = os.statvfs(output)
            free = disk.f_bavail * disk.f_frsize
            if free < 4 * 1024**3:
                raise ValueError("less than 4 GiB available before the next compile/test stage")
            env = dict(common, GOCACHE=str(output / f"gocache-{name}"),
                       GOTMPDIR=str(output / f"tmp-{name}"), TMPDIR=str(output / f"tmp-{name}"))
            argv = [fixed["tools"]["go"]["path"], "test", "-json", "-p=1"]
            if name == "race":
                argv.append("-race")
            argv += [f"-overlay={REPRO / 'overlay.json'}", "-tags=p2pstream_qualification", "-count=1",
                     "-timeout=60s", f"-run=^{EXPECTED_ROOT}$", "-v", "./internal/agent"]
            owned.write_json(records / f"{name}.environment.json", {"environment": env, "available_bytes": free})
            stages[name]["started"] = True
            status = owned.run(records, name, argv, SOURCE, env, 600)
            stages[name]["status"] = status
            require_natural(records, name, status)
            if status != 0:
                raise ValueError(f"{name} compile/test returned {status}; no retry or later stage")
            audit_tests(records, name)
        accepted = True
    except BaseException as error:
        issue = f"{type(error).__name__}: {error}"
        traceback.print_exc()
    finally:
        try:
            after = fixed_inputs()
            owned.write_json(records / "inputs-post.json", after)
            postchecks["all_source_tools_and_overlay_unchanged"] = before is not None and before == after
            accepted = accepted and all(value is True for value in postchecks.values())
        except BaseException as error:
            accepted = False
            postchecks["postcheck_error"] = f"{type(error).__name__}: {error}"
        owned.write_json(records / "RESULT.json", {"schema_version": 1, "accepted": accepted,
            "issue": issue, "stages": stages, "postchecks": postchecks, "expected_tests": fixed["expected_tests"],
            "endpoint_binary_builds": False, "wan_traffic": False, "runtime_fix_claimed": False})
        owned.write_text(records / "final.status", "0\n" if accepted else "1\n")
        owned.write_text(records / "SHA256SUMS.txt", "".join(
            f"{sha(path)}  ./{path.relative_to(records).as_posix()}\n"
            for path in sorted(records.rglob("*")) if path.is_file() and path.name != "SHA256SUMS.txt"))
        owned.write_text(output / "FINAL", "complete\n")
    return 0 if accepted else 1


if __name__ == "__main__":
    raise SystemExit(main())

import copy
import json
import subprocess
import sys
from pathlib import Path

root = Path(sys.argv[1]).resolve()
valid = root / "analyzer-cases" / "valid-actual"
base = [json.loads(line) for line in (valid / "topologies.jsonl").read_text().splitlines() if line]
cases = {}

def add(name, mutate, expected):
    rows = copy.deepcopy(base)
    mutate(rows)
    cases[name] = (rows, expected)

add("valid-actual-inode-reuse", lambda rows: None, 0)

def modeled_recycle(rows):
    inode = rows[0]["namespace_instances"]["server"]["netns_inode"]
    rows[1]["namespace_instances"]["server"]["netns_inode"] = inode
    rows[1]["namespace_identities"]["server"] = f"net:[{inode}]"
add("modeled-recycled-inode-after-join", modeled_recycle, 0)

def same_process_new_inode(rows):
    old = rows[0]["namespace_instances"]["server"]
    new = rows[1]["namespace_instances"]["server"]
    for key in ("boot_id", "helper_pid", "helper_start_ticks"):
        new[key] = old[key]
add("same-process-new-inode", same_process_new_inode, 2)

def prior_unjoined(rows):
    rows[0]["namespace_helpers_joined"] = False
add("prior-helper-unjoined", prior_unjoined, 2)

def bad_predecessor(rows):
    rows[1]["prior_joined_generation"] = 0
add("predecessor-generation-mismatch", bad_predecessor, 2)

def inode_alias(rows):
    inode = rows[1]["namespace_instances"]["server"]["netns_inode"]
    rows[1]["namespace_instances"]["agent"]["netns_inode"] = inode
    rows[1]["namespace_identities"]["agent"] = f"net:[{inode}]"
add("within-arm-inode-alias", inode_alias, 2)

def process_alias(rows):
    server = rows[1]["namespace_instances"]["server"]
    agent = rows[1]["namespace_instances"]["agent"]
    for key in ("boot_id", "helper_pid", "helper_start_ticks"):
        agent[key] = server[key]
add("within-arm-process-alias", process_alias, 2)

def runner_alias(rows):
    inode = int(rows[1]["runner_namespace_identity"].removeprefix("net:[").removesuffix("]"))
    rows[1]["namespace_instances"]["agent"]["netns_inode"] = inode
    rows[1]["namespace_identities"]["agent"] = rows[1]["runner_namespace_identity"]
add("runner-namespace-alias", runner_alias, 2)

def pid_one(rows):
    rows[1]["namespace_instances"]["agent"]["helper_pid"] = 1
add("pid-one", pid_one, 2)

def bool_generation(rows):
    rows[1]["namespace_instances"]["agent"]["arm_generation"] = True
add("boolean-generation", bool_generation, 2)

case_root = root / "analyzer-cases"
analyzer = root / "final-analyzer.py"
args = [
    "87cf7c1b008c86e5a416713f34d5c45d59774efa6d6f038b895f6da681e1a70b",
    "98d67ff45e6f4f0da317b7929c9cf7dce704f3946bcf710c9f4a5ae66f254c67",
    "62a0ba54f0d62310e23607bf89a2738b951882529913c71be15a89d42849c9ac",
    "40e1046c2f68ee3d545dfd582cd424c2f3604b2a",
    "ad818ce955253fd9030cbefa352527321b308cfc574cb4bbee9cbcd0d4930790",
    "1", "0", "80", "40", "2",
]
records = []
for name, (topologies, expected) in cases.items():
    destination = case_root / name
    destination.mkdir(exist_ok=True)
    (destination / "rows.jsonl").write_bytes((valid / "rows.jsonl").read_bytes())
    (destination / "snapshots.jsonl").write_bytes((valid / "snapshots.jsonl").read_bytes())
    with (destination / "topologies.jsonl").open("w") as out:
        for topology in topologies:
            out.write(json.dumps(topology, sort_keys=True, separators=(",", ":")) + "\n")
    command = [sys.executable, str(analyzer), str(destination / "rows.jsonl"),
               str(destination / "snapshots.jsonl"), str(destination / "topologies.jsonl"),
               str(destination / "summary.json"), *args]
    completed = subprocess.run(command, text=True, capture_output=True)
    (destination / "stdout.log").write_text(completed.stdout)
    (destination / "stderr.log").write_text(completed.stderr)
    (destination / "status").write_text(f"{completed.returncode}\n")
    summary = json.loads((destination / "summary.json").read_text())
    if completed.returncode != expected:
        raise SystemExit(f"{name}: expected {expected}, got {completed.returncode}")
    if expected == 0:
        if summary["non_timing_failures"]:
            raise SystemExit(f"{name}: unexpectedly invalid")
    elif (summary["classification"] != "inconclusive_non_timing_failure" or
          summary["diagnostic_reproduced"] or not summary["non_timing_failures"]):
        raise SystemExit(f"{name}: invalid case did not fail closed")
    records.append({"case": name, "expected_status": expected,
                    "actual_status": completed.returncode,
                    "classification": summary["classification"],
                    "non_timing_failures": summary["non_timing_failures"]})

(root / "analyzer-contract-results.json").write_text(json.dumps(records, indent=2, sort_keys=True) + "\n")
print(json.dumps({"status": "pass", "cases": len(records)}, sort_keys=True))

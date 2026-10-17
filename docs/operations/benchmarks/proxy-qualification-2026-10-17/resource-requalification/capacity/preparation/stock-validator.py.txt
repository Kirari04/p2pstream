import glob
import json
import os
import sys

results_dir, destination, candidate_label = sys.argv[1:]
summary = {"candidate_label": candidate_label, "topologies": {}, "accepted": True}
failures = []
def strict_zero(value):
    return type(value) is int and value == 0

def exact_success_responses(row):
    classes = row.get("status_classes")
    attempted = row.get("attempted")
    return (
        type(attempted) is int and attempted > 0
        and isinstance(classes, dict) and "2xx" in classes
        and all(key in {"1xx", "2xx", "3xx", "4xx", "5xx"}
                and type(value) is int and value >= 0 for key, value in classes.items())
        and classes["2xx"] == attempted and sum(classes.values()) == attempted
    )

def events_conserved(row):
    return (
        row.get("event_stats_captured") is True
        and type(row.get("persisted_event_delta")) is int
        and row["persisted_event_delta"] == row.get("attempted")
        and exact_success_responses(row)
        and strict_zero(row.get("errors"))
        and strict_zero(row.get("dropped_event_delta"))
        and strict_zero(row.get("event_queue_after_drain"))
        and row.get("event_drain_error") == ""
    )
def process_cgroup_ok(process):
    return (
        isinstance(process, dict)
        and bool(process.get("container"))
        and type(process.get("cgroup_inode")) is int
        and process.get("cgroup_inode") > 0
        and process.get("cgroup_memory_max_bytes") == 4294967296
        and process.get("cgroup_memory_events_captured") is True
        and strict_zero(process.get("oom_delta"))
        and strict_zero(process.get("oom_kill_delta"))
    )
for path in sorted(glob.glob(os.path.join(results_dir, "*.jsonl"))):
    topology = os.path.basename(path).removesuffix(".jsonl")
    if topology.startswith("compat-"):
        continue
    rows = [json.loads(line) for line in open(path, encoding="utf-8") if line.strip()]
    capacity = [row for row in rows if row.get("label") == candidate_label and row.get("scenario") == "capacity-1536-h1"]
    recovery = [row for row in rows if row.get("label") == candidate_label and row.get("scenario") == "recovery"]
    if len(capacity) != 1 or len(recovery) != 1:
        failures.append(f"{topology}: missing unique candidate capacity/recovery rows")
        continue
    capacity, recovery = capacity[0], recovery[0]
    topology_ok = (
        capacity.get("attempted") == 1536
        and capacity.get("useful_completed") == 1536
        and capacity.get("errors") == 0
        and capacity.get("status_classes", {}).get("2xx") == 1536
        and capacity.get("origin_peak_inflight") == 1536
        and capacity.get("barrier_post_release_hold_ms") == 200
        and capacity.get("owner_peak_reservations", {}).get("public_connection") == 1536
        and capacity.get("owner_peak_reservations", {}).get("public_request") == 1536
        and capacity.get("cgroup_memory_max_bytes") == 4294967296
        and capacity.get("server_ballast_bytes") == 1073741824
        and recovery.get("useful_completed") == recovery.get("attempted")
        and recovery.get("attempted") == 512
        and recovery.get("errors") == 0
        and events_conserved(capacity)
        and events_conserved(recovery)
        and process_cgroup_ok(capacity.get("server_process"))
        and process_cgroup_ok(recovery.get("server_process"))
    )
    if topology.startswith("external-"):
        expected_agents = 1 if "one-agent" in topology else 4
        processes = capacity.get("agent_processes", [])
        recovery_processes = recovery.get("agent_processes", [])
        server = capacity.get("server_process", {})
        inodes = [server.get("cgroup_inode")] + [process.get("cgroup_inode") for process in processes]
        topology_ok = topology_ok and (
            len(processes) == expected_agents
            and len(recovery_processes) == expected_agents
            and all(process_cgroup_ok(process) for process in processes)
            and all(process_cgroup_ok(process) for process in recovery_processes)
            and all(isinstance(inode, int) and inode > 0 for inode in inodes)
            and len(set(inodes)) == expected_agents + 1
        )
    if not topology_ok:
        failures.append(f"{topology}: candidate failed strict capacity/recovery gate")
    summary["topologies"][topology] = {
        "accepted": topology_ok,
        "capacity": capacity,
        "recovery": recovery,
    }
summary["accepted"] = not failures and len(summary["topologies"]) == 3
summary["compatibility_bursts"] = {}
for path in sorted(glob.glob(os.path.join(results_dir, "compat-*.jsonl"))):
    name = os.path.basename(path).removesuffix(".jsonl")
    rows = [json.loads(line) for line in open(path, encoding="utf-8") if line.strip()]
    capacity = [row for row in rows if row.get("label", "").startswith("current-agent-") and row.get("scenario") == "capacity-128-h1"]
    recovery = [row for row in rows if row.get("label", "").startswith("current-agent-") and row.get("scenario") == "recovery"]
    compatible = len(capacity) == 1 and len(recovery) == 1
    if compatible:
        capacity_row, recovery_row = capacity[0], recovery[0]
        rejections = capacity_row.get("admission_rejections", {})
        tunnel_samples = (
            capacity_row.get("agent_tunnels_initial", [])
            + capacity_row.get("agent_tunnels_high_water", [])
            + capacity_row.get("agent_tunnels_final", [])
        )
        agent_processes = capacity_row.get("agent_processes", [])
        agent_initial = agent_processes[0].get("agent_capacity_initial", {}) if len(agent_processes) == 1 else {}
        compatible = (
            capacity_row.get("attempted") == 128
            and capacity_row.get("useful_completed") == 128
            and capacity_row.get("errors") == 0
            and capacity_row.get("status_classes", {}).get("2xx") == 128
            and capacity_row.get("origin_peak_inflight") == 128
            and capacity_row.get("barrier_post_release_hold_ms") == 200
            and isinstance(rejections, dict) and len(rejections) > 0
            and all(value == 0 for value in rejections.values())
            and len(tunnel_samples) == 3
            and all(tunnel.get("connected") is True and tunnel.get("control_connected") is True and tunnel.get("flow_control") == "legacy" for tunnel in tunnel_samples)
            and agent_initial.get("aggregate_current_pool_bytes", 0) == 0
            and agent_initial.get("legacy_session_envelope_bytes", 0) > 0
            and recovery_row.get("useful_completed") == recovery_row.get("attempted")
            and recovery_row.get("attempted") == 512
            and recovery_row.get("errors") == 0
            and events_conserved(capacity_row)
            and events_conserved(recovery_row)
            and process_cgroup_ok(capacity_row.get("server_process"))
            and process_cgroup_ok(recovery_row.get("server_process"))
            and len(capacity_row.get("agent_processes", [])) == 1
            and len(recovery_row.get("agent_processes", [])) == 1
            and all(process_cgroup_ok(process) for process in capacity_row.get("agent_processes", []))
            and all(process_cgroup_ok(process) for process in recovery_row.get("agent_processes", []))
        )
    if not compatible:
        failures.append(f"{name}: mixed-version burst or recovery failed")
    summary["compatibility_bursts"][name] = {
        "accepted": compatible,
        "legacy_session_envelope_bytes": (
            capacity[0].get("agent_processes", [{}])[0].get("agent_capacity_initial", {}).get("legacy_session_envelope_bytes")
            if len(capacity) == 1 and len(capacity[0].get("agent_processes", [])) == 1 else None
        ),
        "capacity": capacity[0] if len(capacity) == 1 else None,
        "recovery": recovery[0] if len(recovery) == 1 else None,
    }
one = summary["compatibility_bursts"].get("compat-current-agent-old-server-1-lane", {})
four = summary["compatibility_bursts"].get("compat-current-agent-old-server-4-lane", {})
one_envelope = one.get("legacy_session_envelope_bytes")
four_envelope = four.get("legacy_session_envelope_bytes")
if not isinstance(one_envelope, int) or one_envelope <= 0 or four_envelope != 4 * one_envelope:
    failures.append("mixed-version configured physical-lane envelopes were not observed at an exact 1:4 ratio")
    if one:
        one["accepted"] = False
    if four:
        four["accepted"] = False
summary["accepted"] = not failures and len(summary["topologies"]) == 3 and len(summary["compatibility_bursts"]) == 2
summary["failures"] = failures
with open(destination + ".tmp", "w", encoding="utf-8") as output:
    json.dump(summary, output, indent=2, sort_keys=True)
    output.write("\n")
os.replace(destination + ".tmp", destination)
if not summary["accepted"]:
    raise SystemExit("; ".join(failures) or "missing topology results")

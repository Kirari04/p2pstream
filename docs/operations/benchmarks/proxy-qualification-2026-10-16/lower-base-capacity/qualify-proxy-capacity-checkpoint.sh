#!/usr/bin/env bash
# Build immutable candidate and 3ec137 compatibility binaries, then run the
# original combined-cgroup and separate one/four-agent 1,536-H1 capacity rows.
set -Eeuo pipefail

cd "$(dirname "$0")/.."

candidate_ref=""
output_root=""
baseline_ref=3ec137c3ad3d07028e867042fb00d3933912d7ab
while (($#)); do
  case "$1" in
    --candidate-ref) candidate_ref=$2; shift 2 ;;
    --output-root) output_root=$2; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
[[ -n $candidate_ref ]] || { echo "--candidate-ref is required" >&2; exit 2; }
candidate_ref=$(git rev-parse --verify "$candidate_ref^{commit}")
baseline_ref=$(git rev-parse --verify "$baseline_ref^{commit}")
candidate_short=${candidate_ref:0:12}
mkdir -p tmp/proxy-capacity-checkpoint
if [[ -z $output_root ]]; then
  output_root=$(mktemp -d -p tmp/proxy-capacity-checkpoint "$candidate_short.XXXXXX")
fi
mkdir -p "$output_root/builds" "$output_root/results" "$output_root/raw" "$output_root/provenance"
output_root=$(realpath "$output_root")

for tool in go git jq sha256sum docker haproxy openssl taskset python3 lscpu; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 1; }
done
docker info >/dev/null

candidate_dir=$(mktemp -d -p "$output_root/builds" candidate.XXXXXX)
baseline_dir=$(mktemp -d -p "$output_root/builds" baseline.XXXXXX)
git archive "$candidate_ref" | tar -x -C "$candidate_dir"
git archive "$baseline_ref" | tar -x -C "$baseline_dir"
cmp -s "$candidate_dir/scripts/qualify-proxy-capacity-checkpoint.sh" "$0" || {
  echo "running capacity orchestrator differs from candidate $candidate_ref" >&2
  exit 1
}
cmp -s "$candidate_dir/scripts/qualify-proxy-performance.sh" scripts/qualify-proxy-performance.sh || {
  echo "running qualification driver differs from candidate $candidate_ref" >&2
  exit 1
}
install -m 0644 "$candidate_dir/internal/server/testdata/proxy_qualification_external_legacy_test.go" \
  "$baseline_dir/internal/server/proxy_qualification_external_legacy_test.go"
install -m 0644 "$candidate_dir/internal/agent/testdata/proxy_qualification_external_legacy_test.go" \
  "$baseline_dir/internal/agent/proxy_qualification_external_legacy_test.go"
install -m 0755 "$0" "$output_root/provenance/qualify-proxy-capacity-checkpoint.sh"
install -m 0755 "$candidate_dir/scripts/qualify-proxy-performance.sh" "$output_root/provenance/qualify-proxy-performance.sh"
install -m 0644 "$candidate_dir/internal/server/testdata/proxy_qualification_external_legacy_test.go" \
  "$output_root/provenance/proxy_qualification_external_legacy_server_test.go"
install -m 0644 "$candidate_dir/internal/agent/testdata/proxy_qualification_external_legacy_test.go" \
  "$output_root/provenance/proxy_qualification_external_legacy_agent_test.go"

candidate_server=$output_root/builds/candidate-server.test
candidate_agent=$output_root/builds/candidate-agent.test
baseline_server=$output_root/builds/baseline-server.test
baseline_agent=$output_root/builds/baseline-agent.test
(cd "$candidate_dir" && go test -c -o "$candidate_server" ./internal/server)
(cd "$candidate_dir" && go test -c -o "$candidate_agent" ./internal/agent)
(cd "$baseline_dir" && go test -c -o "$baseline_server" ./internal/server)
(cd "$baseline_dir" && go test -c -o "$baseline_agent" ./internal/agent)

manifest=$output_root/manifest.json
jq -n \
  --arg candidate_ref "$candidate_ref" \
  --arg baseline_ref "$baseline_ref" \
  --arg go_version "$(go version)" \
  --arg kernel "$(uname -srvmo)" \
  --arg docker_version "$(docker version --format '{{.Server.Version}}')" \
  --arg haproxy_version "$(haproxy -v | head -1)" \
  --arg harness_sha256 "$(sha256sum "$output_root/provenance/qualify-proxy-performance.sh" | awk '{print $1}')" \
  --arg orchestrator_sha256 "$(sha256sum "$output_root/provenance/qualify-proxy-capacity-checkpoint.sh" | awk '{print $1}')" \
  --arg candidate_server_harness_sha256 "$(sha256sum "$candidate_dir/internal/server/proxy_qualification_test.go" | awk '{print $1}')" \
  --arg candidate_agent_harness_sha256 "$(sha256sum "$candidate_dir/internal/agent/proxy_qualification_external_test.go" | awk '{print $1}')" \
  --arg baseline_server_overlay_sha256 "$(sha256sum "$output_root/provenance/proxy_qualification_external_legacy_server_test.go" | awk '{print $1}')" \
  --arg baseline_agent_overlay_sha256 "$(sha256sum "$output_root/provenance/proxy_qualification_external_legacy_agent_test.go" | awk '{print $1}')" \
  --arg candidate_server_sha256 "$(sha256sum "$candidate_server" | awk '{print $1}')" \
  --arg candidate_agent_sha256 "$(sha256sum "$candidate_agent" | awk '{print $1}')" \
  --arg baseline_server_sha256 "$(sha256sum "$baseline_server" | awk '{print $1}')" \
  --arg baseline_agent_sha256 "$(sha256sum "$baseline_agent" | awk '{print $1}')" \
  --arg cpu_online "$(cat /sys/devices/system/cpu/online)" \
  --arg cpu_topology "$(lscpu -p=CPU,CORE,SOCKET,ONLINE | grep -v '^#')" \
  '{candidate_ref:$candidate_ref,baseline_ref:$baseline_ref,go_version:$go_version,kernel:$kernel,
    docker_version:$docker_version,haproxy_version:$haproxy_version,cpu_online:$cpu_online,cpu_topology:$cpu_topology,
    harness_sha256:$harness_sha256,orchestrator_sha256:$orchestrator_sha256,
    candidate_harnesses:{server_sha256:$candidate_server_harness_sha256,agent_sha256:$candidate_agent_harness_sha256},
    baseline_overlays:{server_sha256:$baseline_server_overlay_sha256,agent_sha256:$baseline_agent_overlay_sha256},
    binaries:{candidate_server_sha256:$candidate_server_sha256,candidate_agent_sha256:$candidate_agent_sha256,
      baseline_server_sha256:$baseline_server_sha256,baseline_agent_sha256:$baseline_agent_sha256},
    workload:{public_h1_connections:1536,barrier_seconds:3,server_memory_max_bytes:4294967296,
      server_ballast_bytes:1073741824,agent_memory_max_bytes:4294967296}}' > "$manifest"

run_capacity() {
  local topology=$1 external_agents=$2
  local output=$output_root/results/$topology.jsonl
  local -a external_args=()
  if ((external_agents > 0)); then
    external_args=(
      --external-agents "$external_agents"
      --baseline-agent-binary "$baseline_agent"
      --candidate-agent-binary "$candidate_agent"
    )
  else
    external_args=(--path-filter agent)
  fi
  local runner_log=$output_root/results/$topology.runner.log
  scripts/qualify-proxy-performance.sh \
    --runs 1 \
    --capacity-only --capacity-concurrency 1536 --capacity-protocol h1 \
    --ballast-mib 1024 \
    --baseline-label "baseline-$baseline_ref" \
    --baseline-binary "$baseline_server" \
    --candidate-label "candidate-$candidate_ref" \
    --candidate-binary "$candidate_server" \
    --driver-binary "$candidate_server" \
    --output "$output" \
    "${external_args[@]}" | tee "$runner_log"
  local raw
  raw=$(sed -n 's/^raw=//p' "$runner_log" | tail -1)
  [[ -d $raw ]] || { echo "missing raw directory for $topology" >&2; return 1; }
  mv "$raw" "$output_root/raw/$topology"
}

run_capacity combined-4g-server-plus-colocated-agent 0
run_capacity external-one-agent-separate-4g-cgroups 1
run_capacity external-four-agents-separate-4g-cgroups 4

run_legacy_compatibility_burst() {
  local lanes=$1
  local output=$output_root/results/compat-current-agent-old-server-${lanes}-lane.jsonl
  local runner_log=$output_root/results/compat-current-agent-old-server-${lanes}-lane.runner.log
  scripts/qualify-proxy-performance.sh \
    --runs 1 \
    --capacity-only --capacity-concurrency 128 --capacity-protocol h1 \
    --ballast-mib 1024 \
    --external-agents 1 --external-agent-tunnels "$lanes" \
    --candidate-external-mode legacy \
    --candidate-label "current-agent-$candidate_ref-old-server-${lanes}-lane" \
    --candidate-binary "$baseline_server" \
    --candidate-agent-binary "$candidate_agent" \
    --driver-binary "$candidate_server" \
    --output "$output" | tee "$runner_log"
  local raw
  raw=$(sed -n 's/^raw=//p' "$runner_log" | tail -1)
  [[ -d $raw ]] || { echo "missing raw directory for compatibility ${lanes}-lane" >&2; return 1; }
  mv "$raw" "$output_root/raw/compat-current-agent-old-server-${lanes}-lane"
}

# The current agent must tolerate a burst beyond the legacy 16-open backlog
# after an old server omits the aggregate ACK, for both one and four lanes.
run_legacy_compatibility_burst 1
run_legacy_compatibility_burst 4

summary=$output_root/summary.json
python3 - "$output_root/results" "$summary" "candidate-$candidate_ref" <<'PY'
import glob
import json
import os
import sys

results_dir, destination, candidate_label = sys.argv[1:]
summary = {"candidate_label": candidate_label, "topologies": {}, "accepted": True}
failures = []
def events_conserved(row):
    return (
        row.get("event_stats_captured") is True
        and row.get("persisted_event_delta") == row.get("attempted")
        and row.get("dropped_event_delta", 0) == 0
        and row.get("event_queue_after_drain", 0) == 0
        and not row.get("event_drain_error")
    )
def process_cgroup_ok(process):
    return (
        isinstance(process, dict)
        and bool(process.get("container"))
        and isinstance(process.get("cgroup_inode"), int)
        and process.get("cgroup_inode") > 0
        and process.get("cgroup_memory_max_bytes") == 4294967296
        and process.get("cgroup_memory_events_captured") is True
        and process.get("oom_delta", 0) == 0
        and process.get("oom_kill_delta", 0) == 0
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
with open(destination, "w", encoding="utf-8") as output:
    json.dump(summary, output, indent=2, sort_keys=True)
    output.write("\n")
if not summary["accepted"]:
    raise SystemExit("; ".join(failures) or "missing topology results")
PY

printf 'manifest=%s\nsummary=%s\nresults=%s\n' "$manifest" "$summary" "$output_root/results"

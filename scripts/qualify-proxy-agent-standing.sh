#!/usr/bin/env bash
# Measure the fixed standing cost of 16 and 32 negotiated aggregate agents.
# The delegated qualification runner owns all finite subprocess cleanup.
set -Eeuo pipefail

cd "$(dirname "$0")/.."
server_binary=""
agent_binary=""
driver_binary=""
output_dir=""
source_ref=""
while (($#)); do
  case "$1" in
    --server-binary) server_binary=$2; shift 2 ;;
    --agent-binary) agent_binary=$2; shift 2 ;;
    --driver-binary) driver_binary=$2; shift 2 ;;
    --output-dir) output_dir=$2; shift 2 ;;
    --source-ref) source_ref=$2; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
[[ -x $server_binary && -x $agent_binary ]] || {
  echo "executable --server-binary and --agent-binary are required" >&2
  exit 2
}
[[ -n $output_dir && -n $source_ref ]] || {
  echo "--output-dir and --source-ref are required" >&2
  exit 2
}
[[ -n $driver_binary ]] || driver_binary=$server_binary
[[ -x $driver_binary ]] || { echo "--driver-binary must be executable" >&2; exit 2; }
server_binary=$(realpath "$server_binary")
agent_binary=$(realpath "$agent_binary")
driver_binary=$(realpath "$driver_binary")
mkdir -p "$output_dir"
[[ -z $(find "$output_dir" -mindepth 1 -maxdepth 1 -print -quit) ]] || {
  echo "--output-dir must be empty: $output_dir" >&2
  exit 2
}
output_dir=$(realpath "$output_dir")
runner=$(realpath scripts/qualify-proxy-performance.sh)
wrapper=$(realpath scripts/qualify-proxy-agent-standing.sh)
if git_root=$(git rev-parse --show-toplevel 2>/dev/null); then
  resolved_source_ref=$(git rev-parse "$source_ref^{commit}")
  [[ $resolved_source_ref == "$source_ref" ]] || {
    echo "--source-ref must be the full resolved commit hash" >&2
    exit 2
  }
  for source_path in scripts/qualify-proxy-performance.sh scripts/qualify-proxy-agent-standing.sh internal/server/proxy_qualification_test.go; do
    cmp -s <(git show "$resolved_source_ref:$source_path") "$git_root/$source_path" || {
      echo "$source_path does not match --source-ref $resolved_source_ref" >&2
      exit 2
    }
  done
elif [[ ! $source_ref =~ ^[0-9a-f]{40}$ ]]; then
  echo "--source-ref must be a full commit hash when running outside a Git checkout" >&2
  exit 2
fi

server_sha=$(sha256sum "$server_binary" | awk '{print $1}')
agent_sha=$(sha256sum "$agent_binary" | awk '{print $1}')
driver_sha=$(sha256sum "$driver_binary" | awk '{print $1}')
runner_sha=$(sha256sum "$runner" | awk '{print $1}')
wrapper_sha=$(sha256sum "$wrapper" | awk '{print $1}')
replay_root="$output_dir/frozen-runner"
mkdir -p "$replay_root/scripts" "$replay_root/tmp"
cp "$runner" "$replay_root/scripts/qualify-proxy-performance.sh"
cp "$wrapper" "$replay_root/scripts/qualify-proxy-agent-standing.sh"
frozen_runner="$replay_root/scripts/qualify-proxy-performance.sh"
cp "$runner" "$output_dir/qualify-proxy-performance.sh.txt"
cp "$wrapper" "$output_dir/qualify-proxy-agent-standing.sh.txt"
chmod 0644 "$output_dir"/*.txt

active_runner_pid=""
stop_active_runner() {
  [[ -n $active_runner_pid ]] || return 0
  if kill -0 -- "-$active_runner_pid" 2>/dev/null; then
    kill -TERM -- "-$active_runner_pid" 2>/dev/null || true
    local deadline=$((SECONDS + 20))
    while kill -0 -- "-$active_runner_pid" 2>/dev/null && ((SECONDS < deadline)); do
      sleep 0.1
    done
    if kill -0 -- "-$active_runner_pid" 2>/dev/null; then
      kill -KILL -- "-$active_runner_pid" 2>/dev/null || true
    fi
  fi
  local deadline=$((SECONDS + 2))
  while kill -0 -- "-$active_runner_pid" 2>/dev/null && ((SECONDS < deadline)); do sleep 0.05; done
  if kill -0 -- "-$active_runner_pid" 2>/dev/null; then
    echo "standing runner $active_runner_pid did not join during emergency cleanup" >&2
    return 1
  fi
  wait "$active_runner_pid" 2>/dev/null || true
  active_runner_pid=""
}
finish() {
  local status=$?
  trap - EXIT
  set +e
  [[ -z $active_runner_pid ]] || { ((status != 0)) || status=1; }
  stop_active_runner || { ((status != 0)) || status=1; }
  if ! printf '{"exit_status":%d}\n' "$status" >"$output_dir/status.json.tmp" ||
      ! mv "$output_dir/status.json.tmp" "$output_dir/status.json"; then
    ((status != 0)) || status=1
  fi
  exit "$status"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

status16=-1
status32=-1
for agents in 16 32; do
  raw="$output_dir/standing-$agents.jsonl"
  raw_dir="$output_dir/raw/standing-$agents"
  stdout="$output_dir/standing-$agents.stdout.log"
  stderr="$output_dir/standing-$agents.stderr.log"
  mkdir -p "$raw_dir"
  jq -nc --argjson agents "$agents" --arg raw "$raw_dir" --arg result "$raw" \
    --arg stdout "$stdout" --arg stderr "$stderr" \
    '{agents:$agents,state:"started",raw_directory:$raw,result:$result,stdout:$stdout,stderr:$stderr}' \
    >>"$output_dir/attempts.jsonl"
  set +e
  setsid "$frozen_runner" \
    --candidate-binary "$server_binary" \
    --driver-binary "$driver_binary" \
    --candidate-agent-binary "$agent_binary" \
    --candidate-label "aggregate-standing-$agents" \
    --candidate-external-mode aggregate-v1 \
    --external-agents "$agents" --external-agent-tunnels 4 \
    --external-standing --output "$raw" --raw-dir "$raw_dir" >"$stdout" 2>"$stderr" &
  active_runner_pid=$!
  wait "$active_runner_pid"
  status=$?
  active_runner_pid=""
  set -e
  jq -nc --argjson agents "$agents" --arg raw "$raw_dir" --argjson status "$status" \
    '{agents:$agents,state:"finished",raw_directory:$raw,exit_status:$status}' >>"$output_dir/attempts.jsonl"
  if ((agents == 16)); then status16=$status; else status32=$status; fi
  if [[ -s $raw ]]; then
    jq -c --arg source_ref "$source_ref" --arg server_sha "$server_sha" \
      --arg agent_sha "$agent_sha" --arg driver_sha "$driver_sha" \
      --arg runner_sha "$runner_sha" --arg wrapper_sha "$wrapper_sha" \
      '. + {provenance:{source_ref:$source_ref,server_binary_sha256:$server_sha,
        agent_binary_sha256:$agent_sha,driver_binary_sha256:$driver_sha,
        runner_sha256:$runner_sha,wrapper_sha256:$wrapper_sha}}' "$raw" >"$raw.tmp"
    mv "$raw.tmp" "$raw"
  fi
  if ((status != 0)); then
    cat "$stderr" >&2
    echo "standing-$agents failed with status $status; later cohorts are unsafe and will not run" >&2
    break
  fi
done

python3 - "$output_dir" "$status16" "$status32" <<'PY'
import json
import pathlib
import statistics
import sys

root = pathlib.Path(sys.argv[1])
statuses = {16: int(sys.argv[2]), 32: int(sys.argv[3])}
expected_acceptance = {
    "expected_aggregate_mode_and_four_bulk_lanes", "strict_process_counts",
    "initial_exact_lane_shape", "initial_server_session_promises", "initial_server_owners_idle",
    "initial_agent_session_promises", "initial_agents_idle", "health_each_agent",
    "post_health_exact_lane_shape", "post_health_server_session_promises", "post_health_server_owners_idle",
    "post_health_agent_session_promises", "post_health_agents_idle", "readable_4g_no_swap_envelopes",
    "distinct_process_cgroups", "cgroup_identities_stable", "session_identities_stable",
    "agent_session_identities_nonempty_and_exact", "zero_oom_deltas",
}
def standing_contract(row, agents):
    if not isinstance(row, dict):
        return False
    acceptance = row.get("acceptance")
    if not (isinstance(acceptance, dict) and set(acceptance) == expected_acceptance
            and all(value is True for value in acceptance.values())
            and row.get("scenario") == "agent-standing-pool"
            and row.get("label") == f"aggregate-standing-{agents}"
            and type(row.get("external_agents")) is int and row["external_agents"] == agents
            and row.get("transport_mode") == "aggregate-v1"
            and type(row.get("bulk_sessions_per_agent")) is int and row["bulk_sessions_per_agent"] == 4
            and type(row.get("physical_sessions_per_agent")) is int and row["physical_sessions_per_agent"] == 5):
        return False
    for phase in ("initial", "final"):
        sample = row.get(phase)
        if not (isinstance(sample, dict) and isinstance(sample.get("agent_processes"), list)
                and len(sample["agent_processes"]) == agents
                and isinstance(sample.get("server_process"), dict)
                and isinstance(sample["server_process"].get("runtime"), dict)
                and isinstance(sample["server_process"]["runtime"].get("agent_tunnels"), list)
                and len(sample["server_process"]["runtime"]["agent_tunnels"]) == agents):
            return False
    health = row.get("health_roundtrips")
    return (isinstance(health, list) and len(health) == agents
            and all(isinstance(item, dict) and type(item.get("requested_agent_index")) is int for item in health)
            and sorted(item["requested_agent_index"] for item in health) == list(range(1, agents + 1)))

rows = {}
row_counts = {}
parse_errors = {}
for agents in (16, 32):
    path = root / f"standing-{agents}.jsonl"
    lines = [line for line in path.read_text(encoding="utf-8").splitlines() if line.strip()] if path.exists() else []
    row_counts[agents] = len(lines)
    try:
        data = [json.loads(line) for line in lines]
        rows[agents] = data[0] if len(data) == 1 else None
    except (ValueError, TypeError) as error:
        rows[agents] = None
        parse_errors[agents] = str(error)

summary = {"acceptance": {}, "cohorts": {}}
for agents, row in rows.items():
    complete = row is not None and statuses[agents] == 0
    accepted = complete and standing_contract(row, agents)
    summary["acceptance"][f"agents_{agents}"] = accepted
    if not accepted:
        summary["cohorts"][str(agents)] = {
            "exit_status": statuses[agents], "row_count": row_counts[agents],
            "error": parse_errors.get(agents, "row failed standing schema/acceptance or runner exit contract"),
        }
        continue
    try:
        initial = row["initial"]
        final = row["final"]
        summary["cohorts"][str(agents)] = {
            "exit_status": statuses[agents], "row_count": 1,
            "server_session_resource_bytes": sum(
                tunnel["session_resource_bytes"]
                for tunnel in initial["server_process"]["runtime"]["agent_tunnels"]),
            "agent_session_resource_bytes": sum(
                process["runtime"]["agent_capacity"]["session_resource_bytes"]
                for process in initial["agent_processes"]),
            "initial_parallel_cgroup_memory_current_sum_bytes": initial["parallel_cgroup_memory_current_sum_bytes"],
            "final_parallel_cgroup_memory_current_sum_bytes": final["parallel_cgroup_memory_current_sum_bytes"],
            "initial_server_memory_current_bytes": initial["server_process"]["cgroup_memory_current_bytes"],
            "final_server_memory_current_bytes": final["server_process"]["cgroup_memory_current_bytes"],
            "initial_agent_memory_current_bytes": [
                process["cgroup_memory_current_bytes"] for process in initial["agent_processes"]],
            "final_agent_memory_current_bytes": [
                process["cgroup_memory_current_bytes"] for process in final["agent_processes"]],
            "initial_agent_memory_current_median_bytes": statistics.median(
                process["cgroup_memory_current_bytes"] for process in initial["agent_processes"]),
            "final_agent_memory_current_median_bytes": statistics.median(
                process["cgroup_memory_current_bytes"] for process in final["agent_processes"]),
            "initial_sample_span_seconds": initial["sample_span_seconds"],
            "final_sample_span_seconds": final["sample_span_seconds"],
            "health_roundtrips": len(row["health_roundtrips"]),
            "acceptance": row["acceptance"],
        }
    except (KeyError, TypeError, ValueError, statistics.StatisticsError) as error:
        summary["acceptance"][f"agents_{agents}"] = False
        summary["cohorts"][str(agents)] = {
            "exit_status": statuses[agents], "row_count": row_counts[agents],
            "error": f"malformed standing measurements: {error}",
        }

with (root / "summary.json.tmp").open("w", encoding="utf-8") as output:
    json.dump(summary, output, indent=2, sort_keys=True)
    output.write("\n")
(root / "summary.json.tmp").replace(root / "summary.json")
if not all(summary["acceptance"].values()):
    raise SystemExit(f"standing-pool qualification failed: {summary['acceptance']}")
PY

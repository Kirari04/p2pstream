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
  if kill -0 "$active_runner_pid" 2>/dev/null; then
    kill -TERM -- "-$active_runner_pid" 2>/dev/null || true
    local deadline=$((SECONDS + 20))
    while kill -0 "$active_runner_pid" 2>/dev/null && ((SECONDS < deadline)); do
      sleep 0.1
    done
    if kill -0 "$active_runner_pid" 2>/dev/null; then
      kill -KILL -- "-$active_runner_pid" 2>/dev/null || true
    fi
  fi
  wait "$active_runner_pid" 2>/dev/null || true
  active_runner_pid=""
}
trap stop_active_runner EXIT
trap 'stop_active_runner; exit 130' INT
trap 'stop_active_runner; exit 143' TERM

status16=-1
status32=-1
for agents in 16 32; do
  raw="$output_dir/standing-$agents.jsonl"
  set +e
  setsid "$frozen_runner" \
    --candidate-binary "$server_binary" \
    --driver-binary "$driver_binary" \
    --candidate-agent-binary "$agent_binary" \
    --candidate-label "aggregate-standing-$agents" \
    --candidate-external-mode aggregate-v1 \
    --external-agents "$agents" --external-agent-tunnels 4 \
    --external-standing --output "$raw" &
  active_runner_pid=$!
  wait "$active_runner_pid"
  status=$?
  active_runner_pid=""
  set -e
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
rows = {}
for agents in (16, 32):
    path = root / f"standing-{agents}.jsonl"
    data = [json.loads(line) for line in path.open(encoding="utf-8") if line.strip()] if path.exists() else []
    rows[agents] = data[0] if len(data) == 1 else None

summary = {"acceptance": {}, "cohorts": {}}
for agents, row in rows.items():
    complete = row is not None and statuses[agents] == 0
    accepted = complete and all(row.get("acceptance", {}).values())
    summary["acceptance"][f"agents_{agents}"] = accepted
    if row is None:
        summary["cohorts"][str(agents)] = {"exit_status": statuses[agents], "row_count": 0}
        continue
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
with (root / "summary.json").open("w", encoding="utf-8") as output:
    json.dump(summary, output, indent=2, sort_keys=True)
    output.write("\n")
if not all(summary["acceptance"].values()):
    raise SystemExit(f"standing-pool qualification failed: {summary['acceptance']}")
PY

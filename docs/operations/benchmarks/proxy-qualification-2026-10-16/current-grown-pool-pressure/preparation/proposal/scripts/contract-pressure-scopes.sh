#!/usr/bin/env bash
# PREPARED ONLY: requires a separate workload-window grant before execution.
# No proxy/load requests, ballast, routes, veths, or netem are started.
set -Eeuo pipefail
if [[ ${1:-} != --inside ]]; then
  [[ $# == 4 && $1 == --output-root && $3 == --runner ]] || {
    echo "usage: bash $0 --output-root FRESH_DIR --runner REVIEWED_PRESSURE_RUNNER" >&2; exit 2;
  }
  [[ ! -e $2 ]] || { echo 'output directory already exists' >&2; exit 2; }
  mkdir -p "$2"
  export SCOPE_CONTRACT_ROOT SCOPE_CONTRACT_RUNNER SCOPE_CONTRACT_PARENT_NETNS SCOPE_CONTRACT_PARENT_PIDNS
  SCOPE_CONTRACT_ROOT=$(realpath "$2")
  SCOPE_CONTRACT_RUNNER=$(realpath "$4")
  SCOPE_CONTRACT_PARENT_NETNS=$(readlink /proc/self/ns/net)
  SCOPE_CONTRACT_PARENT_PIDNS=$(readlink /proc/self/ns/pid)
  sha256sum "$0" "$SCOPE_CONTRACT_RUNNER" >"$SCOPE_CONTRACT_ROOT/source.sha256"
  # Preserve the original pressure entry and PID topology, without the WAN
  # supervisor's extra PID/mount namespaces. Record the exact entry command.
  if unshare --user --map-root-user --net true 2>/dev/null; then
    printf 'unshare --user --map-root-user --net bash CONTRACT --inside\n' >"$SCOPE_CONTRACT_ROOT/entry.txt"
    exec unshare --user --map-root-user --net bash "$0" --inside
  fi
  printf 'sudo -n --preserve-env=CONTRACT_VARS unshare --net bash CONTRACT --inside\n' >"$SCOPE_CONTRACT_ROOT/entry.txt"
  exec sudo -n --preserve-env=SCOPE_CONTRACT_ROOT,SCOPE_CONTRACT_RUNNER,SCOPE_CONTRACT_PARENT_NETNS,SCOPE_CONTRACT_PARENT_PIDNS \
    unshare --net bash "$0" --inside
fi
root=$SCOPE_CONTRACT_ROOT
runner=$SCOPE_CONTRACT_RUNNER
[[ $(readlink /proc/self/ns/net) != "$SCOPE_CONTRACT_PARENT_NETNS" ]]
[[ $(readlink /proc/self/ns/pid) == "$SCOPE_CONTRACT_PARENT_PIDNS" ]]
for tool in systemd-run systemctl timeout setsid unshare nsenter python3 jq ps taskset; do command -v "$tool" >/dev/null; done
printf 'classification=bounded-scope-contract-only\npid_namespace=%s\nnet_namespace=%s\n' \
  "$(readlink /proc/self/ns/pid)" "$(readlink /proc/self/ns/net)" >"$root/topology.txt"
extract() { awk -v name="$1" '$0 == name "() {" {copy=1} copy {print} copy && $0 == "}" {exit}' "$runner"; }
for name in cleanup register_owned_group process_start_state assert_namespace_helper wait_ready join_owned_group stop_process cgroup_snapshot; do
  eval "$(extract "$name")"
done
units=()
declare -A owned_groups=()
stops=()
run_dir=$root
output=$root/results.jsonl
lifecycle_output=$root/lifecycle.jsonl
: >"$lifecycle_output"
cleanup_contract() {
  local status=$? unit state
  local -a active_units=()
  trap - EXIT INT TERM
  set +e
  # Kill only uniquely named scopes created by this contract if a group failed
  # to join; this is emergency cleanup and cannot make the contract succeed.
  for unit in "${units[@]}"; do
    state=$(timeout -k 1 2 systemctl --user show "$unit.scope" -p ActiveState --value 2>/dev/null)
    if [[ $state == active || $state == activating || $state == deactivating ]]; then
      ((status != 0)) || status=1
      active_units+=("$unit.scope")
    fi
  done
  if ((${#active_units[@]})); then
    timeout -k 1 3 systemctl --user kill --signal=KILL "${active_units[@]}" >/dev/null 2>&1
  fi
  (exit "$status")
  cleanup
}
trap cleanup_contract EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
boot_id=$(tr -d '\n' </proc/sys/kernel/random/boot_id)
mapfile -t cpus < <(python3 - <<'PY'
import os
for cpu in sorted(os.sched_getaffinity(0)):
    print(cpu)
PY
)
((${#cpus[@]} >= 2))
server_cpu_set=${cpus[0]}
agent_cpu_set=${cpus[1]}
cat >"$root/payload.py" <<'PY'
import json, os, pathlib, sys, time
ready, stop, status = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2]), int(sys.argv[3])
ready.write_text(json.dumps({"pid": os.getpid(), "net_namespace": os.readlink("/proc/self/ns/net"),
                             "pid_namespace": os.readlink("/proc/self/ns/pid")}) + "\n")
deadline = time.monotonic() + 30
while not stop.exists():
    if time.monotonic() >= deadline:
        raise SystemExit("bounded scope fixture timed out")
    time.sleep(.05)
raise SystemExit(status)
PY

for expected in 0 7; do
  case_dir=$root/exit-$expected
  mkdir -p "$case_dir"
  holder_stop=$case_dir/namespace.stop
  setsid unshare --net python3 "$root/payload.py" "$case_dir/namespace.ready" "$holder_stop" 0 \
    >"$case_dir/namespace.log" 2>&1 &
  namespace_pid=$!
  register_owned_group "$namespace_pid" "fixture-$expected-namespace"
  stops+=("$holder_stop")
  wait_ready "$case_dir/namespace.ready" "$namespace_pid"
  instance=$(process_start_state "$namespace_pid")
  [[ ${instance#* } != "$(readlink /proc/self/ns/net)" ]]
  assert_namespace_helper "$namespace_pid" "$instance"

  server_unit=p2p-pressure-scope-contract-server-$BASHPID-$expected
  agent_unit=p2p-pressure-scope-contract-agent-$BASHPID-$expected
  units+=("$server_unit" "$agent_unit")
  server_stop=$case_dir/server.stop
  agent_stop=$case_dir/agent.stop
  stops+=("$server_stop" "$agent_stop")
  # Exact original pressure scope/nsenter/taskset structure; only the finite
  # payload is substituted for the Go test binary. No traffic is generated.
  setsid systemd-run --user --scope --quiet --unit="$server_unit" \
    -p MemoryMax=4294967296 -p MemorySwapMax=0 -- nsenter -t "$$" -n env \
    taskset -c "$server_cpu_set" python3 "$root/payload.py" "$case_dir/server.ready" "$server_stop" 0 \
    >"$case_dir/server.log" 2>&1 &
  server_owner=$!
  register_owned_group "$server_owner" "fixture-$expected-server"
  wait_ready "$case_dir/server.ready" "$server_owner"
  assert_namespace_helper "$namespace_pid" "$instance"
  setsid systemd-run --user --scope --quiet --unit="$agent_unit" \
    -p MemoryMax=4294967296 -p MemorySwapMax=0 -- nsenter -t "$namespace_pid" -n env \
    taskset -c "$agent_cpu_set" python3 "$root/payload.py" "$case_dir/agent.ready" "$agent_stop" "$expected" \
    >"$case_dir/agent.log" 2>&1 &
  agent_owner=$!
  register_owned_group "$agent_owner" "fixture-$expected-agent"
  wait_ready "$case_dir/agent.ready" "$agent_owner"
  server_pid=$(jq -r .pid "$case_dir/server.ready")
  agent_pid=$(jq -r .pid "$case_dir/agent.ready")
  cgroup_snapshot "$server_pid" >"$case_dir/server-cgroup.json"
  cgroup_snapshot "$agent_pid" >"$case_dir/agent-cgroup.json"
  jq -e --arg unit "$server_unit.scope" '.cgroup_path | endswith("/" + $unit)' "$case_dir/server-cgroup.json" >/dev/null
  jq -e --arg unit "$agent_unit.scope" '.cgroup_path | endswith("/" + $unit)' "$case_dir/agent-cgroup.json" >/dev/null
  jq -es 'all(.[]; .memory_max_bytes == 4294967296 and .memory_swap_max_bytes == 0 and
      .memory_events.oom == 0 and .memory_events.oom_kill == 0) and
      (.[0].cgroup_path != .[1].cgroup_path)' "$case_dir/server-cgroup.json" "$case_dir/agent-cgroup.json" >/dev/null
  [[ $(jq -r .pid_namespace "$case_dir/server.ready") == "$SCOPE_CONTRACT_PARENT_PIDNS" ]]
  [[ $(jq -r .pid_namespace "$case_dir/agent.ready") == "$SCOPE_CONTRACT_PARENT_PIDNS" ]]
  [[ $(jq -r .net_namespace "$case_dir/server.ready") != $(jq -r .net_namespace "$case_dir/agent.ready") ]]
  if stop_process "fixture-$expected-agent" "$agent_owner" "$agent_stop"; then status=0; else status=$?; fi
  [[ $status == "$expected" ]]
  stop_process "fixture-$expected-server" "$server_owner" "$server_stop"
  stop_process "fixture-$expected-namespace" "$namespace_pid" "$holder_stop"
  for unit in "$server_unit" "$agent_unit"; do
    state=$(timeout -k 1 2 systemctl --user show "$unit.scope" -p ActiveState --value 2>/dev/null || true)
    [[ $state != active && $state != activating && $state != deactivating ]]
  done
  printf '{"expected_agent_status":%s,"observed_agent_status":%s,"all_groups_joined":true}\n' "$expected" "$status" >"$case_dir/contract-result.json"
done
[[ ${#owned_groups[@]} == 0 ]]
printf 'bounded original-topology scopes, zero/nonzero statuses, cgroups and namespace joins PASS\n'

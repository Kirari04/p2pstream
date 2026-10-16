#!/usr/bin/env bash
# Finite reverse-proxy qualification. It compares the production p2pstream
# public listener with a same-host HAProxy control and leaves JSONL/raw logs in
# ignored tmp/. It does not change host networking or start a persistent server.
set -Eeuo pipefail

cd "$(dirname "$0")/.."

smoke=0
runs=3
baseline_binary=""
baseline_label="baseline-ee1aba7"
candidate_binary=""
driver_binary=""
candidate_agent_binary=""
baseline_agent_binary=""
candidate_label="candidate"
output=""
use_cgroup=1
origin_tls=0
origin_http1=0
ballast_mib=0
agent_ballast_mib=0
decisive_only=0
capacity_only=0
capacity_concurrency=1536
capacity_protocol=both
decisive_duration=10
decisive_warmup=2
decisive_concurrency=""
profile_root=""
profile_mode="cpu"
scenario_filter=""
path_filter=""
stdlib_controls=0
stdlib_pooled_controls=0
stdlib_sharded_controls=0
controls_only=0
stdlib_max_idle=""
stdlib_pool=0
stdlib_shards=1
external_agents=0
external_agent_tunnels=4
management_url=""
management_ca_file=""
agent_public_id=""
agent_token=""
server_process_json=""
agent_processes_json=""
external_expected_mode=""
external_expected_bulk=""
candidate_external_mode=aggregate-v1
baseline_external_mode=legacy
external_lifecycle=0
while (($#)); do
  case "$1" in
    --smoke) smoke=1; runs=1; shift ;;
    --runs) runs=$2; shift 2 ;;
    --baseline-binary) baseline_binary=$2; shift 2 ;;
    --baseline-label) baseline_label=$2; shift 2 ;;
    --candidate-binary) candidate_binary=$2; shift 2 ;;
    --driver-binary) driver_binary=$2; shift 2 ;;
    --candidate-agent-binary) candidate_agent_binary=$2; shift 2 ;;
    --baseline-agent-binary) baseline_agent_binary=$2; shift 2 ;;
    --candidate-label) candidate_label=$2; shift 2 ;;
    --output) output=$2; shift 2 ;;
    --host-processes) use_cgroup=0; shift ;;
    --origin-tls) origin_tls=1; shift ;;
    --origin-http1) origin_http1=1; shift ;;
    --ballast-mib) ballast_mib=$2; shift 2 ;;
    --agent-ballast-mib) agent_ballast_mib=$2; shift 2 ;;
    --decisive-only) decisive_only=1; shift ;;
    --capacity-only) capacity_only=1; shift ;;
    --capacity-concurrency) capacity_concurrency=$2; shift 2 ;;
    --capacity-protocol) capacity_protocol=$2; shift 2 ;;
    --duration-seconds) decisive_duration=$2; shift 2 ;;
    --warmup-seconds) decisive_warmup=$2; shift 2 ;;
    --decisive-concurrency) decisive_concurrency=$2; shift 2 ;;
    --profile-dir) profile_root=$2; shift 2 ;;
    --profile-mode) profile_mode=$2; shift 2 ;;
    --scenario-filter) scenario_filter=$2; shift 2 ;;
    --path-filter) path_filter=$2; shift 2 ;;
    --stdlib-controls) stdlib_controls=1; shift ;;
    --stdlib-pooled-controls) stdlib_pooled_controls=1; shift ;;
    --stdlib-sharded-controls) stdlib_sharded_controls=1; shift ;;
    --diagnostic-controls-only) controls_only=1; shift ;;
    --external-agents) external_agents=$2; shift 2 ;;
    --external-agent-tunnels) external_agent_tunnels=$2; shift 2 ;;
    --candidate-external-mode) candidate_external_mode=$2; shift 2 ;;
    --baseline-external-mode) baseline_external_mode=$2; shift 2 ;;
    --external-lifecycle) external_lifecycle=1; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
if ((smoke)); then use_cgroup=0; fi
if ((controls_only && !stdlib_controls && !stdlib_pooled_controls && !stdlib_sharded_controls)); then
  echo "--diagnostic-controls-only requires a standard-library control" >&2
  exit 2
fi

for tool in go jq openssl haproxy python3 taskset; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 1; }
done
if ((use_cgroup)); then
  command -v docker >/dev/null || { echo "docker is required for the 4 GiB qualification cgroup" >&2; exit 1; }
  docker info >/dev/null
fi

mapfile -t available_cpus < <(python3 - <<'PY'
import os
for cpu in sorted(os.sched_getaffinity(0)):
    print(cpu)
PY
)
proxy_cpu_count=${#available_cpus[@]}
((proxy_cpu_count > 4)) && proxy_cpu_count=4
proxy_cpu_set=$(IFS=,; echo "${available_cpus[*]:0:proxy_cpu_count}")
agent_cpu_set=""
if ((external_agents > 0)); then
  if ((${#available_cpus[@]} < 9)); then
    echo "external-agent qualification requires at least nine allowed logical CPUs" >&2
    exit 2
  fi
  agent_cpu_count=4
  agent_cpu_set=$(IFS=,; echo "${available_cpus[*]:proxy_cpu_count:agent_cpu_count}")
  load_cpu_set=$(IFS=,; echo "${available_cpus[*]:proxy_cpu_count+agent_cpu_count}")
elif ((${#available_cpus[@]} > proxy_cpu_count)); then
  load_cpu_set=$(IFS=,; echo "${available_cpus[*]:proxy_cpu_count}")
else
  load_cpu_set=$proxy_cpu_set
fi
if [[ ! $runs =~ ^[1-9][0-9]*$ ]]; then
  echo "--runs must be a positive integer" >&2
  exit 2
fi
if [[ ! $external_agents =~ ^[0-4]$ || ! $external_agent_tunnels =~ ^[1-9][0-9]*$ || $external_agent_tunnels -gt 32 ]]; then
  echo "--external-agents must be 0..4 and --external-agent-tunnels must be 1..32" >&2
  exit 2
fi
case "$candidate_external_mode:$baseline_external_mode" in
  legacy:legacy|legacy:aggregate-v1|aggregate-v1:legacy|aggregate-v1:aggregate-v1) ;;
  *) echo "external modes must be legacy or aggregate-v1" >&2; exit 2 ;;
esac
if [[ ! $agent_ballast_mib =~ ^[0-9]+$ || $agent_ballast_mib -gt 2048 ]]; then
  echo "--agent-ballast-mib must be 0..2048" >&2
  exit 2
fi

run_dir=$(mktemp -d -p tmp proxy-qualification.XXXXXX)
mkdir -p "$run_dir/results"
if [[ -z $candidate_binary ]]; then
  candidate_binary="$run_dir/server-candidate.test"
  go test -c -o "$candidate_binary" ./internal/server
fi
candidate_binary=$(realpath "$candidate_binary")
if [[ -z $driver_binary ]]; then driver_binary=$candidate_binary; fi
driver_binary=$(realpath "$driver_binary")
if ((external_agents > 0)) && [[ -z $candidate_agent_binary ]]; then
  candidate_agent_binary="$run_dir/agent-candidate.test"
  go test -c -o "$candidate_agent_binary" ./internal/agent
fi
if [[ -n $candidate_agent_binary ]]; then candidate_agent_binary=$(realpath "$candidate_agent_binary"); fi
if [[ -n $baseline_binary ]]; then
  baseline_binary=$(realpath "$baseline_binary")
fi
if [[ -n $baseline_agent_binary ]]; then baseline_agent_binary=$(realpath "$baseline_agent_binary"); fi
if ((external_agents > 0)) && [[ -n $baseline_binary && -z $baseline_agent_binary ]]; then
  echo "--baseline-agent-binary is required with an external-agent baseline" >&2
  exit 2
fi
if [[ -z $output ]]; then
  output="$run_dir/results/results.jsonl"
fi
mkdir -p "$(dirname "$output")"
: > "$output"

cert="$run_dir/public.crt.pem"
key="$run_dir/public.key.pem"
bundle="$run_dir/public.bundle.pem"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
  -subj /CN=public.test -days 1 -keyout "$key" -out "$cert" >/dev/null 2>&1
awk '1' "$key" "$cert" > "$bundle"

children=()
stops=()
containers=()
cleanup() {
  for stop in "${stops[@]:-}"; do
    [[ -n $stop ]] && touch "$stop"
  done
  for pid in "${children[@]:-}"; do
    [[ -n $pid ]] && kill "$pid" 2>/dev/null || true
  done
  for container in "${containers[@]:-}"; do
    [[ -n $container ]] && docker kill "$container" >/dev/null 2>&1 || true
  done
  wait || true
}
trap cleanup EXIT INT TERM

wait_ready() {
  local file=$1 pid=$2
  local deadline=$((SECONDS + 20))
  while [[ ! -s $file ]]; do
    if ! kill -0 "$pid" 2>/dev/null; then
      echo "qualification process $pid exited before writing $file" >&2
      return 1
    fi
    if ((SECONDS >= deadline)); then
      echo "timed out waiting for $file" >&2
      return 1
    fi
    sleep 0.05
  done
  jq -e . "$file" >/dev/null
}

start_test_process() {
  local binary=$1 role=$2 name=$3
  local ready="$run_dir/$name.ready.json" stop="$run_dir/$name.stop" log="$run_dir/$name.log"
  local test_pattern='^TestProxyQualificationProcess$'
  [[ $role == proxy-external-agent ]] && test_pattern='^TestExternalProxyQualificationServerProcess$'
  [[ $role == agent-external ]] && test_pattern='^TestExternalProxyQualificationAgentProcess$'
  process_container=""
  role_cpu_set=$proxy_cpu_set
  [[ $role == origin ]] && role_cpu_set=$load_cpu_set
  [[ $role == agent-external ]] && role_cpu_set=$agent_cpu_set
  if [[ $role == proxy || $role == proxy-external-agent || $role == agent-external ]] && ((use_cgroup)); then
    process_container="p2pstream-qualification-${name//[^a-zA-Z0-9_.-]/-}-$$"
    containers+=("$process_container")
    docker run --rm --name "$process_container" --network host \
      --user "$(id -u):$(id -g)" \
      --memory 4g --memory-swap 4g --cpuset-cpus "$role_cpu_set" --pids-limit 8192 \
      --mount "type=bind,source=$binary,target=/server.test,readonly" \
      --mount "type=bind,source=$(realpath "$run_dir"),target=/qualification" \
      --env P2PSTREAM_QUALIFICATION_ROLE="$role" \
      --env P2PSTREAM_QUALIFICATION_READY="/qualification/$name.ready.json" \
      --env P2PSTREAM_QUALIFICATION_STOP="/qualification/$name.stop" \
      --env P2PSTREAM_QUALIFICATION_ORIGIN_URL="${origin_url:-}" \
      --env P2PSTREAM_QUALIFICATION_ORIGIN_TLS="$origin_tls" \
      --env P2PSTREAM_QUALIFICATION_ORIGIN_HTTP1="$origin_http1" \
      --env P2PSTREAM_QUALIFICATION_CERT=/qualification/public.crt.pem \
      --env P2PSTREAM_QUALIFICATION_KEY=/qualification/public.key.pem \
      --env P2PSTREAM_QUALIFICATION_MANAGEMENT_URL="$management_url" \
      --env P2PSTREAM_QUALIFICATION_MANAGEMENT_CA_FILE="$management_ca_file" \
      --env P2PSTREAM_QUALIFICATION_AGENT_PUBLIC_ID="$agent_public_id" \
      --env P2PSTREAM_QUALIFICATION_AGENT_TOKEN="$agent_token" \
      --env P2PSTREAM_QUALIFICATION_AGENT_TUNNELS="$external_agent_tunnels" \
      --env P2PSTREAM_QUALIFICATION_EXTERNAL_AGENTS="$external_agents" \
      --env GOMAXPROCS="$proxy_cpu_count" \
      --env P2PSTREAM_QUALIFICATION_BALLAST_MIB="$ballast_mib" \
      --env P2PSTREAM_QUALIFICATION_AGENT_BALLAST_MIB="$agent_ballast_mib" \
      --env P2PSTREAM_QUALIFICATION_PROFILE_DIR="${profile_root:+/qualification/profiles/$name}" \
      --env P2PSTREAM_QUALIFICATION_PROFILE_MODE="$profile_mode" \
      --env P2PSTREAM_QUALIFICATION_STDLIB_MAX_IDLE="${stdlib_max_idle:-32}" \
      --env P2PSTREAM_QUALIFICATION_STDLIB_POOL="$stdlib_pool" \
      --env P2PSTREAM_QUALIFICATION_STDLIB_SHARDS="$stdlib_shards" \
      debian:trixie-slim /server.test -test.run "$test_pattern" -test.timeout 15m >"$log" 2>&1 &
  else
    P2PSTREAM_QUALIFICATION_ROLE="$role" \
    P2PSTREAM_QUALIFICATION_READY="$ready" \
    P2PSTREAM_QUALIFICATION_STOP="$stop" \
    P2PSTREAM_QUALIFICATION_ORIGIN_URL="${origin_url:-}" \
    P2PSTREAM_QUALIFICATION_ORIGIN_TLS="$origin_tls" \
    P2PSTREAM_QUALIFICATION_ORIGIN_HTTP1="$origin_http1" \
    P2PSTREAM_QUALIFICATION_CERT="$cert" \
    P2PSTREAM_QUALIFICATION_KEY="$key" \
    P2PSTREAM_QUALIFICATION_MANAGEMENT_URL="$management_url" \
    P2PSTREAM_QUALIFICATION_MANAGEMENT_CA_FILE="$management_ca_file" \
    P2PSTREAM_QUALIFICATION_AGENT_PUBLIC_ID="$agent_public_id" \
    P2PSTREAM_QUALIFICATION_AGENT_TOKEN="$agent_token" \
    P2PSTREAM_QUALIFICATION_AGENT_TUNNELS="$external_agent_tunnels" \
    P2PSTREAM_QUALIFICATION_EXTERNAL_AGENTS="$external_agents" \
    GOMAXPROCS="$proxy_cpu_count" \
    P2PSTREAM_QUALIFICATION_BALLAST_MIB="$ballast_mib" \
    P2PSTREAM_QUALIFICATION_AGENT_BALLAST_MIB="$agent_ballast_mib" \
    P2PSTREAM_QUALIFICATION_PROFILE_DIR="${profile_root:+$profile_root/$name}" \
    P2PSTREAM_QUALIFICATION_PROFILE_MODE="$profile_mode" \
    P2PSTREAM_QUALIFICATION_STDLIB_MAX_IDLE="${stdlib_max_idle:-32}" \
    P2PSTREAM_QUALIFICATION_STDLIB_POOL="$stdlib_pool" \
    P2PSTREAM_QUALIFICATION_STDLIB_SHARDS="$stdlib_shards" \
      taskset -c "$role_cpu_set" "$binary" -test.run "$test_pattern" -test.timeout 15m >"$log" 2>&1 &
  fi
  process_pid=$!
  children+=("$process_pid")
  stops+=("$stop")
  wait_ready "$ready" "$process_pid"
  process_sample_pid=$process_pid
  if [[ -n $process_container ]]; then
    process_sample_pid=$(docker inspect --format '{{.State.Pid}}' "$process_container")
  fi
  process_ready=$ready
  process_stop=$stop
}

stop_process() {
  local pid=$1 stop=$2
  touch "$stop"
  local deadline=$((SECONDS + 10))
  while kill -0 "$pid" 2>/dev/null; do
    if ((SECONDS >= deadline)); then
      kill "$pid" 2>/dev/null || true
      break
    fi
    sleep 0.05
  done
  wait "$pid" 2>/dev/null || true
}

start_test_process "$driver_binary" origin origin
origin_pid=$process_pid
origin_stop=$process_stop
origin_url=$(jq -r .url "$process_ready")

if ((capacity_only)); then
  case "$capacity_protocol" in
    h1) scenario_rows=("capacity-$capacity_concurrency-h1 h1 delay $capacity_concurrency $capacity_concurrency 0" "recovery h2 small 64 512 0") ;;
    h2) scenario_rows=("capacity-$capacity_concurrency-h2 h2 delay $capacity_concurrency $capacity_concurrency 0" "recovery h2 small 64 512 0") ;;
    both) scenario_rows=(
      "capacity-$capacity_concurrency-h1 h1 delay $capacity_concurrency $capacity_concurrency 0"
      "capacity-$capacity_concurrency-h2 h2 delay $capacity_concurrency $capacity_concurrency 0"
      "recovery h2 small 64 512 0"
    ) ;;
    *) echo "--capacity-protocol must be h1, h2, or both" >&2; exit 2 ;;
  esac
elif ((decisive_only)); then
  h1_keepalive_concurrency=${decisive_concurrency:-128}
  h1_churn_concurrency=${decisive_concurrency:-64}
  h2_concurrency=${decisive_concurrency:-256}
  upload_concurrency=${decisive_concurrency:-128}
  scenario_rows=(
    "h1-keepalive-duration h1 small $h1_keepalive_concurrency $h1_keepalive_concurrency 0 $decisive_duration $decisive_warmup"
    "h1-churn-duration h1 small $h1_churn_concurrency $h1_churn_concurrency 0 $decisive_duration $decisive_warmup"
    "h2-multiplex-duration h2 small $h2_concurrency $h2_concurrency 0 $decisive_duration $decisive_warmup"
    "upload-duration h2 upload $upload_concurrency $upload_concurrency 0 $decisive_duration $decisive_warmup"
  )
  if ((external_agents > 0)); then
    scenario_rows+=(
      'large-stream h2 slow 64 256 0'
      'slow-reader h2 slow 32 128 0'
    )
  fi
elif ((smoke)); then
  scenario_rows=(
    'tls-idle tls small 32 32 0'
    'steady-100 h2 small 16 100 100'
    'h1-keepalive h1 small 16 128 0'
    'h1-churn h1 small 8 64 0'
    'h2-multiplex h2 small 32 128 0'
    'upload h2 upload 16 64 0'
    'slow-reader h2 slow 8 16 0'
    'capacity-1536-h1 h1 delay 96 192 0'
    'capacity-1536-h2 h2 delay 96 192 0'
    'recovery h2 small 16 64 0'
  )
else
  scenario_rows=(
    'tls-idle tls small 512 512 0'
    'steady-500 h2 small 128 1500 500'
    'h1-keepalive h1 small 128 5000 0'
    'h1-churn h1 small 64 1200 0'
    'h2-multiplex h2 small 256 5000 0'
    'upload h2 upload 128 2000 0'
    'slow-reader h2 slow 32 128 0'
    'capacity-1536-h1 h1 delay 1536 3072 0'
    'capacity-1536-h2 h2 delay 1536 3072 0'
    'recovery h2 small 64 512 0'
  )
fi

run_load() {
  local label=$1 path_name=$2 base_url=$3 admin_url=$4 pids=$5 scenario_row=$6 sequence=$7
  read -r scenario protocol suffix concurrency requests offered_rps duration_seconds warmup_seconds <<<"$scenario_row"
  local result="$run_dir/results/${sequence}-${label}-${path_name}-${scenario}.json"
  P2PSTREAM_QUALIFICATION_ROLE=load \
  P2PSTREAM_QUALIFICATION_URL="$base_url/$suffix" \
  P2PSTREAM_QUALIFICATION_SCENARIO="$scenario" \
  P2PSTREAM_QUALIFICATION_PROTOCOL="$protocol" \
  P2PSTREAM_QUALIFICATION_CONCURRENCY="$concurrency" \
  P2PSTREAM_QUALIFICATION_REQUESTS="$requests" \
  P2PSTREAM_QUALIFICATION_OFFERED_RPS="$offered_rps" \
  P2PSTREAM_QUALIFICATION_DURATION_SECONDS="${duration_seconds:-0}" \
  P2PSTREAM_QUALIFICATION_WARMUP_SECONDS="${warmup_seconds:-0}" \
  P2PSTREAM_QUALIFICATION_LABEL="$label" \
  P2PSTREAM_QUALIFICATION_PATH="$path_name" \
  P2PSTREAM_QUALIFICATION_PIDS="$pids" \
  P2PSTREAM_QUALIFICATION_ADMIN_URL="$admin_url" \
  P2PSTREAM_QUALIFICATION_PROXY_CPU_SET="$proxy_cpu_set" \
  P2PSTREAM_QUALIFICATION_SERVER_PROCESS="$server_process_json" \
  P2PSTREAM_QUALIFICATION_AGENT_PROCESSES="$agent_processes_json" \
  P2PSTREAM_QUALIFICATION_LOAD_CPU_SET="$load_cpu_set" \
  P2PSTREAM_QUALIFICATION_RESOURCE_ENVELOPE="${resource_envelope:-}" \
  P2PSTREAM_QUALIFICATION_SEQUENCE="$sequence" \
  P2PSTREAM_QUALIFICATION_BALLAST_MIB="${result_ballast_mib:-0}" \
  P2PSTREAM_QUALIFICATION_RESULT="$result" \
    taskset -c "$load_cpu_set" "$driver_binary" -test.run '^TestProxyQualificationLoad$' -test.timeout 10m \
    >"$result.log" 2>&1
  if [[ -n $external_expected_mode ]]; then
    if ! jq -e \
      --arg mode "$external_expected_mode" \
      --arg scenario "$scenario" \
      --argjson agents "$external_agents" \
      --argjson bulk "$external_expected_bulk" \
      --argjson agent_ballast_bytes "$((agent_ballast_mib * 1024 * 1024))" \
      --argjson constrained "$use_cgroup" '
        def tunnel_shape:
          length == $agents and all(.[];
            .connected == true and
            .flow_control == $mode and
            .control_connected == true and
            (.bulk_sessions // 0) == $bulk);
        (.attempted > 0) and
        (.event_stats_captured == true) and
        (.persisted_event_delta == .attempted) and
        ((.dropped_event_delta // 0) == 0) and
        ((.event_queue_after_drain // 0) == 0) and
        ((.event_drain_error // "") == "") and
        (.agent_tunnels_initial | tunnel_shape) and
        (.agent_tunnels_high_water | tunnel_shape) and
        (.agent_tunnels_final | tunnel_shape) and
        (if $mode == "aggregate-v1" then
          all(.agent_processes[]; (.ballast_bytes // 0) == $agent_ballast_bytes)
        else $agent_ballast_bytes == 0 end) and
        (if ($scenario | startswith("capacity-")) then true else
          (.useful_completed == .attempted) and
          (.errors == 0) and
          ([.status_classes | to_entries[] | select(.key != "2xx" and .value != 0)] | length == 0)
        end) and
        (if $constrained == 1 then
          (.server_process.container | length > 0) and
          (.server_process.cgroup_memory_max_bytes == 4294967296) and
          (.server_process.cgroup_memory_events_captured == true) and
          ((.server_process.oom_delta // 0) == 0) and
          ((.server_process.oom_kill_delta // 0) == 0) and
          (.agent_processes | length == $agents) and
          (all(.agent_processes[];
            (.container | length > 0) and
            .cgroup_memory_max_bytes == 4294967296 and
            .cgroup_memory_events_captured == true and
            ((.oom_delta // 0) == 0) and
            ((.oom_kill_delta // 0) == 0))) and
          ([.server_process.cgroup_inode] + [.agent_processes[].cgroup_inode] |
            all(.[]; . > 0) and (unique | length == ($agents + 1)))
        else true end)
      ' "$result" >/dev/null; then
      echo "external qualification acceptance failed: $result" >&2
      jq '{scenario,attempted,useful_completed,errors,status_classes,event_stats_captured,persisted_event_delta,dropped_event_delta,event_queue_after_drain,event_drain_error,agent_tunnels_initial,agent_tunnels_high_water,agent_tunnels_final,server_process,agent_processes}' "$result" >&2
      return 1
    fi
  fi
  jq -c --argjson run "$sequence" '. + {sequence: $run}' "$result" >> "$output"
}

sequence=0
wait_external_agent_tunnels() {
  local admin_url=$1 expected_mode=$2 expected_agents=$3 expected_bulk=$4
  python3 - "$admin_url" "$expected_mode" "$expected_agents" "$expected_bulk" <<'PY'
import json
import sys
import time
import urllib.request

url, mode, raw_agents, raw_bulk = sys.argv[1:]
agents, bulk = int(raw_agents), int(raw_bulk)
deadline = time.monotonic() + 30
last = None
while time.monotonic() < deadline:
    try:
        with urllib.request.urlopen(url, timeout=.5) as response:
            last = json.load(response).get("agent_tunnels", [])
        if len(last) == agents and all(
            item.get("connected")
            and item.get("flow_control") == mode
            and item.get("control_connected")
            and item.get("bulk_sessions", 0) == bulk
            for item in last
        ):
            raise SystemExit(0)
    except Exception as error:
        last = repr(error)
    time.sleep(.05)
raise SystemExit(f"external agent tunnels did not become ready: {last}")
PY
}

qualification_process_json() {
  local role=$1 pid=$2 admin_url=$3 cpu_set=$4 container=$5
  jq -cn --arg role "$role" --argjson pid "$pid" --arg admin_url "$admin_url" \
    --arg cpu_set "$cpu_set" --arg container "$container" \
    '{role:$role,pid:$pid,admin_url:$admin_url,cpu_set:$cpu_set,container:$container}'
}

capture_external_lifecycle() {
  local phase=$1 offsets=$2 label=$3 baseline_tunnels=$4 server_spec=$5 agent_specs=$6
  python3 - "$phase" "$offsets" "$label" "$baseline_tunnels" "$server_spec" "$agent_specs" "$output" "$external_expected_mode" "$external_expected_bulk" <<'PY'
import json
import os
import sys
import time
import urllib.request

phase, raw_offsets, label, raw_baseline, raw_server_spec, raw_specs, destination, mode, raw_bulk = sys.argv[1:]
offsets = [float(value) for value in raw_offsets.split(",")]
baseline_runtime = json.loads(raw_baseline)
baseline = baseline_runtime.get("agent_tunnels", [])
server_spec = json.loads(raw_server_spec)
specs = json.loads(raw_specs)
bulk = int(raw_bulk)
started = time.monotonic()

def fetch(url):
    with urllib.request.urlopen(url, timeout=2) as response:
        return json.load(response)

def read_int(path):
    try:
        return int(open(path, encoding="utf-8").read().strip())
    except (OSError, ValueError):
        return None

def process_stats(spec):
    pid = int(spec["pid"])
    root = f"/proc/{pid}/root/sys/fs/cgroup"
    memory_stat = {}
    memory_events = {}
    try:
        for line in open(f"{root}/memory.stat", encoding="utf-8"):
            key, value = line.split()
            memory_stat[key] = int(value)
        for line in open(f"{root}/memory.events", encoding="utf-8"):
            key, value = line.split()
            memory_events[key] = int(value)
        statm = open(f"/proc/{pid}/statm", encoding="utf-8").read().split()
        rss = int(statm[1]) * os.sysconf("SC_PAGE_SIZE")
    except (OSError, ValueError, IndexError):
        memory_stat, memory_events, rss = {}, {}, None
    return {
        "role": spec.get("role"),
        "pid": pid,
        "container": spec.get("container"),
        "rss_bytes": rss,
        "cgroup_memory_bytes": read_int(f"{root}/memory.current"),
        "cgroup_sock_bytes": memory_stat.get("sock"),
        "cgroup_memory_events": memory_events,
        "runtime": fetch(spec["admin_url"]),
    }

rows = []
for offset in offsets:
    delay = started + offset - time.monotonic()
    if delay > 0:
        time.sleep(delay)
    row = {
        "label": label,
        "scenario": "agent-idle-lifecycle",
        "lifecycle_phase": phase,
        "requested_offset_seconds": offset,
        "observed_offset_seconds": time.monotonic() - started,
        "transport_mode": mode,
        "server_process": process_stats(server_spec),
        "agent_processes": [process_stats(spec) for spec in specs],
    }
    rows.append(row)
    with open(destination, "a", encoding="utf-8") as output:
        output.write(json.dumps(row, separators=(",", ":"), sort_keys=True) + "\n")

baseline_streams = {
    (tunnel.get("id"), lane.get("lane")): lane.get("streams", 0)
    for tunnel in baseline
    for lane in tunnel.get("lanes", [])
}
final = rows[-1]
final_tunnels = final["server_process"].get("runtime", {}).get("agent_tunnels", [])
final_streams = {
    (tunnel.get("id"), lane.get("lane")): lane.get("streams", 0)
    for tunnel in final_tunnels
    for lane in tunnel.get("lanes", [])
}
shape_ok = len(final_tunnels) == len(baseline) and all(
    tunnel.get("connected") is True
    and tunnel.get("control_connected") is True
    and tunnel.get("flow_control") == mode
    and tunnel.get("bulk_sessions", 0) == bulk
    and (mode != "aggregate-v1" or len(tunnel.get("lanes", [])) == bulk + 1)
    for tunnel in final_tunnels
)
agents_idle = all(
    process.get("runtime", {}).get("agent_capacity", {}).get("in_use") == 0
    and process.get("runtime", {}).get("agent_capacity", {}).get("legacy_stream_supplement_bytes", 0) == 0
    for process in final["agent_processes"]
)
def events_unchanged(first, last):
    before = first.get("cgroup_memory_events", {})
    after = last.get("cgroup_memory_events", {})
    return all(key in before and key in after and before[key] == after[key] for key in ("oom", "oom_kill"))

processes_readable = all(
    process.get("rss_bytes") is not None
    and process.get("cgroup_memory_bytes") is not None
    and process.get("cgroup_sock_bytes") is not None
    for process in [final["server_process"], *final["agent_processes"]]
)
events_ok = events_unchanged(rows[0]["server_process"], final["server_process"]) and all(
    events_unchanged(rows[0]["agent_processes"][index], process)
    for index, process in enumerate(final["agent_processes"])
)
baseline_receive_owner = baseline_runtime.get("owner_reservations", {}).get("receive_window")
final_receive_owner = final["server_process"].get("runtime", {}).get("owner_reservations", {}).get("receive_window")
server_owner_idle = isinstance(baseline_receive_owner, int) and final_receive_owner == baseline_receive_owner
if not shape_ok or final_streams != baseline_streams or not agents_idle or not events_ok or not processes_readable or not server_owner_idle:
    raise SystemExit(
        f"{phase} lifecycle did not return to the connected idle baseline: "
        f"shape={shape_ok} streams={final_streams} baseline={baseline_streams} agents_idle={agents_idle} "
        f"events={events_ok} readable={processes_readable} server_owner_idle={server_owner_idle}"
    )
PY
}

run_external_p2pstream() {
  local label=$1 binary=$2 agent_binary=$3 iteration=$4 expected_mode=$5
  local name="${label}-${iteration}-external"
  if ((use_cgroup)); then
    management_ca_file="/qualification/$name.management-ca.pem"
  else
    management_ca_file="$run_dir/$name.management-ca.pem"
  fi
  start_test_process "$binary" proxy-external-agent "$name-server"
  local server_pid=$process_pid server_stop=$process_stop server_sample_pid=$process_sample_pid
  local server_container=$process_container server_ready=$process_ready
  local base_url admin_url
  base_url=$(jq -r .url "$server_ready")
  admin_url=$(jq -r .admin_url "$server_ready")
  management_url=$(jq -r .management_url "$server_ready")
  management_ca_file=$(jq -r .management_ca_file "$server_ready")
  server_process_json=$(qualification_process_json server "$server_sample_pid" "$admin_url" "$proxy_cpu_set" "$server_container")
  agent_processes_json='[]'
  local -a agent_pids=() agent_stops=()
  local agent_count
  agent_count=$(jq '.agents | length' "$server_ready")
  [[ $agent_count -eq $external_agents ]] || { echo "server returned $agent_count agent credentials, want $external_agents" >&2; return 1; }
  for ((agent_index=0; agent_index<agent_count; agent_index++)); do
    agent_public_id=$(jq -r ".agents[$agent_index].public_id" "$server_ready")
    agent_token=$(jq -r ".agents[$agent_index].token" "$server_ready")
    start_test_process "$agent_binary" agent-external "$name-agent-$((agent_index + 1))"
    agent_pids+=("$process_pid")
    agent_stops+=("$process_stop")
    local agent_admin_url agent_spec
    agent_admin_url=$(jq -r .admin_url "$process_ready")
    agent_spec=$(qualification_process_json "agent-$((agent_index + 1))" "$process_sample_pid" "$agent_admin_url" "$agent_cpu_set" "$process_container")
    agent_processes_json=$(jq -cn --argjson existing "$agent_processes_json" --argjson next "$agent_spec" '$existing + [$next]')
  done
  agent_public_id=""
  agent_token=""
  local expected_bulk=$((external_agent_tunnels - 1))
  [[ $expected_mode == aggregate-v1 ]] && expected_bulk=$external_agent_tunnels
  wait_external_agent_tunnels "$admin_url" "$expected_mode" "$external_agents" "$expected_bulk"
  external_expected_mode=$expected_mode
  external_expected_bulk=$expected_bulk
  local lifecycle_baseline='[]'
  if ((external_lifecycle)); then
    [[ $expected_mode == aggregate-v1 ]] || { echo "--external-lifecycle currently requires aggregate-v1" >&2; return 1; }
    lifecycle_baseline=$(python3 - "$admin_url" <<'PY'
import json, sys, urllib.parse, urllib.request
url = sys.argv[1] + ("&" if "?" in sys.argv[1] else "?") + urllib.parse.urlencode({"owners": "1"})
with urllib.request.urlopen(url, timeout=2) as response:
    json.dump(json.load(response), sys.stdout, separators=(",", ":"))
PY
)
  fi
  for scenario_row in "${scenario_rows[@]}"; do
    [[ -n $scenario_filter && ${scenario_row%% *} != "$scenario_filter" ]] && continue
    [[ $scenario_row == tls-idle* ]] && continue
    sequence=$((sequence + 1))
    if ((use_cgroup)); then
      resource_envelope="p2pstream-server-4g+${external_agents}x-agent-4g:flow_control=$expected_mode,separate-cgroups,server_ballast_mib=$ballast_mib,agent_ballast_mib_each=$agent_ballast_mib"
    else
      resource_envelope="p2pstream-server+${external_agents}x-agent:flow_control=$expected_mode,host-process-smoke,server_ballast_mib=$ballast_mib,agent_ballast_mib_each=$agent_ballast_mib"
    fi
    result_ballast_mib=$ballast_mib
    run_load "$label" "agent-external" "$base_url/agent" "$admin_url" "$server_sample_pid" "$scenario_row" "$sequence"
    [[ $scenario_row == capacity-* ]] && sleep 0.5
  done
  if ((external_lifecycle)); then
    local lifecycle_server_process_json
    lifecycle_server_process_json=$(jq -c '.admin_url += (if contains("?") then "&owners=1" else "?owners=1" end)' <<<"$server_process_json")
    capture_external_lifecycle natural "1,5,30,35,65" "$label" "$lifecycle_baseline" "$lifecycle_server_process_json" "$agent_processes_json"
    sequence=$((sequence + 1))
    run_load "$label" "agent-external" "$base_url/agent" "$admin_url" "$server_sample_pid" \
      "lifecycle-repopulate h1 small 128 128 0" "$sequence"
    python3 - "$admin_url" <<'PY'
import sys, urllib.parse, urllib.request
url = sys.argv[1] + ("&" if "?" in sys.argv[1] else "?") + urllib.parse.urlencode({"close_idle_agent_transports": "1"})
with urllib.request.urlopen(url, timeout=5) as response:
    response.read()
PY
    capture_external_lifecycle forced-idle-close "0,1,5,35" "$label" "$lifecycle_baseline" "$lifecycle_server_process_json" "$agent_processes_json"
  fi
  for ((agent_index=${#agent_pids[@]}-1; agent_index>=0; agent_index--)); do
    stop_process "${agent_pids[$agent_index]}" "${agent_stops[$agent_index]}"
  done
  stop_process "$server_pid" "$server_stop"
  management_url=""
  management_ca_file=""
  server_process_json=""
  agent_processes_json=""
  external_expected_mode=""
  external_expected_bulk=""
}

run_p2pstream() {
  local label=$1 binary=$2 iteration=$3
  start_test_process "$binary" proxy "${label}-${iteration}"
  local pid=$process_pid stop=$process_stop
  local sample_pid=$process_sample_pid
  local container=$process_container
  local base_url admin_url
  base_url=$(jq -r .url "$process_ready")
  admin_url=$(jq -r .admin_url "$process_ready")
  server_process_json=$(qualification_process_json "server+colocated-agent" "$sample_pid" "$admin_url" "$proxy_cpu_set" "$container")
  agent_processes_json='[]'
  local paths=(direct agent)
  if [[ -n $path_filter ]]; then paths=("$path_filter"); fi
  if [[ -z $path_filter ]] && ((iteration % 2 == 0)); then paths=(agent direct); fi
  for path_name in "${paths[@]}"; do
    for scenario_row in "${scenario_rows[@]}"; do
      [[ -n $scenario_filter && ${scenario_row%% *} != "$scenario_filter" ]] && continue
      [[ $path_name == agent && $scenario_row == tls-idle* ]] && continue
      sequence=$((sequence + 1))
      resource_envelope=p2pstream-server+in-process-real-agent
      result_ballast_mib=$ballast_mib
      run_load "$label" "$path_name" "$base_url/$path_name" "$admin_url" "$sample_pid" "$scenario_row" "$sequence"
      [[ $scenario_row == capacity-* ]] && sleep 0.5
    done
  done
  stop_process "$pid" "$stop"
  server_process_json=""
  agent_processes_json=""
}

run_haproxy() {
  local iteration=$1
  local port
  port=$(python3 - <<'PY'
import socket
s = socket.socket()
s.bind(('127.0.0.1', 0))
print(s.getsockname()[1])
s.close()
PY
)
  local origin_address=${origin_url#*://}
  local cfg="$run_dir/haproxy-$iteration.cfg" log="$run_dir/haproxy-$iteration.log"
  {
    echo 'global'
    echo '  maxconn 4096'
    echo "  nbthread $proxy_cpu_count"
    echo 'defaults'
    echo '  mode http'
    echo '  timeout connect 5s'
    echo '  timeout client 30s'
    echo '  timeout server 30s'
    echo 'frontend public'
    echo "  bind 127.0.0.1:$port ssl crt $bundle alpn h2,http/1.1"
    echo '  default_backend origin'
    echo 'backend origin'
    if [[ $origin_url == https://* ]]; then
      echo "  server origin $origin_address ssl verify none"
    else
      echo "  server origin $origin_address"
    fi
  } > "$cfg"
  taskset -c "$proxy_cpu_set" /usr/sbin/haproxy -db -f "$cfg" >"$log" 2>&1 &
  local pid=$!
  children+=("$pid")
  local deadline=$((SECONDS + 10))
  while ! openssl s_client -connect "127.0.0.1:$port" -servername public.test </dev/null >/dev/null 2>&1; do
    kill -0 "$pid" 2>/dev/null || { echo "HAProxy failed to start" >&2; return 1; }
    ((SECONDS < deadline)) || { echo "HAProxy start timeout" >&2; return 1; }
    sleep 0.05
  done
  for scenario_row in "${scenario_rows[@]}"; do
    [[ -n $scenario_filter && ${scenario_row%% *} != "$scenario_filter" ]] && continue
    sequence=$((sequence + 1))
    resource_envelope=haproxy-process
    result_ballast_mib=0
    run_load "haproxy-3.2.9" "direct-control" "https://127.0.0.1:$port" "" "$pid" "$scenario_row" "$sequence"
    [[ $scenario_row == capacity-* ]] && sleep 0.5
  done
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
}

run_stdlib_control() {
  local iteration=$1 idle=$2 pooled=$3 shards=${4:-1}
  stdlib_max_idle=$idle
  stdlib_pool=$pooled
  stdlib_shards=$shards
  local label="go-stdlib-idle-$idle"
  ((pooled)) && label="go-stdlib-pooled-idle-$idle"
  ((shards > 1)) && label="go-stdlib-pooled-shards-${shards}-total-idle-${idle}"
  start_test_process "$driver_binary" stdlib "${label}-${iteration}"
  local pid=$process_pid stop=$process_stop base_url admin_url
  base_url=$(jq -r .url "$process_ready")
  admin_url=$(jq -r .admin_url "$process_ready")
  for scenario_row in "${scenario_rows[@]}"; do
    [[ -n $scenario_filter && ${scenario_row%% *} != "$scenario_filter" ]] && continue
    sequence=$((sequence + 1))
    resource_envelope="go-stdlib-reverse-proxy-diagnostic:shards=$shards,total_idle=$idle,per_shard_idle=$((idle / shards)),pooled_buffer=$pooled"
    result_ballast_mib=0
    run_load "$label" "direct-diagnostic" "$base_url" "$admin_url" "$process_sample_pid" "$scenario_row" "$sequence"
  done
  stop_process "$pid" "$stop"
  stdlib_max_idle=""
  stdlib_pool=0
  stdlib_shards=1
}

for ((iteration=1; iteration<=runs; iteration++)); do
  if ((controls_only)); then
    :
  elif ((external_agents > 0)) && [[ -n $baseline_binary ]]; then
    if ((iteration % 2)); then
      run_external_p2pstream "$baseline_label" "$baseline_binary" "$baseline_agent_binary" "$iteration" "$baseline_external_mode"
      run_external_p2pstream "$candidate_label" "$candidate_binary" "$candidate_agent_binary" "$iteration" "$candidate_external_mode"
    else
      run_external_p2pstream "$candidate_label" "$candidate_binary" "$candidate_agent_binary" "$iteration" "$candidate_external_mode"
      run_external_p2pstream "$baseline_label" "$baseline_binary" "$baseline_agent_binary" "$iteration" "$baseline_external_mode"
    fi
  elif ((external_agents > 0)); then
    run_external_p2pstream "$candidate_label" "$candidate_binary" "$candidate_agent_binary" "$iteration" "$candidate_external_mode"
  elif [[ -n $baseline_binary ]]; then
    if ((iteration % 2)); then
      run_p2pstream "$baseline_label" "$baseline_binary" "$iteration"
      run_p2pstream "$candidate_label" "$candidate_binary" "$iteration"
    else
      run_p2pstream "$candidate_label" "$candidate_binary" "$iteration"
      run_p2pstream "$baseline_label" "$baseline_binary" "$iteration"
    fi
  else
    run_p2pstream "$candidate_label" "$candidate_binary" "$iteration"
  fi
  if ((stdlib_controls)); then
    run_stdlib_control "$iteration" 32 0
    run_stdlib_control "$iteration" 128 0
  fi
  if ((stdlib_pooled_controls)); then
    run_stdlib_control "$iteration" 32 1
    run_stdlib_control "$iteration" 128 1
  fi
  if ((stdlib_sharded_controls)); then
    run_stdlib_control "$iteration" 128 1 4
  fi
  ((controls_only)) || run_haproxy "$iteration"
done

stop_process "$origin_pid" "$origin_stop"
printf 'results=%s\nraw=%s\n' "$(realpath "$output")" "$(realpath "$run_dir")"

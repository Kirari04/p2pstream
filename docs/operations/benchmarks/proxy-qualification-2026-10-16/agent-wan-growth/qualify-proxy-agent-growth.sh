#!/usr/bin/env bash
# Finite negotiated-agent growth qualification. The public client and server
# share one disposable namespace; the real agent and its origin use a second,
# with all tunnel traffic crossing an 80 ms / 1 Gbit/s routed path.
set -Eeuo pipefail

if [[ ${1:-} != --inside ]]; then
  cd "$(dirname "$0")/.."
  server_binary=""
  aggregate_agent_binary=""
  legacy_agent_binary=""
  output=""
  runs=2
  warmup_mib=256
  steady_mib=1024
  transfer_repetitions=3
  required_pool_mib=8
  server_source_ref=""
  aggregate_agent_source_ref=""
  legacy_agent_source_ref=""
  smoke_mode=0
  while (($#)); do
    case "$1" in
      --server-binary) server_binary=$2; shift 2 ;;
      --aggregate-agent-binary) aggregate_agent_binary=$2; shift 2 ;;
      --legacy-agent-binary) legacy_agent_binary=$2; shift 2 ;;
      --output) output=$2; shift 2 ;;
      --runs) runs=$2; shift 2 ;;
      --warmup-mib) warmup_mib=$2; shift 2 ;;
      --steady-mib) steady_mib=$2; shift 2 ;;
      --transfer-repetitions) transfer_repetitions=$2; shift 2 ;;
      --required-pool-mib) required_pool_mib=$2; shift 2 ;;
      --server-source-ref) server_source_ref=$2; shift 2 ;;
      --aggregate-agent-source-ref) aggregate_agent_source_ref=$2; shift 2 ;;
      --legacy-agent-source-ref) legacy_agent_source_ref=$2; shift 2 ;;
      --smoke)
        smoke_mode=1
        runs=1
        warmup_mib=4
        steady_mib=4
        transfer_repetitions=1
        required_pool_mib=2
        shift
        ;;
      *) echo "unknown argument: $1" >&2; exit 2 ;;
    esac
  done
  [[ -x $server_binary && -x $aggregate_agent_binary && -x $legacy_agent_binary ]] || {
    echo "executable --server-binary, --aggregate-agent-binary and --legacy-agent-binary are required" >&2
    exit 2
  }
  [[ $runs =~ ^[1-9][0-9]*$ ]] || { echo "--runs must be positive" >&2; exit 2; }
  for value in "$warmup_mib" "$steady_mib" "$transfer_repetitions" "$required_pool_mib"; do
    [[ $value =~ ^[0-9]+$ ]] || { echo "transfer sizes, repetitions and required growth must be non-negative integers" >&2; exit 2; }
  done
  ((warmup_mib > 0 && steady_mib > 0 && transfer_repetitions > 0)) || {
    echo "transfer sizes and repetitions must be positive" >&2
    exit 2
  }
  ((required_pool_mib > 2 || (smoke_mode == 1 && required_pool_mib == 2))) || {
    echo "--required-pool-mib must be greater than the 2 MiB initial pool (exactly 2 is allowed only with --smoke)" >&2
    exit 2
  }
  [[ -n $server_source_ref && -n $aggregate_agent_source_ref && -n $legacy_agent_source_ref ]] || {
    echo "--server-source-ref, --aggregate-agent-source-ref and --legacy-agent-source-ref are required" >&2
    exit 2
  }
  mkdir -p tmp/proxy-agent-growth
  [[ -n $output ]] || output=tmp/proxy-agent-growth/results.jsonl
  export P2PSTREAM_GROWTH_SERVER_BINARY P2PSTREAM_GROWTH_AGGREGATE_AGENT_BINARY P2PSTREAM_GROWTH_LEGACY_AGENT_BINARY
  export P2PSTREAM_GROWTH_OUTPUT P2PSTREAM_GROWTH_RUNS P2PSTREAM_GROWTH_PARENT_NETNS
  export P2PSTREAM_GROWTH_WARMUP_MIB P2PSTREAM_GROWTH_STEADY_MIB P2PSTREAM_GROWTH_TRANSFER_REPETITIONS P2PSTREAM_GROWTH_REQUIRED_MIB
  export P2PSTREAM_GROWTH_SERVER_SOURCE_REF P2PSTREAM_GROWTH_AGGREGATE_AGENT_SOURCE_REF P2PSTREAM_GROWTH_LEGACY_AGENT_SOURCE_REF
  export P2PSTREAM_GROWTH_SMOKE_MODE
  P2PSTREAM_GROWTH_SERVER_BINARY=$(realpath "$server_binary")
  P2PSTREAM_GROWTH_AGGREGATE_AGENT_BINARY=$(realpath "$aggregate_agent_binary")
  P2PSTREAM_GROWTH_LEGACY_AGENT_BINARY=$(realpath "$legacy_agent_binary")
  P2PSTREAM_GROWTH_OUTPUT=$(realpath -m "$output")
  P2PSTREAM_GROWTH_RUNS=$runs
  P2PSTREAM_GROWTH_WARMUP_MIB=$warmup_mib
  P2PSTREAM_GROWTH_STEADY_MIB=$steady_mib
  P2PSTREAM_GROWTH_TRANSFER_REPETITIONS=$transfer_repetitions
  P2PSTREAM_GROWTH_REQUIRED_MIB=$required_pool_mib
  P2PSTREAM_GROWTH_SERVER_SOURCE_REF=$server_source_ref
  P2PSTREAM_GROWTH_AGGREGATE_AGENT_SOURCE_REF=$aggregate_agent_source_ref
  P2PSTREAM_GROWTH_LEGACY_AGENT_SOURCE_REF=$legacy_agent_source_ref
  P2PSTREAM_GROWTH_SMOKE_MODE=$smoke_mode
  P2PSTREAM_GROWTH_PARENT_NETNS=$(readlink /proc/self/ns/net)
  if unshare --user --map-root-user --net true 2>/dev/null; then
    exec unshare --user --map-root-user --net "$0" --inside
  fi
  exec sudo -n --preserve-env=P2PSTREAM_GROWTH_SERVER_BINARY,P2PSTREAM_GROWTH_AGGREGATE_AGENT_BINARY,P2PSTREAM_GROWTH_LEGACY_AGENT_BINARY,P2PSTREAM_GROWTH_OUTPUT,P2PSTREAM_GROWTH_RUNS,P2PSTREAM_GROWTH_PARENT_NETNS,P2PSTREAM_GROWTH_WARMUP_MIB,P2PSTREAM_GROWTH_STEADY_MIB,P2PSTREAM_GROWTH_TRANSFER_REPETITIONS,P2PSTREAM_GROWTH_REQUIRED_MIB,P2PSTREAM_GROWTH_SERVER_SOURCE_REF,P2PSTREAM_GROWTH_AGGREGATE_AGENT_SOURCE_REF,P2PSTREAM_GROWTH_LEGACY_AGENT_SOURCE_REF,P2PSTREAM_GROWTH_SMOKE_MODE \
    unshare --net "$0" --inside
fi

[[ -n ${P2PSTREAM_GROWTH_PARENT_NETNS:-} && $(readlink /proc/self/ns/net) != "$P2PSTREAM_GROWTH_PARENT_NETNS" ]] || {
  echo "refusing to configure networking outside a disposable namespace" >&2
  exit 1
}
for tool in ip tc nsenter jq python3 taskset openssl sha256sum ping; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 1; }
done

server_binary=$P2PSTREAM_GROWTH_SERVER_BINARY
aggregate_agent_binary=$P2PSTREAM_GROWTH_AGGREGATE_AGENT_BINARY
legacy_agent_binary=$P2PSTREAM_GROWTH_LEGACY_AGENT_BINARY
output=$P2PSTREAM_GROWTH_OUTPUT
runs=$P2PSTREAM_GROWTH_RUNS
warmup_mib=$P2PSTREAM_GROWTH_WARMUP_MIB
steady_mib=$P2PSTREAM_GROWTH_STEADY_MIB
transfer_repetitions=$P2PSTREAM_GROWTH_TRANSFER_REPETITIONS
required_pool_mib=$P2PSTREAM_GROWTH_REQUIRED_MIB
server_source_ref=$P2PSTREAM_GROWTH_SERVER_SOURCE_REF
aggregate_agent_source_ref=$P2PSTREAM_GROWTH_AGGREGATE_AGENT_SOURCE_REF
legacy_agent_source_ref=$P2PSTREAM_GROWTH_LEGACY_AGENT_SOURCE_REF
smoke_mode=$P2PSTREAM_GROWTH_SMOKE_MODE
largest_transfer_mib=$((warmup_mib > steady_mib ? warmup_mib : steady_mib))
transfer_timeout_seconds=$((60 + (largest_transfer_mib + 1) / 2))
warmup_lane_rounds=4
transfer_mib_per_cohort=$((2 * (warmup_lane_rounds + 1) * warmup_mib + 2 * transfer_repetitions * steady_mib))
overall_timeout_seconds=$((300 + runs * 2 * (transfer_mib_per_cohort + 1) / 2))
mkdir -p "$(dirname "$output")"
run_dir=$(mktemp -d -p "$(dirname "$output")" proxy-agent-growth.XXXXXX)
: > "$output"

children=()
stops=()
cleanup() {
  for stop in "${stops[@]:-}"; do [[ -n $stop ]] && touch "$stop"; done
  for pid in "${children[@]:-}"; do [[ -n $pid ]] && kill "$pid" 2>/dev/null || true; done
  wait || true
}
trap cleanup EXIT INT TERM

unshare --net sleep "$overall_timeout_seconds" &
agent_namespace_pid=$!
children+=("$agent_namespace_pid")
unshare --net sleep "$overall_timeout_seconds" &
router_namespace_pid=$!
children+=("$router_namespace_pid")
for pid in "$agent_namespace_pid" "$router_namespace_pid"; do
  for ((attempt=0; attempt<100; attempt++)); do
    [[ $(readlink "/proc/$pid/ns/net") != "$(readlink /proc/self/ns/net)" ]] && break
    sleep .01
  done
  [[ $(readlink "/proc/$pid/ns/net") != "$(readlink /proc/self/ns/net)" ]]
done
agent_ns() { nsenter -t "$agent_namespace_pid" -n "$@"; }
router_ns() { nsenter -t "$router_namespace_pid" -n "$@"; }
ip link set lo up
agent_ns ip link set lo up
router_ns ip link set lo up
ip link add growth-server type veth peer name growth-rs
ip link set growth-rs netns "$router_namespace_pid"
ip link add growth-agent type veth peer name growth-rr
ip link set growth-agent netns "$agent_namespace_pid"
ip link set growth-rr netns "$router_namespace_pid"
ip addr add 10.211.0.1/24 dev growth-server
ip link set growth-server up
router_ns ip addr add 10.211.0.2/24 dev growth-rs
router_ns ip addr add 10.212.0.1/24 dev growth-rr
router_ns ip link set growth-rs up
router_ns ip link set growth-rr up
agent_ns ip addr add 10.212.0.2/24 dev growth-agent
agent_ns ip link set growth-agent up
ip route add 10.212.0.0/24 via 10.211.0.2
agent_ns ip route add 10.211.0.0/24 via 10.212.0.1
router_ns sysctl -qw net.ipv4.ip_forward=1
for iface in growth-rs growth-rr; do
  router_ns tc qdisc add dev "$iface" root netem delay 40ms rate 1gbit limit 100000
done

measured_rtt_ms=$(agent_ns ping -c 5 -q 10.211.0.1 | awk -F/ '/rtt|round-trip/ {print $5}')
python3 - "$measured_rtt_ms" <<'PY'
import sys
rtt = float(sys.argv[1])
if not 60 <= rtt <= 120:
    raise SystemExit(f"measured RTT {rtt}ms is outside the expected 60..120ms envelope")
PY

server_binary_sha256=$(sha256sum "$server_binary" | awk '{print $1}')
aggregate_agent_binary_sha256=$(sha256sum "$aggregate_agent_binary" | awk '{print $1}')
legacy_agent_binary_sha256=$(sha256sum "$legacy_agent_binary" | awk '{print $1}')
script_sha256=$(sha256sum "$0" | awk '{print $1}')

mapfile -t cpus < <(python3 - <<'PY'
import os
for cpu in sorted(os.sched_getaffinity(0)):
    print(cpu)
PY
)
((${#cpus[@]} >= 9)) || { echo "at least nine logical CPUs are required" >&2; exit 2; }
server_cpu_set=$(IFS=,; echo "${cpus[*]:0:4}")
agent_cpu_set=$(IFS=,; echo "${cpus[*]:4:4}")
load_cpu_set=$(IFS=,; echo "${cpus[*]:8}")

wait_ready() {
  local path=$1 pid=$2
  for ((attempt=0; attempt<400; attempt++)); do
    [[ -s $path ]] && { jq -e . "$path" >/dev/null; return; }
    kill -0 "$pid" 2>/dev/null || { echo "process $pid exited before $path" >&2; return 1; }
    sleep .05
  done
  echo "timeout waiting for $path" >&2
  return 1
}
stop_process() {
  local pid=$1 stop=$2
  touch "$stop"
  for ((attempt=0; attempt<200; attempt++)); do
    kill -0 "$pid" 2>/dev/null || break
    sleep .05
  done
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
}
query_agent_json() {
  local pid=$1 url=$2
  nsenter -t "$pid" -n python3 - "$url" <<'PY'
import json, sys, urllib.request
with urllib.request.urlopen(sys.argv[1], timeout=2) as response:
    json.dump(json.load(response), sys.stdout, separators=(",", ":"))
PY
}

close_idle_agent_transports() {
  local admin_url=$1
  python3 - "$admin_url" <<'PY'
import sys, urllib.parse, urllib.request
url = sys.argv[1] + ("&" if "?" in sys.argv[1] else "?") + urllib.parse.urlencode({"close_idle_agent_transports": "1"})
with urllib.request.urlopen(url, timeout=5) as response:
    response.read()
PY
}

wait_aggregate_lanes_grown_and_quiescent() {
  local server_admin=$1 agent_admin=$2 required_bytes=$3
  local server_runtime agent_runtime
  for ((attempt=0; attempt<600; attempt++)); do
    server_runtime=$(python3 - "$server_admin" <<'PY'
import json, sys, urllib.request
with urllib.request.urlopen(sys.argv[1], timeout=2) as response:
    json.dump(json.load(response), sys.stdout, separators=(",", ":"))
PY
)
    agent_runtime=$(query_agent_json "$agent_namespace_pid" "$agent_admin")
    if jq -e --argjson required "$required_bytes" '
      [.agent_tunnels[0].lanes[] | select(.aggregate == true and .lane > 0)] as $lanes |
      ($lanes | length == 4) and
      all($lanes[];
        .receive_limit_bytes > $required and
        .receive_available_bytes == .receive_limit_bytes and
        .free_units == .total_units and
        ((.buffered_units // 0) == 0) and
        ((.pending_refund_bytes // 0) == 0) and
        ((.streams // 0) == 0))
    ' <<<"$server_runtime" >/dev/null &&
      jq -e --argjson required "$required_bytes" '
        [.tunnel_sessions[] | select(.aggregate == true and .lane > 0)] as $lanes |
        ($lanes | length == 4) and
        all($lanes[];
          .receive_limit_bytes > $required and
          .receive_available_bytes == .receive_limit_bytes and
          .free_units == .total_units and
          ((.buffered_units // 0) == 0) and
          ((.pending_refund_bytes // 0) == 0))
      ' <<<"$agent_runtime" >/dev/null; then
      return 0
    fi
    sleep .05
  done
  echo "aggregate receiving lanes did not become grown and quiescent" >&2
  jq '{agent_tunnels}' <<<"$server_runtime" >&2
  jq '{tunnel_sessions,agent_capacity}' <<<"$agent_runtime" >&2
  return 1
}

cert=$run_dir/public.crt.pem
key=$run_dir/public.key.pem
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
  -subj /CN=public.test -days 1 -keyout "$key" -out "$cert" >/dev/null 2>&1
origin_ready=$run_dir/origin.ready.json
origin_stop=$run_dir/origin.stop
nsenter -t "$agent_namespace_pid" -n env \
  P2PSTREAM_QUALIFICATION_ROLE=origin \
  P2PSTREAM_QUALIFICATION_READY="$origin_ready" \
  P2PSTREAM_QUALIFICATION_STOP="$origin_stop" \
  P2PSTREAM_QUALIFICATION_BIND_ADDRESS=10.212.0.2 \
  taskset -c "$agent_cpu_set" "$server_binary" -test.run '^TestProxyQualificationProcess$' -test.timeout "${overall_timeout_seconds}s" \
  >"$run_dir/origin.log" 2>&1 &
origin_pid=$!
children+=("$origin_pid")
stops+=("$origin_stop")
wait_ready "$origin_ready" "$origin_pid"
origin_url=$(jq -r .url "$origin_ready")

wait_mode() {
  local admin_url=$1 mode=$2 bulk=$3
  python3 - "$admin_url" "$mode" "$bulk" <<'PY'
import json, sys, time, urllib.request
url, mode, bulk = sys.argv[1], sys.argv[2], int(sys.argv[3])
deadline = time.monotonic() + 30
last = None
while time.monotonic() < deadline:
    try:
        with urllib.request.urlopen(url, timeout=.5) as response:
            last = json.load(response).get("agent_tunnels", [])
        if len(last) == 1 and last[0].get("connected") and last[0].get("flow_control") == mode and last[0].get("control_connected") and last[0].get("bulk_sessions") == bulk:
            lanes = last[0].get("lanes", [])
            if mode == "legacy" and len(lanes) == 4 and all(not lane.get("aggregate", False) for lane in lanes):
                raise SystemExit(0)
            bulk_lanes = [lane for lane in lanes if lane.get("lane") != 0]
            if mode == "aggregate-v1" and len(lanes) == 5 and len(bulk_lanes) == 4 and all(lane.get("aggregate") and lane.get("receive_limit_bytes") == 2097152 for lane in bulk_lanes):
                raise SystemExit(0)
    except Exception as error:
        last = repr(error)
    time.sleep(.05)
raise SystemExit(f"mode/lane/initial-window check failed: {last}")
PY
}

sequence=0
run_transfer() {
  local label=$1 mode=$2 server_pid=$3 public_url=$4 server_admin=$5 agent_pid=$6 agent_admin=$7
  local stage=$8 direction=$9 payload_bytes=${10} repetition=${11}
  local scenario suffix
  if [[ $direction == download ]]; then
    scenario=growth-download
    suffix=bulk
  else
    scenario=growth-upload
    suffix=bulk-upload
  fi
  sequence=$((sequence + 1))
  local raw="$run_dir/$sequence-$label-$stage-$direction-$repetition.json"
  local server_spec
  server_spec=$(jq -cn --argjson pid "$server_pid" --arg admin "$server_admin" --arg cpus "$server_cpu_set" '{role:"server",pid:$pid,admin_url:$admin,cpu_set:$cpus}')
  P2PSTREAM_QUALIFICATION_ROLE=load \
  P2PSTREAM_QUALIFICATION_URL="$public_url/agent/$suffix" \
  P2PSTREAM_QUALIFICATION_SCENARIO="$scenario" \
  P2PSTREAM_QUALIFICATION_PROTOCOL=h2 \
  P2PSTREAM_QUALIFICATION_CONCURRENCY=1 \
  P2PSTREAM_QUALIFICATION_REQUESTS=1 \
  P2PSTREAM_QUALIFICATION_OFFERED_RPS=0 \
  P2PSTREAM_QUALIFICATION_DURATION_SECONDS=0 \
  P2PSTREAM_QUALIFICATION_WARMUP_SECONDS=0 \
  P2PSTREAM_QUALIFICATION_GROWTH_PAYLOAD_BYTES="$payload_bytes" \
  P2PSTREAM_QUALIFICATION_LABEL="$label" \
  P2PSTREAM_QUALIFICATION_PATH=agent-external-growth \
  P2PSTREAM_QUALIFICATION_PIDS="$server_pid" \
  P2PSTREAM_QUALIFICATION_ADMIN_URL="$server_admin" \
  P2PSTREAM_QUALIFICATION_PROXY_CPU_SET="$server_cpu_set" \
  P2PSTREAM_QUALIFICATION_SERVER_PROCESS="$server_spec" \
  P2PSTREAM_QUALIFICATION_LOAD_CPU_SET="$load_cpu_set" \
  P2PSTREAM_QUALIFICATION_RESOURCE_ENVELOPE="p2pstream-external-agent-tunnel-netns:80ms,1gbit,current-server,mode=$mode" \
  P2PSTREAM_QUALIFICATION_SEQUENCE="$sequence" \
  P2PSTREAM_QUALIFICATION_RESULT="$raw" \
    taskset -c "$load_cpu_set" "$server_binary" -test.run '^TestProxyQualificationLoad$' -test.timeout "${transfer_timeout_seconds}s" \
    >"$raw.log" 2>&1
  local agent_runtime
  agent_runtime=$(query_agent_json "$agent_namespace_pid" "$agent_admin")
  jq -e --arg mode "$mode" --arg direction "$direction" --argjson bytes "$payload_bytes" '
    (.attempted == 1 and .useful_completed == 1 and .errors == 0 and .status_classes["2xx"] == 1) and
    (.event_stats_captured == true and .persisted_event_delta == 1 and ((.dropped_event_delta // 0) == 0) and
      ((.event_queue_after_drain // 0) == 0) and ((.event_drain_error // "") == "")) and
    (.configured_payload_bytes == $bytes) and
    (if $direction == "download" then .response_bytes == $bytes else .response_bytes == 32 end) and
    (.agent_tunnels_final | length == 1 and .[0].flow_control == $mode)
  ' "$raw" >/dev/null
  if [[ $mode == aggregate-v1 && $stage == steady && $direction == download ]]; then
    if ((required_pool_mib > 0)); then
      jq -e --argjson required "$((required_pool_mib << 20))" \
        '[.agent_tunnels_final[0].lanes[] | select(.aggregate == true and .lane > 0) | .receive_limit_bytes] | length == 4 and min > $required' "$raw" >/dev/null
    fi
  fi
  if [[ $mode == aggregate-v1 && $stage == steady && $direction == upload ]]; then
    jq -e --argjson required "$((required_pool_mib << 20))" '
      (.agent_capacity.aggregate_growth_pool_bytes > 0) and
      (.agent_capacity.aggregate_current_pool_bytes > .agent_capacity.aggregate_initial_pool_bytes) and
      ([.tunnel_sessions[] | select(.aggregate == true and .lane > 0) | .receive_limit_bytes] | length == 4 and min > $required)
    ' <<<"$agent_runtime" >/dev/null
  fi
  jq -c --arg stage "$stage" --arg direction "$direction" --arg mode "$mode" \
    --argjson repetition "$repetition" --argjson agent_runtime "$agent_runtime" \
    --argjson measured_rtt_ms "$measured_rtt_ms" \
    --arg server_binary_sha256 "$server_binary_sha256" \
    --arg aggregate_agent_binary_sha256 "$aggregate_agent_binary_sha256" \
    --arg legacy_agent_binary_sha256 "$legacy_agent_binary_sha256" \
    --arg script_sha256 "$script_sha256" \
    --arg server_source_ref "$server_source_ref" \
    --arg aggregate_agent_source_ref "$aggregate_agent_source_ref" \
    --arg legacy_agent_source_ref "$legacy_agent_source_ref" '
      ([.agent_tunnels_high_water[0].lanes[]? | select(.aggregate == true and .lane > 0) | .receive_limit_bytes]) as $server_growth_pools |
      ([.agent_tunnels_final[0].lanes[]? | select(.aggregate == true and .lane > 0) | .receive_limit_bytes]) as $server_final_pools |
      ([$agent_runtime.tunnel_sessions[]? | select(.aggregate == true and .lane > 0) | .receive_limit_bytes]) as $agent_pools |
      . + {
        measurement_stage:$stage,
        transfer_direction:$direction,
        transport_mode:$mode,
        repetition:$repetition,
        configured_rtt_ms:80,
        measured_rtt_ms:$measured_rtt_ms,
        shaped_rate_bits_per_second:1000000000,
        useful_transfer_bytes_per_second:(.configured_payload_bytes / .wall_seconds),
        receiving_pool_observed_bytes:(if $mode != "aggregate-v1" then null elif $stage == "steady" and $direction == "download" then ($server_final_pools | min) elif $stage == "steady" then ($agent_pools | min) elif $direction == "download" then ($server_growth_pools | max // 0) else ($agent_pools | max // 0) end),
        receiving_pool_evidence:(if $mode != "aggregate-v1" then "legacy-per-stream-window" elif $stage == "warmup" then "growth-high-water-max-unattributed" elif $stage == "retained-warmup" then "retained-stream-warmup-excluded-from-summary" elif $direction == "download" then "all-server-receive-lanes-prewarmed-and-quiescent" else "all-agent-receive-lanes-prewarmed-and-quiescent" end),
        agent_aggregate_growth_reservation_bytes:($agent_runtime.agent_capacity.aggregate_growth_pool_bytes // 0),
        agent_runtime_after:$agent_runtime,
        provenance:{
          server_binary_sha256:$server_binary_sha256,
          aggregate_agent_binary_sha256:$aggregate_agent_binary_sha256,
          legacy_agent_binary_sha256:$legacy_agent_binary_sha256,
          harness_sha256:$script_sha256,
          server_source_ref:$server_source_ref,
          aggregate_agent_source_ref:$aggregate_agent_source_ref,
          legacy_agent_source_ref:$legacy_agent_source_ref
        }
      }
    ' "$raw" >> "$output"
}

run_cohort() {
  local label=$1 mode=$2 agent_binary=$3 iteration=$4
  local server_ready=$run_dir/$label-$iteration-server.ready.json server_stop=$run_dir/$label-$iteration-server.stop
  local management_ca=$run_dir/$label-$iteration-management-ca.pem
  env \
    P2PSTREAM_QUALIFICATION_ROLE=proxy-external-agent \
    P2PSTREAM_QUALIFICATION_READY="$server_ready" \
    P2PSTREAM_QUALIFICATION_STOP="$server_stop" \
    P2PSTREAM_QUALIFICATION_ORIGIN_URL="$origin_url" \
    P2PSTREAM_QUALIFICATION_CERT="$cert" \
    P2PSTREAM_QUALIFICATION_KEY="$key" \
    P2PSTREAM_QUALIFICATION_MANAGEMENT_CA_FILE="$management_ca" \
    P2PSTREAM_QUALIFICATION_EXTERNAL_AGENTS=1 \
    P2PSTREAM_QUALIFICATION_BIND_ADDRESS=10.211.0.1 \
    GOMAXPROCS=4 \
    taskset -c "$server_cpu_set" "$server_binary" -test.run '^TestExternalProxyQualificationServerProcess$' -test.timeout "${overall_timeout_seconds}s" \
    >"$run_dir/$label-$iteration-server.log" 2>&1 &
  local server_pid=$!
  children+=("$server_pid")
  stops+=("$server_stop")
  wait_ready "$server_ready" "$server_pid"
  local public_url server_admin management_url public_id token
  public_url=$(jq -r .url "$server_ready")
  server_admin=$(jq -r .admin_url "$server_ready")
  management_url=$(jq -r .management_url "$server_ready")
  public_id=$(jq -r '.agents[0].public_id' "$server_ready")
  token=$(jq -r '.agents[0].token' "$server_ready")

  local agent_ready=$run_dir/$label-$iteration-agent.ready.json agent_stop=$run_dir/$label-$iteration-agent.stop
  nsenter -t "$agent_namespace_pid" -n env \
    P2PSTREAM_QUALIFICATION_ROLE=agent-external \
    P2PSTREAM_QUALIFICATION_READY="$agent_ready" \
    P2PSTREAM_QUALIFICATION_STOP="$agent_stop" \
    P2PSTREAM_QUALIFICATION_MANAGEMENT_URL="$management_url" \
    P2PSTREAM_QUALIFICATION_MANAGEMENT_CA_FILE="$management_ca" \
    P2PSTREAM_QUALIFICATION_AGENT_PUBLIC_ID="$public_id" \
    P2PSTREAM_QUALIFICATION_AGENT_TOKEN="$token" \
    P2PSTREAM_QUALIFICATION_AGENT_TUNNELS=4 \
    GOMAXPROCS=4 \
    taskset -c "$agent_cpu_set" "$agent_binary" -test.run '^TestExternalProxyQualificationAgentProcess$' -test.timeout "${overall_timeout_seconds}s" \
    >"$run_dir/$label-$iteration-agent.log" 2>&1 &
  local agent_pid=$!
  children+=("$agent_pid")
  stops+=("$agent_stop")
  wait_ready "$agent_ready" "$agent_pid"
  local agent_admin expected_bulk
  agent_admin=$(jq -r .admin_url "$agent_ready")
  expected_bulk=3
  [[ $mode == aggregate-v1 ]] && expected_bulk=4
  wait_mode "$server_admin" "$mode" "$expected_bulk"

  for direction in download upload; do
    for ((warmup_round=1; warmup_round<=warmup_lane_rounds; warmup_round++)); do
      run_transfer "$label" "$mode" "$server_pid" "$public_url" "$server_admin" "$agent_pid" "$agent_admin" warmup "$direction" "$((warmup_mib << 20))" "$warmup_round"
      close_idle_agent_transports "$server_admin"
    done
  done
  if [[ $mode == aggregate-v1 ]]; then
    wait_aggregate_lanes_grown_and_quiescent "$server_admin" "$agent_admin" "$((required_pool_mib << 20))"
  fi
  run_transfer "$label" "$mode" "$server_pid" "$public_url" "$server_admin" "$agent_pid" "$agent_admin" retained-warmup download "$((warmup_mib << 20))" 1
  run_transfer "$label" "$mode" "$server_pid" "$public_url" "$server_admin" "$agent_pid" "$agent_admin" retained-warmup upload "$((warmup_mib << 20))" 1
  for ((repetition=1; repetition<=transfer_repetitions; repetition++)); do
    if ((repetition % 2)); then directions=(download upload); else directions=(upload download); fi
    for direction in "${directions[@]}"; do
      run_transfer "$label" "$mode" "$server_pid" "$public_url" "$server_admin" "$agent_pid" "$agent_admin" steady "$direction" "$((steady_mib << 20))" "$repetition"
    done
  done

  stop_process "$agent_pid" "$agent_stop"
  stop_process "$server_pid" "$server_stop"
}

for ((iteration=1; iteration<=runs; iteration++)); do
  if ((iteration % 2)); then
    run_cohort current-server-legacy-agent-compat legacy "$legacy_agent_binary" "$iteration"
    run_cohort current-server-aggregate-v1 aggregate-v1 "$aggregate_agent_binary" "$iteration"
  else
    run_cohort current-server-aggregate-v1 aggregate-v1 "$aggregate_agent_binary" "$iteration"
    run_cohort current-server-legacy-agent-compat legacy "$legacy_agent_binary" "$iteration"
  fi
done
stop_process "$origin_pid" "$origin_stop"
summary=${output%.jsonl}.summary.json
python3 - "$output" "$summary" "$required_pool_mib" "$smoke_mode" <<'PY'
import json, statistics, sys

source, destination, required_pool_mib, smoke_mode = sys.argv[1], sys.argv[2], int(sys.argv[3]), int(sys.argv[4])
rows = [json.loads(line) for line in open(source, encoding="utf-8") if line.strip()]
steady = [row for row in rows if row["measurement_stage"] == "steady"]
summary = {"acceptance": {}, "cohorts": {}}
for mode in ("legacy", "aggregate-v1"):
    summary["cohorts"][mode] = {}
    for direction in ("download", "upload"):
        values = [row["useful_transfer_bytes_per_second"] for row in steady if row["transport_mode"] == mode and row["transfer_direction"] == direction]
        if not values:
            raise SystemExit(f"missing steady {mode} {direction} rows")
        summary["cohorts"][mode][direction] = {
            "samples": len(values),
            "median_useful_bytes_per_second": statistics.median(values),
            "min_useful_bytes_per_second": min(values),
            "max_useful_bytes_per_second": max(values),
        }
for direction in ("download", "upload"):
    candidate = summary["cohorts"]["aggregate-v1"][direction]["median_useful_bytes_per_second"]
    baseline = summary["cohorts"]["legacy"][direction]["median_useful_bytes_per_second"]
    ratio = candidate / baseline
    summary["acceptance"][f"{direction}_candidate_to_legacy_ratio"] = ratio
    pools = [row["receiving_pool_observed_bytes"] for row in steady if row["transport_mode"] == "aggregate-v1" and row["transfer_direction"] == direction and row["receiving_pool_observed_bytes"] is not None]
    summary["cohorts"]["aggregate-v1"][direction]["min_receiving_pool_observed_bytes"] = min(pools) if pools else None
    summary["cohorts"]["aggregate-v1"][direction]["max_receiving_pool_observed_bytes"] = max(pools) if pools else None
    summary["cohorts"]["aggregate-v1"][direction]["receiving_pool_growth_proven"] = bool(pools) and min(pools) > (required_pool_mib << 20)
    if not smoke_mode and ratio < .90:
        raise SystemExit(f"{direction} candidate/legacy median throughput ratio {ratio:.4f} is below 0.90")
summary["acceptance"]["required_receiving_pool_bytes"] = required_pool_mib << 20
with open(destination, "w", encoding="utf-8") as output_file:
    json.dump(summary, output_file, indent=2, sort_keys=True)
    output_file.write("\n")
PY
printf 'results=%s\nsummary=%s\nraw=%s\n' "$output" "$summary" "$run_dir"

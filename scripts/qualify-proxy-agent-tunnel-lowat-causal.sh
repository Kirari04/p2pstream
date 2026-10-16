#!/usr/bin/env bash
# Finite aggregate-v1 physical-tunnel TCP_NOTSENT_LOWAT causal diagnostic.
# Both arms use the same application and Yamux implementation. The candidate
# requests TCP_NOTSENT_LOWAT=32 KiB on both authenticated physical tunnel
# endpoints; the control observes the same option without setting it. The test
# requires exactly five reverse-correlated sockets per endpoint before load and
# the identical observation set after all sixteen transfers. There is no live
# option polling during the workload.
# The public client and server share one disposable namespace; the real agent
# and origin use a second, with tunnel traffic crossing an 80 ms / 1 Gbit/s path.
set -Eeuo pipefail

if [[ ${1:-} != --inside ]]; then
  cd "$(dirname "$0")/.."
  driver_binary=""
  control_server_binary=""
  lowat_server_binary=""
  lowat_agent_binary=""
  control_agent_binary=""
  output=""
  runs=3
  warmup_mib=256
  steady_mib=256
  transfer_repetitions=3
  required_pool_mib=8
  configured_rtt_ms=80
  base_source_ref=""
  lowat_source_ref=""
  control_source_ref=""
  common_scaffold_sha256=""
  lowat_32k_overlay_sha256=""
  smoke_mode=0
  pressure_mode=0
  tiny_response_mode=0
  while (($#)); do
    case "$1" in
      --driver-binary) driver_binary=$2; shift 2 ;;
      --control-server-binary) control_server_binary=$2; shift 2 ;;
      --lowat-server-binary) lowat_server_binary=$2; shift 2 ;;
      --lowat-agent-binary) lowat_agent_binary=$2; shift 2 ;;
      --control-agent-binary) control_agent_binary=$2; shift 2 ;;
      --output) output=$2; shift 2 ;;
      --runs) runs=$2; shift 2 ;;
      --warmup-mib) warmup_mib=$2; shift 2 ;;
      --steady-mib) steady_mib=$2; shift 2 ;;
      --transfer-repetitions) transfer_repetitions=$2; shift 2 ;;
      --required-pool-mib) required_pool_mib=$2; shift 2 ;;
      --rtt-ms) configured_rtt_ms=$2; shift 2 ;;
      --pressure)
        pressure_mode=1
        runs=1
        shift
        ;;
      --tiny-response)
        tiny_response_mode=1
        runs=3
        shift
        ;;
      --base-source-ref) base_source_ref=$2; shift 2 ;;
      --lowat-source-ref) lowat_source_ref=$2; shift 2 ;;
      --control-source-ref) control_source_ref=$2; shift 2 ;;
      --common-scaffold-sha256) common_scaffold_sha256=$2; shift 2 ;;
      --lowat-32k-overlay-sha256) lowat_32k_overlay_sha256=$2; shift 2 ;;
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
  [[ -x $driver_binary && -x $control_server_binary && -x $lowat_server_binary &&
      -x $lowat_agent_binary && -x $control_agent_binary ]] || {
    echo "executable driver, control/lowat server and control/lowat agent binaries are required" >&2
    exit 2
  }
  [[ $runs =~ ^[1-9][0-9]*$ ]] || { echo "--runs must be positive" >&2; exit 2; }
  for value in "$warmup_mib" "$steady_mib" "$transfer_repetitions" "$required_pool_mib"; do
    [[ $value =~ ^[0-9]+$ ]] || { echo "transfer sizes, repetitions and required growth must be non-negative integers" >&2; exit 2; }
  done
  [[ $configured_rtt_ms =~ ^[0-9]+$ ]] && ((configured_rtt_ms <= 1000)) || {
    echo "--rtt-ms must be an integer from 0 through 1000" >&2
    exit 2
  }
  ((configured_rtt_ms == 1 || configured_rtt_ms % 2 == 0)) || {
    echo "--rtt-ms must be 1 or an even integer so the two path halves match" >&2
    exit 2
  }
  ((warmup_mib > 0 && steady_mib > 0 && transfer_repetitions > 0)) || {
    echo "transfer sizes and repetitions must be positive" >&2
    exit 2
  }
  ((required_pool_mib > 2 || (smoke_mode == 1 && required_pool_mib == 2))) || {
    echo "--required-pool-mib must be greater than the 2 MiB initial pool (exactly 2 is allowed only with --smoke)" >&2
    exit 2
  }
  ((pressure_mode == 0 || tiny_response_mode == 0)) || {
    echo "--pressure and --tiny-response are mutually exclusive" >&2
    exit 2
  }
  ((smoke_mode == 0 || tiny_response_mode == 0)) || {
    echo "--smoke and --tiny-response are mutually exclusive" >&2
    exit 2
  }
  [[ -n $base_source_ref && -n $lowat_source_ref && -n $control_source_ref ]] || {
    echo "--base-source-ref, --lowat-source-ref and --control-source-ref are required" >&2
    exit 2
  }
  [[ $base_source_ref == "$control_source_ref" && $base_source_ref == "$lowat_source_ref" ]] || {
    echo "control and lowat-32k arms must share the exact same base source ref" >&2
    exit 2
  }
  [[ $common_scaffold_sha256 =~ ^[0-9a-f]{64}$ && $lowat_32k_overlay_sha256 =~ ^[0-9a-f]{64}$ ]] || {
    echo "--common-scaffold-sha256 and --lowat-32k-overlay-sha256 must be lowercase SHA-256 digests" >&2
    exit 2
  }
  ((runs == 3 && warmup_mib == 256 && steady_mib == 256 && transfer_repetitions == 3 && required_pool_mib == 8 && configured_rtt_ms == 80)) || {
    echo "tunnel-lowat causal diagnostic is fixed at 3 pairs, 256 MiB warm/steady, 3 timed transfers, 8 MiB pool and 80 ms RTT" >&2
    exit 2
  }
  ((smoke_mode == 0 && pressure_mode == 0 && tiny_response_mode == 0)) || {
    echo "tunnel-lowat causal diagnostic does not support smoke, pressure or tiny-response modes" >&2
    exit 2
  }
  mkdir -p tmp/proxy-agent-growth
  [[ -n $output ]] || output=tmp/proxy-agent-growth/results.jsonl
  export P2PSTREAM_GROWTH_DRIVER_BINARY P2PSTREAM_GROWTH_CONTROL_SERVER_BINARY P2PSTREAM_GROWTH_LOWAT_SERVER_BINARY
  export P2PSTREAM_GROWTH_LOWAT_AGENT_BINARY P2PSTREAM_GROWTH_CONTROL_AGENT_BINARY
  export P2PSTREAM_GROWTH_OUTPUT P2PSTREAM_GROWTH_RUNS P2PSTREAM_GROWTH_PARENT_NETNS
  export P2PSTREAM_GROWTH_WARMUP_MIB P2PSTREAM_GROWTH_STEADY_MIB P2PSTREAM_GROWTH_TRANSFER_REPETITIONS P2PSTREAM_GROWTH_REQUIRED_MIB
  export P2PSTREAM_GROWTH_BASE_SOURCE_REF P2PSTREAM_GROWTH_LOWAT_SOURCE_REF P2PSTREAM_GROWTH_CONTROL_SOURCE_REF
  export P2PSTREAM_GROWTH_COMMON_SCAFFOLD_SHA256 P2PSTREAM_GROWTH_LOWAT_32K_OVERLAY_SHA256
  export P2PSTREAM_GROWTH_SMOKE_MODE
  export P2PSTREAM_GROWTH_PRESSURE_MODE
  export P2PSTREAM_GROWTH_TINY_RESPONSE_MODE
  export P2PSTREAM_GROWTH_CONFIGURED_RTT_MS
  P2PSTREAM_GROWTH_DRIVER_BINARY=$(realpath "$driver_binary")
  P2PSTREAM_GROWTH_CONTROL_SERVER_BINARY=$(realpath "$control_server_binary")
  P2PSTREAM_GROWTH_LOWAT_SERVER_BINARY=$(realpath "$lowat_server_binary")
  P2PSTREAM_GROWTH_LOWAT_AGENT_BINARY=$(realpath "$lowat_agent_binary")
  P2PSTREAM_GROWTH_CONTROL_AGENT_BINARY=$(realpath "$control_agent_binary")
  P2PSTREAM_GROWTH_OUTPUT=$(realpath -m "$output")
  P2PSTREAM_GROWTH_RUNS=$runs
  P2PSTREAM_GROWTH_WARMUP_MIB=$warmup_mib
  P2PSTREAM_GROWTH_STEADY_MIB=$steady_mib
  P2PSTREAM_GROWTH_TRANSFER_REPETITIONS=$transfer_repetitions
  P2PSTREAM_GROWTH_REQUIRED_MIB=$required_pool_mib
  P2PSTREAM_GROWTH_BASE_SOURCE_REF=$base_source_ref
  P2PSTREAM_GROWTH_LOWAT_SOURCE_REF=$lowat_source_ref
  P2PSTREAM_GROWTH_CONTROL_SOURCE_REF=$control_source_ref
  P2PSTREAM_GROWTH_COMMON_SCAFFOLD_SHA256=$common_scaffold_sha256
  P2PSTREAM_GROWTH_LOWAT_32K_OVERLAY_SHA256=$lowat_32k_overlay_sha256
  P2PSTREAM_GROWTH_SMOKE_MODE=$smoke_mode
  P2PSTREAM_GROWTH_PRESSURE_MODE=$pressure_mode
  P2PSTREAM_GROWTH_TINY_RESPONSE_MODE=$tiny_response_mode
  P2PSTREAM_GROWTH_CONFIGURED_RTT_MS=$configured_rtt_ms
  P2PSTREAM_GROWTH_PARENT_NETNS=$(readlink /proc/self/ns/net)
  if unshare --user --map-root-user --net true 2>/dev/null; then
    exec unshare --user --map-root-user --net "$0" --inside
  fi
  exec sudo -n --preserve-env=P2PSTREAM_GROWTH_DRIVER_BINARY,P2PSTREAM_GROWTH_CONTROL_SERVER_BINARY,P2PSTREAM_GROWTH_LOWAT_SERVER_BINARY,P2PSTREAM_GROWTH_LOWAT_AGENT_BINARY,P2PSTREAM_GROWTH_CONTROL_AGENT_BINARY,P2PSTREAM_GROWTH_OUTPUT,P2PSTREAM_GROWTH_RUNS,P2PSTREAM_GROWTH_PARENT_NETNS,P2PSTREAM_GROWTH_WARMUP_MIB,P2PSTREAM_GROWTH_STEADY_MIB,P2PSTREAM_GROWTH_TRANSFER_REPETITIONS,P2PSTREAM_GROWTH_REQUIRED_MIB,P2PSTREAM_GROWTH_BASE_SOURCE_REF,P2PSTREAM_GROWTH_LOWAT_SOURCE_REF,P2PSTREAM_GROWTH_CONTROL_SOURCE_REF,P2PSTREAM_GROWTH_COMMON_SCAFFOLD_SHA256,P2PSTREAM_GROWTH_LOWAT_32K_OVERLAY_SHA256,P2PSTREAM_GROWTH_SMOKE_MODE,P2PSTREAM_GROWTH_PRESSURE_MODE,P2PSTREAM_GROWTH_TINY_RESPONSE_MODE,P2PSTREAM_GROWTH_CONFIGURED_RTT_MS \
    unshare --net "$0" --inside
fi

[[ -n ${P2PSTREAM_GROWTH_PARENT_NETNS:-} && $(readlink /proc/self/ns/net) != "$P2PSTREAM_GROWTH_PARENT_NETNS" ]] || {
  echo "refusing to configure networking outside a disposable namespace" >&2
  exit 1
}
for tool in ip tc nsenter jq python3 taskset openssl sha256sum ping; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 1; }
done
if [[ ${P2PSTREAM_GROWTH_PRESSURE_MODE:-0} == 1 ]]; then
  command -v systemd-run >/dev/null || { echo "systemd-run is required for --pressure" >&2; exit 1; }
fi

driver_binary=$P2PSTREAM_GROWTH_DRIVER_BINARY
control_server_binary=$P2PSTREAM_GROWTH_CONTROL_SERVER_BINARY
lowat_server_binary=$P2PSTREAM_GROWTH_LOWAT_SERVER_BINARY
lowat_agent_binary=$P2PSTREAM_GROWTH_LOWAT_AGENT_BINARY
control_agent_binary=$P2PSTREAM_GROWTH_CONTROL_AGENT_BINARY
output=$P2PSTREAM_GROWTH_OUTPUT
runs=$P2PSTREAM_GROWTH_RUNS
warmup_mib=$P2PSTREAM_GROWTH_WARMUP_MIB
steady_mib=$P2PSTREAM_GROWTH_STEADY_MIB
transfer_repetitions=$P2PSTREAM_GROWTH_TRANSFER_REPETITIONS
required_pool_mib=$P2PSTREAM_GROWTH_REQUIRED_MIB
base_source_ref=$P2PSTREAM_GROWTH_BASE_SOURCE_REF
lowat_source_ref=$P2PSTREAM_GROWTH_LOWAT_SOURCE_REF
control_source_ref=$P2PSTREAM_GROWTH_CONTROL_SOURCE_REF
common_scaffold_sha256=$P2PSTREAM_GROWTH_COMMON_SCAFFOLD_SHA256
lowat_32k_overlay_sha256=$P2PSTREAM_GROWTH_LOWAT_32K_OVERLAY_SHA256
smoke_mode=$P2PSTREAM_GROWTH_SMOKE_MODE
pressure_mode=${P2PSTREAM_GROWTH_PRESSURE_MODE:-0}
tiny_response_mode=${P2PSTREAM_GROWTH_TINY_RESPONSE_MODE:-0}
configured_rtt_ms=${P2PSTREAM_GROWTH_CONFIGURED_RTT_MS:-80}
largest_transfer_mib=$((warmup_mib > steady_mib ? warmup_mib : steady_mib))
transfer_timeout_seconds=$((60 + (largest_transfer_mib + 1) / 2))
warmup_lane_rounds=4
transfer_mib_per_cohort=$((2 * (warmup_lane_rounds + 1) * warmup_mib + 2 * transfer_repetitions * steady_mib))
overall_timeout_seconds=$((300 + runs * 2 * (transfer_mib_per_cohort + 1) / 2 + pressure_mode * 600))
if ((tiny_response_mode)); then overall_timeout_seconds=1200; fi
mkdir -p "$(dirname "$output")"
run_dir=$(mktemp -d -p "$(dirname "$output")" proxy-agent-growth.XXXXXX)
: > "$output"
tunnel_socket_output="${output%.jsonl}.tunnel-lowat-observations.jsonl"
: > "$tunnel_socket_output"

declare -A owned_groups=()
stops=()
cleanup() {
  for stop in "${stops[@]:-}"; do [[ -n $stop ]] && touch "$stop"; done
  local pid attempt
  for pid in "${!owned_groups[@]}"; do kill -TERM -- "-$pid" 2>/dev/null || true; done
  for ((attempt=0; attempt<50; attempt++)); do
    local live=0
    for pid in "${!owned_groups[@]}"; do kill -0 -- "-$pid" 2>/dev/null && live=1; done
    ((live)) || break
    sleep .1
  done
  for pid in "${!owned_groups[@]}"; do
    if kill -0 -- "-$pid" 2>/dev/null; then kill -KILL -- "-$pid" 2>/dev/null || true; fi
    wait "$pid" 2>/dev/null || true
  done
}
on_interrupt() { exit 130; }
on_terminate() { exit 143; }
trap cleanup EXIT
trap on_interrupt INT
trap on_terminate TERM

setsid unshare --net sleep "$overall_timeout_seconds" &
agent_namespace_pid=$!
owned_groups[$agent_namespace_pid]=1
setsid unshare --net sleep "$overall_timeout_seconds" &
router_namespace_pid=$!
owned_groups[$router_namespace_pid]=1
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
if ((configured_rtt_ms == 1)); then one_way_delay=500us; else one_way_delay=$((configured_rtt_ms / 2))ms; fi
for iface in growth-rs growth-rr; do
  router_ns tc qdisc add dev "$iface" root netem delay "$one_way_delay" rate 1gbit limit 100000
done

measured_rtt_ms=$(agent_ns ping -c 5 -q 10.211.0.1 | awk -F/ '/rtt|round-trip/ {print $5}')
python3 - "$configured_rtt_ms" "$measured_rtt_ms" <<'PY'
import sys
configured, measured = float(sys.argv[1]), float(sys.argv[2])
if configured <= 1:
    valid = 0 <= measured <= 5
else:
    valid = configured * .75 <= measured <= configured * 1.5
if not valid:
    raise SystemExit(f"measured RTT {measured}ms is outside the expected envelope for configured {configured}ms")
PY

driver_binary_sha256=$(sha256sum "$driver_binary" | awk '{print $1}')
control_server_binary_sha256=$(sha256sum "$control_server_binary" | awk '{print $1}')
lowat_server_binary_sha256=$(sha256sum "$lowat_server_binary" | awk '{print $1}')
lowat_agent_binary_sha256=$(sha256sum "$lowat_agent_binary" | awk '{print $1}')
control_agent_binary_sha256=$(sha256sum "$control_agent_binary" | awk '{print $1}')
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
    kill -0 -- "-$pid" 2>/dev/null || break
    sleep .05
  done
  if kill -0 -- "-$pid" 2>/dev/null; then
    kill -TERM -- "-$pid" 2>/dev/null || true
    for ((attempt=0; attempt<50; attempt++)); do
      kill -0 -- "-$pid" 2>/dev/null || break
      sleep .1
    done
    if kill -0 -- "-$pid" 2>/dev/null; then
      kill -KILL -- "-$pid" 2>/dev/null || true
    fi
    wait "$pid" 2>/dev/null || true
    unset 'owned_groups[$pid]'
    echo "owned process group $pid did not stop within its graceful shutdown deadline" >&2
    return 1
  fi
  local status
  set +e
  wait "$pid" 2>/dev/null
  status=$?
  set -e
  unset 'owned_groups[$pid]'
  ((status == 0)) || {
    echo "process $pid exited with status $status" >&2
    return 1
  }
}
query_agent_json() {
  local pid=$1 url=$2
  nsenter -t "$pid" -n python3 - "$url" <<'PY'
import json, sys, urllib.request
with urllib.request.urlopen(sys.argv[1], timeout=2) as response:
    json.dump(json.load(response), sys.stdout, separators=(",", ":"))
PY
}

query_server_json() {
  local url=$1
  python3 - "$url" <<'PY'
import json, sys, urllib.request
with urllib.request.urlopen(sys.argv[1], timeout=5) as response:
    json.dump(json.load(response), sys.stdout, separators=(",", ":"))
PY
}

query_agent_tunnel_socket_json() {
  local pid=$1 url=$2
  nsenter -t "$pid" -n python3 - "$url" <<'PY'
import json, sys, urllib.parse, urllib.request
url = sys.argv[1] + ("&" if "?" in sys.argv[1] else "?") + urllib.parse.urlencode({"tunnel_socket_snapshot": "1"})
with urllib.request.urlopen(url, timeout=5) as response:
    json.dump(json.load(response), sys.stdout, separators=(",", ":"))
PY
}

query_server_tunnel_socket_json() {
  local url=$1
  python3 - "$url" <<'PY'
import json, sys, urllib.parse, urllib.request
url = sys.argv[1] + ("&" if "?" in sys.argv[1] else "?") + urllib.parse.urlencode({"tunnel_socket_snapshot": "1"})
with urllib.request.urlopen(url, timeout=5) as response:
    json.dump(json.load(response), sys.stdout, separators=(",", ":"))
PY
}

validate_tunnel_socket_snapshot_pair() {
  local label=$1 stage=$2 server_json=$3 agent_json=$4
  python3 - "$label" "$stage" "$server_json" "$agent_json" <<'PY'
import ipaddress
import json
import sys

label, stage = sys.argv[1:3]
server, agent = map(json.loads, sys.argv[3:5])
candidate = label == "tunnel-lowat-32k"
if label not in {"tunnel-lowat-default-control", "tunnel-lowat-32k"}:
    raise SystemExit(f"unknown LOWAT arm {label!r}")

def endpoint(value):
    if not isinstance(value, str) or not value:
        raise ValueError(value)
    if value.startswith("["):
        host, sep, port = value[1:].partition("]:")
        if not sep:
            raise ValueError(value)
    else:
        host, port = value.rsplit(":", 1)
    parsed_port = int(port)
    if parsed_port < 1 or parsed_port > 65535:
        raise ValueError(value)
    return str(ipaddress.ip_address(host)), parsed_port

error_fields = (
    "namespace_notsent_lowat_error", "notsent_lowat_before_error",
    "notsent_lowat_after_error", "send_buffer_before_error",
    "send_buffer_after_error", "receive_buffer_before_error",
    "receive_buffer_after_error", "setter_error", "unwrap_error",
    "syscall_conn_error", "control_error",
)
lowat_fields = (
    "requested_notsent_lowat", "namespace_notsent_lowat",
    "notsent_lowat_before", "notsent_lowat_after",
)
buffer_fields = (
    "send_buffer_before", "send_buffer_after",
    "receive_buffer_before", "receive_buffer_after",
)
boolean_fields = (
    "platform_supported", "apply_requested", "setter_attempted", "conclusive",
)

def exact_int(mapping, name, minimum, maximum):
    if name not in mapping or type(mapping[name]) is not int or not minimum <= mapping[name] <= maximum:
        raise SystemExit(f"{stage} socket field {name} is missing or invalid")
    return mapping[name]

def exact_bool(mapping, name):
    if name not in mapping or type(mapping[name]) is not bool:
        raise SystemExit(f"{stage} socket field {name} is missing or invalid")
    return mapping[name]

def empty_string(mapping, name):
    if name not in mapping or not isinstance(mapping[name], str) or mapping[name] != "":
        raise SystemExit(f"{stage} socket field {name} is missing, mistyped, or nonempty")


def validate_common(response, role):
    if not isinstance(response, dict):
        raise SystemExit(f"{stage} {role} snapshot is not an object")
    if exact_int(response, "schema_version", 1, 1) != 1 or response.get("process_role") != role:
        raise SystemExit(f"{stage} {role} snapshot schema/role mismatch")
    if "process_role" not in response or not isinstance(response["process_role"], str):
        raise SystemExit(f"{stage} {role} snapshot process role is missing or mistyped")
    exact_int(response, "captured_unix_nano", 1, (1 << 63) - 1)
    if exact_int(response, "capacity", 0, (1 << 31) - 1) != 64 or exact_int(response, "dropped_observations", 0, (1 << 64) - 1) != 0:
        raise SystemExit(f"{stage} {role} snapshot capacity/drop mismatch")
    if "observations" not in response:
        raise SystemExit(f"{stage} {role} snapshot lacks observations")
    rows = response["observations"]
    if not isinstance(rows, list) or len(rows) != 5:
        raise SystemExit(f"{stage} {role} has {len(rows) if isinstance(rows, list) else 'invalid'} observations, want exactly 5")
    for expected_index, row in enumerate(rows):
        if not isinstance(row, dict) or exact_int(row, "index", 0, 63) != expected_index:
            raise SystemExit(f"{stage} {role} observation indexes are not exactly 0..4")
        if "socket" not in row or not isinstance(row["socket"], dict):
            raise SystemExit(f"{stage} {role} observation socket is missing or mistyped")
        sock = row["socket"]
        for name in boolean_fields:
            exact_bool(sock, name)
        if not sock["conclusive"] or not sock["platform_supported"]:
            raise SystemExit(f"{stage} {role} contains a nonconclusive socket observation")
        for name in (*error_fields, "inconclusive_reason"):
            empty_string(sock, name)
        for name in lowat_fields:
            exact_int(sock, name, 0, (1 << 32) - 1)
        if sock["requested_notsent_lowat"] != 32768:
            raise SystemExit(f"{stage} {role} requested LOWAT is not exactly 32768")
        for name in buffer_fields:
            exact_int(sock, name, 1, (1 << 63) - 1)
        if "local_address" not in sock or "remote_address" not in sock:
            raise SystemExit(f"{stage} {role} contains a missing socket tuple")
        endpoint(sock["local_address"]); endpoint(sock["remote_address"])
        if candidate:
            if not sock["apply_requested"] or not sock["setter_attempted"]:
                raise SystemExit(f"{stage} {role} candidate did not request/attempt LOWAT")
            if sock["requested_notsent_lowat"] != 32768 or sock["notsent_lowat_after"] != 32768:
                raise SystemExit(f"{stage} {role} candidate LOWAT is not exactly 32768")
        else:
            if sock["apply_requested"] or sock["setter_attempted"]:
                raise SystemExit(f"{stage} {role} control attempted LOWAT mutation")
            if sock["notsent_lowat_after"] != sock["notsent_lowat_before"]:
                raise SystemExit(f"{stage} {role} control LOWAT changed")
    return rows

server_rows = validate_common(server, "server")
agent_rows = validate_common(agent, "agent")
for row in server_rows:
    exact_int(row, "lane", 0, 4)
    if "lane_role" not in row or not isinstance(row["lane_role"], str):
        raise SystemExit(f"{stage} server lane role is missing or mistyped")
    exact_bool(row, "aggregate")
if sorted(row["lane"] for row in server_rows) != list(range(5)):
    raise SystemExit(f"{stage} server lanes are not exactly 0..4")
for row in server_rows:
    lane = row["lane"]
    expected_role = "control" if lane == 0 else "bulk"
    if row.get("lane_role") != expected_role or row.get("aggregate") is not True:
        raise SystemExit(f"{stage} server lane {lane} role/mode mismatch")

agent_by_tuple = {}
for row in agent_rows:
    sock = row["socket"]
    key = (endpoint(sock["local_address"]), endpoint(sock["remote_address"]))
    if key in agent_by_tuple:
        raise SystemExit(f"{stage} duplicate agent socket tuple")
    agent_by_tuple[key] = row
matched = []
for row in sorted(server_rows, key=lambda item: item["lane"]):
    sock = row["socket"]
    reverse = (endpoint(sock["remote_address"]), endpoint(sock["local_address"]))
    peer = agent_by_tuple.pop(reverse, None)
    if peer is None:
        raise SystemExit(f"{stage} server lane {row['lane']} lacks a reverse agent tuple")
    matched.append({"lane": row["lane"], "server_index": row["index"], "agent_index": peer["index"]})
if agent_by_tuple:
    raise SystemExit(f"{stage} contains unmatched agent socket tuples")
print(json.dumps(matched, separators=(",", ":")))
PY
}

capture_tunnel_socket_snapshot_pair() {
  local label=$1 iteration=$2 stage=$3 server_admin=$4 agent_pid=$5 agent_admin=$6
  local server_json agent_json matches validation_error validation_status
  server_json=$(query_server_tunnel_socket_json "$server_admin")
  agent_json=$(query_agent_tunnel_socket_json "$agent_pid" "$agent_admin")
  validation_error=$run_dir/$label-$iteration-$stage-tunnel-socket-validation.err
  set +e
  matches=$(validate_tunnel_socket_snapshot_pair "$label" "$stage" "$server_json" "$agent_json" 2>"$validation_error")
  validation_status=$?
  set -e
  [[ -n $matches ]] || matches='[]'
  jq -cn --arg label "$label" --argjson iteration "$iteration" --arg stage "$stage" \
    --argjson server "$server_json" --argjson agent "$agent_json" --argjson matches "$matches" \
    --argjson validation_status "$validation_status" --rawfile validation_error "$validation_error" \
    '{label:$label,iteration:$iteration,stage:$stage,server:$server,agent:$agent,
      reverse_tuple_matches:$matches,validation_ok:($validation_status == 0),validation_error:$validation_error}' \
    >> "$tunnel_socket_output"
  ((validation_status == 0)) || return "$validation_status"
}

cgroup_snapshot() {
  local pid=$1
  python3 - "$pid" <<'PY'
import json, pathlib, sys

pid = int(sys.argv[1])
unified = None
for line in pathlib.Path(f"/proc/{pid}/cgroup").read_text().splitlines():
    hierarchy, _, path = line.partition(":")
    _, _, path = path.partition(":")
    if hierarchy == "0":
        unified = path
        break
if not unified:
    raise SystemExit(f"unified cgroup unavailable for pid {pid}")
root = pathlib.Path(f"/proc/{pid}/root/sys/fs/cgroup") / unified.lstrip("/")
def integer(name):
    value = (root / name).read_text().strip()
    if value == "max":
        raise SystemExit(f"{name} is unbounded for pid {pid}")
    return int(value)
def pairs(name):
    result = {}
    for line in (root / name).read_text().splitlines():
        key, value = line.split()
        result[key] = int(value)
    return result
events = pairs("memory.events")
stat = pairs("memory.stat")
for required in ("oom", "oom_kill"):
    if required not in events:
        raise SystemExit(f"memory.events lacks {required} for pid {pid}")
status = pathlib.Path(f"/proc/{pid}/status").read_text().splitlines()
rss = next(int(line.split()[1]) * 1024 for line in status if line.startswith("VmRSS:"))
print(json.dumps({
    "pid": pid, "cgroup_path": unified, "memory_current_bytes": integer("memory.current"),
    "memory_max_bytes": integer("memory.max"), "memory_swap_max_bytes": integer("memory.swap.max"),
    "memory_events": events, "memory_stat_sock_bytes": stat.get("sock", 0),
    "process_rss_bytes": rss, "process_fd_count": len(list(pathlib.Path(f"/proc/{pid}/fd").iterdir())),
}, separators=(",", ":")))
PY
}

set_agent_ballast() {
  local pid=$1 admin_url=$2 ballast_mib=$3
  nsenter -t "$pid" -n python3 - "$admin_url" "$ballast_mib" <<'PY'
import json, sys, urllib.parse, urllib.request
url = sys.argv[1] + ("&" if "?" in sys.argv[1] else "?") + urllib.parse.urlencode({"ballast_mib": sys.argv[2]})
with urllib.request.urlopen(url, timeout=30) as response:
    value = json.load(response)
if value.get("agent_ballast_bytes", 0) != int(sys.argv[2]) << 20:
    raise SystemExit(f"ballast setter returned unexpected size: {value.get('agent_ballast_bytes')}")
json.dump(value, sys.stdout, separators=(",", ":"))
PY
}

agent_health_roundtrip() {
  local admin_url=$1
  python3 - "$admin_url" <<'PY'
import json, sys, urllib.error, urllib.parse, urllib.request
url = sys.argv[1] + ("&" if "?" in sys.argv[1] else "?") + urllib.parse.urlencode({"agent_health": "1"})
try:
    with urllib.request.urlopen(url, timeout=5) as response:
        body = json.load(response)
        status = response.status
except urllib.error.HTTPError as error:
    status = error.code
    body = json.load(error)
body["http_status"] = status
json.dump(body, sys.stdout, separators=(",", ":"))
PY
}

append_pressure_sample() {
  local stage=$1 ballast_mib=$2 server_pid=$3 server_admin=$4 agent_pid=$5 agent_admin=$6
  local server_runtime agent_runtime server_cgroup agent_cgroup
  server_runtime=$(query_server_json "$server_admin")
  agent_runtime=$(query_agent_json "$agent_pid" "$agent_admin")
  server_cgroup=$(cgroup_snapshot "$server_pid")
  agent_cgroup=$(cgroup_snapshot "$agent_pid")
  jq -cn --arg stage "$stage" --argjson ballast_mib "$ballast_mib" \
    --argjson server_runtime "$server_runtime" --argjson agent_runtime "$agent_runtime" \
    --argjson server_cgroup "$server_cgroup" --argjson agent_cgroup "$agent_cgroup" --arg base_source_ref "$base_source_ref" \
    --arg lowat_source_ref "$lowat_source_ref" \
    --arg driver_binary_sha256 "$driver_binary_sha256" \
    --arg lowat_agent_binary_sha256 "$lowat_agent_binary_sha256" \
    --arg script_sha256 "$script_sha256" '
      {measurement_stage:$stage,transport_mode:"aggregate-v1",configured_agent_ballast_mib:$ballast_mib,
       server_runtime:$server_runtime,agent_runtime:$agent_runtime,server_cgroup:$server_cgroup,agent_cgroup:$agent_cgroup,
       provenance:{base_source_ref:$base_source_ref,lowat_source_ref:$lowat_source_ref,
         driver_binary_sha256:$driver_binary_sha256,lowat_agent_binary_sha256:$lowat_agent_binary_sha256,
         harness_sha256:$script_sha256}}
    ' >> "$output"
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
setsid nsenter -t "$agent_namespace_pid" -n env \
  P2PSTREAM_QUALIFICATION_ROLE=origin \
  P2PSTREAM_QUALIFICATION_READY="$origin_ready" \
  P2PSTREAM_QUALIFICATION_STOP="$origin_stop" \
  P2PSTREAM_QUALIFICATION_BIND_ADDRESS=10.212.0.2 \
  taskset -c "$agent_cpu_set" "$driver_binary" -test.run '^TestProxyQualificationProcess$' -test.timeout "${overall_timeout_seconds}s" \
  >"$run_dir/origin.log" 2>&1 &
origin_pid=$!
owned_groups[$origin_pid]=1
stops+=("$origin_stop")
wait_ready "$origin_ready" "$origin_pid"
origin_url=$(jq -r .url "$origin_ready")
origin_admin=$(jq -r .admin_url "$origin_ready")

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
  P2PSTREAM_QUALIFICATION_RESOURCE_ENVELOPE="p2pstream-external-agent-tunnel-lowat-causal:${configured_rtt_ms}ms,1gbit,variant=$label,mode=$mode" \
  P2PSTREAM_QUALIFICATION_SEQUENCE="$sequence" \
  P2PSTREAM_QUALIFICATION_RESULT="$raw" \
    taskset -c "$load_cpu_set" "$driver_binary" -test.run '^TestProxyQualificationLoad$' -test.timeout "${transfer_timeout_seconds}s" \
    >"$raw.log" 2>&1
  local agent_runtime
  agent_runtime=$(query_agent_json "$agent_namespace_pid" "$agent_admin")
  jq -e --arg mode "$mode" --arg direction "$direction" --argjson bytes "$payload_bytes" '
    (.attempted == 1 and .useful_completed == 1 and .errors == 0 and .status_classes["2xx"] == 1) and
    (.status_codes == {"200": 1} and .observed_http_major == 2 and .observed_origin_http_major == 1) and
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
    --argjson configured_rtt_ms "$configured_rtt_ms" --argjson measured_rtt_ms "$measured_rtt_ms" \
    --arg driver_binary_sha256 "$driver_binary_sha256" \
    --arg control_server_binary_sha256 "$control_server_binary_sha256" \
    --arg lowat_server_binary_sha256 "$lowat_server_binary_sha256" \
    --arg endpoint_server_binary_sha256 "$endpoint_server_binary_sha256" \
    --arg endpoint_agent_binary_sha256 "$endpoint_agent_binary_sha256" \
    --arg lowat_agent_binary_sha256 "$lowat_agent_binary_sha256" \
    --arg control_agent_binary_sha256 "$control_agent_binary_sha256" \
    --arg script_sha256 "$script_sha256" \
    --arg base_source_ref "$base_source_ref" \
    --arg lowat_source_ref "$lowat_source_ref" \
    --arg control_source_ref "$control_source_ref" \
    --arg common_scaffold_sha256 "$common_scaffold_sha256" \
    --arg lowat_32k_overlay_sha256 "$lowat_32k_overlay_sha256" '
      ([.agent_tunnels_high_water[0].lanes[]? | select(.aggregate == true and .lane > 0) | .receive_limit_bytes]) as $server_growth_pools |
      ([.agent_tunnels_final[0].lanes[]? | select(.aggregate == true and .lane > 0) | .receive_limit_bytes]) as $server_final_pools |
      ([$agent_runtime.tunnel_sessions[]? | select(.aggregate == true and .lane > 0) | .receive_limit_bytes]) as $agent_pools |
      . + {
        measurement_stage:$stage,
        transfer_direction:$direction,
        transport_mode:$mode,
        repetition:$repetition,
        configured_rtt_ms:$configured_rtt_ms,
        measured_rtt_ms:$measured_rtt_ms,
        shaped_rate_bits_per_second:1000000000,
        useful_transfer_bytes_per_second:(.configured_payload_bytes / .wall_seconds),
        receiving_pool_observed_bytes:(if $mode != "aggregate-v1" then null elif $stage == "steady" and $direction == "download" then ($server_final_pools | min) elif $stage == "steady" then ($agent_pools | min) elif $direction == "download" then ($server_growth_pools | max // 0) else ($agent_pools | max // 0) end),
        receiving_pool_evidence:(if $mode != "aggregate-v1" then "legacy-per-stream-window" elif $stage == "warmup" then "growth-high-water-max-unattributed" elif $stage == "retained-warmup" then "retained-stream-warmup-excluded-from-summary" elif $direction == "download" then "all-server-receive-lanes-prewarmed-and-quiescent" else "all-agent-receive-lanes-prewarmed-and-quiescent" end),
        agent_aggregate_growth_reservation_bytes:($agent_runtime.agent_capacity.aggregate_growth_pool_bytes // 0),
        agent_runtime_after:$agent_runtime,
        provenance:{
          driver_binary_sha256:$driver_binary_sha256,
          control_server_binary_sha256:$control_server_binary_sha256,
          lowat_server_binary_sha256:$lowat_server_binary_sha256,
          endpoint_server_binary_sha256:$endpoint_server_binary_sha256,
          endpoint_agent_binary_sha256:$endpoint_agent_binary_sha256,
          lowat_agent_binary_sha256:$lowat_agent_binary_sha256,
          control_agent_binary_sha256:$control_agent_binary_sha256,
          harness_sha256:$script_sha256,
          base_source_ref:$base_source_ref,
          lowat_source_ref:$lowat_source_ref,
          control_source_ref:$control_source_ref,
          common_scaffold_sha256:$common_scaffold_sha256,
          lowat_32k_overlay_sha256:$lowat_32k_overlay_sha256
        }
      }
    ' "$raw" >> "$output"
}

run_tiny_response() {
  local label=$1 mode=$2 server_pid=$3 public_url=$4 server_admin=$5 agent_pid=$6 agent_admin=$7
  local protocol=$8 concurrency=$9 repetition=${10}
  sequence=$((sequence + 1))
  local scenario="${protocol}-keepalive"
  local raw="$run_dir/$sequence-$label-tiny-$protocol-c$concurrency-r$repetition.json"
  local server_spec agent_specs expected_major agent_runtime
  server_spec=$(jq -cn --argjson pid "$server_pid" --arg admin "$server_admin" --arg cpus "$server_cpu_set" \
    '{role:"server",pid:$pid,admin_url:$admin,cpu_set:$cpus}')
  agent_specs=$(jq -cn --argjson pid "$agent_pid" --arg admin "$agent_admin" --arg cpus "$agent_cpu_set" \
    '[{role:"agent",pid:$pid,admin_url:$admin,cpu_set:$cpus}]')
  expected_major=1
  [[ $protocol == h2 ]] && expected_major=2
  P2PSTREAM_QUALIFICATION_ROLE=load \
  P2PSTREAM_QUALIFICATION_URL="$public_url/agent/" \
  P2PSTREAM_QUALIFICATION_SCENARIO="$scenario" \
  P2PSTREAM_QUALIFICATION_PROTOCOL="$protocol" \
  P2PSTREAM_QUALIFICATION_CONCURRENCY="$concurrency" \
  P2PSTREAM_QUALIFICATION_REQUESTS="$concurrency" \
  P2PSTREAM_QUALIFICATION_OFFERED_RPS=0 \
  P2PSTREAM_QUALIFICATION_DURATION_SECONDS=10 \
  P2PSTREAM_QUALIFICATION_WARMUP_SECONDS=2 \
  P2PSTREAM_QUALIFICATION_LABEL="$label" \
  P2PSTREAM_QUALIFICATION_PATH=agent-external-tiny \
  P2PSTREAM_QUALIFICATION_PIDS="$server_pid" \
  P2PSTREAM_QUALIFICATION_ADMIN_URL="$server_admin" \
  P2PSTREAM_QUALIFICATION_PROXY_CPU_SET="$server_cpu_set" \
  P2PSTREAM_QUALIFICATION_SERVER_PROCESS="$server_spec" \
  P2PSTREAM_QUALIFICATION_AGENT_PROCESSES="$agent_specs" \
  P2PSTREAM_QUALIFICATION_LOAD_CPU_SET="$load_cpu_set" \
  P2PSTREAM_QUALIFICATION_RESOURCE_ENVELOPE="p2pstream-external-agent-tiny:${configured_rtt_ms}ms,1gbit,current-server,mode=$mode" \
  P2PSTREAM_QUALIFICATION_SEQUENCE="$sequence" \
  P2PSTREAM_QUALIFICATION_RESULT="$raw" \
    taskset -c "$load_cpu_set" "$driver_binary" -test.run '^TestProxyQualificationLoad$' -test.timeout 60s \
    >"$raw.log" 2>&1
  agent_runtime=$(query_agent_json "$agent_pid" "$agent_admin")
  jq -e --arg mode "$mode" --argjson major "$expected_major" '
    .attempted > 0 and .useful_completed == .attempted and .errors == 0 and
    .status_classes["2xx"] == .attempted and .response_bytes == (.attempted * 256) and
    .observed_http_major == $major and
    .event_stats_captured == true and .persisted_event_delta == .attempted and
    ((.dropped_event_delta // 0) == 0) and ((.event_queue_after_drain // 0) == 0) and
    ((.event_drain_error // "") == "") and
    (.agent_tunnels_final | length == 1 and .[0].flow_control == $mode)
  ' "$raw" >/dev/null
  jq -c --arg stage tiny-response --arg mode "$mode" --arg protocol "$protocol" \
    --argjson repetition "$repetition" --argjson agent_runtime "$agent_runtime" \
    --argjson configured_rtt_ms "$configured_rtt_ms" --argjson measured_rtt_ms "$measured_rtt_ms" \
    --arg driver_binary_sha256 "$driver_binary_sha256" \
    --arg lowat_agent_binary_sha256 "$lowat_agent_binary_sha256" \
    --arg control_agent_binary_sha256 "$control_agent_binary_sha256" \
    --arg script_sha256 "$script_sha256" --arg base_source_ref "$base_source_ref" \
    --arg lowat_source_ref "$lowat_source_ref" --arg control_source_ref "$control_source_ref" '
      . + {measurement_stage:$stage,transport_mode:$mode,measured_protocol:$protocol,repetition:$repetition,
        configured_rtt_ms:$configured_rtt_ms,measured_rtt_ms:$measured_rtt_ms,
        agent_runtime_after:$agent_runtime,
        provenance:{base_source_ref:$base_source_ref,lowat_source_ref:$lowat_source_ref,
          control_source_ref:$control_source_ref,driver_binary_sha256:$driver_binary_sha256,
          lowat_agent_binary_sha256:$lowat_agent_binary_sha256,
          control_agent_binary_sha256:$control_agent_binary_sha256,harness_sha256:$script_sha256}}
    ' "$raw" >> "$output"
}

run_pressure_rejection() {
  local label=$1 server_pid=$2 public_url=$3 server_admin=$4 agent_pid=$5 agent_admin=$6 before_rejected=$7
  sequence=$((sequence + 1))
  local raw="$run_dir/$sequence-$label-pressure-rejection.json"
  local server_spec
  server_spec=$(jq -cn --argjson pid "$server_pid" --arg admin "$server_admin" --arg cpus "$server_cpu_set" '{role:"server",pid:$pid,admin_url:$admin,cpu_set:$cpus}')
  P2PSTREAM_QUALIFICATION_ROLE=load \
  P2PSTREAM_QUALIFICATION_URL="$public_url/agent/" \
  P2PSTREAM_QUALIFICATION_SCENARIO=h2-keepalive \
  P2PSTREAM_QUALIFICATION_PROTOCOL=h2 \
  P2PSTREAM_QUALIFICATION_CONCURRENCY=1 \
  P2PSTREAM_QUALIFICATION_REQUESTS=1 \
  P2PSTREAM_QUALIFICATION_OFFERED_RPS=0 \
  P2PSTREAM_QUALIFICATION_DURATION_SECONDS=0 \
  P2PSTREAM_QUALIFICATION_WARMUP_SECONDS=0 \
  P2PSTREAM_QUALIFICATION_LABEL="$label" \
  P2PSTREAM_QUALIFICATION_PATH=agent-external-pressure \
  P2PSTREAM_QUALIFICATION_PIDS="$server_pid" \
  P2PSTREAM_QUALIFICATION_ADMIN_URL="$server_admin" \
  P2PSTREAM_QUALIFICATION_PROXY_CPU_SET="$server_cpu_set" \
  P2PSTREAM_QUALIFICATION_SERVER_PROCESS="$server_spec" \
  P2PSTREAM_QUALIFICATION_LOAD_CPU_SET="$load_cpu_set" \
  P2PSTREAM_QUALIFICATION_RESOURCE_ENVELOPE="p2pstream-external-agent-pressure:server4GiB,agent4GiB,no-swap" \
  P2PSTREAM_QUALIFICATION_SEQUENCE="$sequence" \
  P2PSTREAM_QUALIFICATION_RESULT="$raw" \
    setsid taskset -c "$load_cpu_set" "$driver_binary" -test.run '^TestProxyQualificationLoad$' -test.timeout 45s \
    >"$raw.log" 2>&1
  local agent_runtime after_rejected
  agent_runtime=$(query_agent_json "$agent_pid" "$agent_admin")
  after_rejected=$(jq -r '.agent_capacity.rejected_pressure' <<<"$agent_runtime")
  jq -e --argjson before "$before_rejected" --argjson after "$after_rejected" '
    (.attempted == 1 and .useful_completed == 0 and ((.status_classes["2xx"] // 0) == 0)) and
    (.event_stats_captured == true and .persisted_event_delta == 1 and ((.dropped_event_delta // 0) == 0) and
      ((.event_queue_after_drain // 0) == 0) and ((.event_drain_error // "") == "")) and
    ($after > $before)
  ' "$raw" >/dev/null
  jq -c --argjson agent_runtime "$agent_runtime" --argjson before "$before_rejected" --argjson after "$after_rejected" \
    '. + {measurement_stage:"pressure-rejection",transport_mode:"aggregate-v1",agent_runtime_after:$agent_runtime,
      rejected_pressure_before:$before,rejected_pressure_after:$after}' "$raw" >> "$output"
}

run_canceled_upload_trace() {
  local label=$1 server_pid=$2 public_url=$3 server_admin=$4 agent_pid=$5 agent_admin=$6 payload_bytes=$7
  sequence=$((sequence + 1))
  local raw="$run_dir/$sequence-$label-canceled-upload.json"
  local samples="$run_dir/$sequence-$label-canceled-upload-agent-samples.jsonl"
  local server_spec initial_agent final_agent lane_high_water initial_session_identities final_session_identities observation_token origin_observation
  : > "$samples"
  observation_token="cancel-$sequence"
  initial_agent=$(query_agent_json "$agent_pid" "$agent_admin")
  initial_session_identities=$(jq -c '[.tunnel_sessions[] | {lane,session_identity}] | sort_by(.lane)' <<<"$initial_agent")
  jq -e '
    .agent_capacity.in_use == 0 and
    ([.tunnel_sessions[] | select(.aggregate == true and .lane > 0)] | length == 4) and
    all(.tunnel_sessions[] | select(.aggregate == true and .lane > 0);
      ((.buffered_units // 0) == 0) and ((.pending_refund_bytes // 0) == 0))
  ' <<<"$initial_agent" >/dev/null
  server_spec=$(jq -cn --argjson pid "$server_pid" --arg admin "$server_admin" --arg cpus "$server_cpu_set" '{role:"server",pid:$pid,admin_url:$admin,cpu_set:$cpus}')
  P2PSTREAM_QUALIFICATION_ROLE=load \
  P2PSTREAM_QUALIFICATION_URL="$public_url/agent/bulk-upload?qualification_cancel=$observation_token" \
  P2PSTREAM_QUALIFICATION_SCENARIO=growth-upload-cancel \
  P2PSTREAM_QUALIFICATION_PROTOCOL=h2 \
  P2PSTREAM_QUALIFICATION_CONCURRENCY=1 \
  P2PSTREAM_QUALIFICATION_REQUESTS=1 \
  P2PSTREAM_QUALIFICATION_OFFERED_RPS=0 \
  P2PSTREAM_QUALIFICATION_DURATION_SECONDS=0 \
  P2PSTREAM_QUALIFICATION_WARMUP_SECONDS=0 \
  P2PSTREAM_QUALIFICATION_GROWTH_PAYLOAD_BYTES="$payload_bytes" \
  P2PSTREAM_QUALIFICATION_ORIGIN_OBSERVER_URL="$origin_admin" \
  P2PSTREAM_QUALIFICATION_UPLOAD_OBSERVATION_TOKEN="$observation_token" \
  P2PSTREAM_QUALIFICATION_LABEL="$label" \
  P2PSTREAM_QUALIFICATION_PATH=agent-external-canceled-upload \
  P2PSTREAM_QUALIFICATION_PIDS="$server_pid" \
  P2PSTREAM_QUALIFICATION_ADMIN_URL="$server_admin" \
  P2PSTREAM_QUALIFICATION_PROXY_CPU_SET="$server_cpu_set" \
  P2PSTREAM_QUALIFICATION_SERVER_PROCESS="$server_spec" \
  P2PSTREAM_QUALIFICATION_LOAD_CPU_SET="$load_cpu_set" \
  P2PSTREAM_QUALIFICATION_RESOURCE_ENVELOPE="p2pstream-external-agent-canceled-upload" \
  P2PSTREAM_QUALIFICATION_SEQUENCE="$sequence" \
  P2PSTREAM_QUALIFICATION_RESULT="$raw" \
    setsid taskset -c "$load_cpu_set" "$driver_binary" -test.run '^TestProxyQualificationLoad$' -test.timeout 45s \
    >"$raw.log" 2>&1 &
  local load_pid=$!
  owned_groups[$load_pid]=1
  while kill -0 "$load_pid" 2>/dev/null; do
    {
      query_agent_json "$agent_pid" "$agent_admin"
      printf '\n'
    } >> "$samples"
    sleep .02
  done
  wait "$load_pid"
  unset 'owned_groups[$load_pid]'
  jq -e --argjson bytes "$payload_bytes" '
    .attempted == 1 and .useful_completed == 0 and .errors == 1 and .canceled_errors == 1 and .observed_http_major == 2 and
    .configured_payload_bytes == $bytes and .request_body_bytes > 0 and .request_body_bytes < $bytes and
    .origin_request_body_bytes > 0 and .origin_request_body_bytes < $bytes and
    ((.origin_body_barrier_timed_out // false) == false) and
    .event_stats_captured == true and .persisted_event_delta == 1 and
    ((.dropped_event_delta // 0) == 0) and ((.event_queue_after_drain // 0) == 0) and ((.event_drain_error // "") == "")
  ' "$raw" >/dev/null
  origin_observation=$(python3 - "$origin_admin" "$observation_token" <<'PY'
import json, sys, urllib.parse, urllib.request
url = sys.argv[1] + "?" + urllib.parse.urlencode({"upload_token": sys.argv[2]})
with urllib.request.urlopen(url, timeout=2) as response:
    json.dump(json.load(response), sys.stdout, separators=(",", ":"))
PY
)
  jq -e --arg token "$observation_token" --argjson bytes "$(jq -r .origin_request_body_bytes "$raw")" '
    .token == $token and .received_body_bytes >= $bytes and .received_body_bytes > 0
  ' <<<"$origin_observation" >/dev/null
  wait_aggregate_lanes_grown_and_quiescent "$server_admin" "$agent_admin" "$((required_pool_mib << 20))"
  final_agent=$(query_agent_json "$agent_pid" "$agent_admin")
  final_session_identities=$(jq -c '[.tunnel_sessions[] | {lane,session_identity}] | sort_by(.lane)' <<<"$final_agent")
  [[ $initial_session_identities == "$final_session_identities" ]] || {
    echo "canceled upload replaced an aggregate tunnel session" >&2
    return 1
  }
  jq -e '
    .agent_capacity.in_use == 0 and
    ([.tunnel_sessions[] | select(.aggregate == true)] | length == 5) and
    all(.tunnel_sessions[] | select(.aggregate == true and .lane > 0);
      .receive_available_bytes == .receive_limit_bytes and .free_units == .total_units and
      ((.buffered_units // 0) == 0) and ((.pending_refund_bytes // 0) == 0))
  ' <<<"$final_agent" >/dev/null
  lane_high_water=$(python3 - "$samples" <<'PY'
import json, sys
high = {}
for line in open(sys.argv[1], encoding="utf-8"):
    row = json.loads(line)
    for lane in row.get("tunnel_sessions", []):
        if not lane.get("aggregate") or lane.get("lane", 0) <= 0:
            continue
        target = high.setdefault(str(lane["lane"]), {"buffered_units": 0, "pending_refund_bytes": 0})
        target["buffered_units"] = max(target["buffered_units"], lane.get("buffered_units", 0))
        target["pending_refund_bytes"] = max(target["pending_refund_bytes"], lane.get("pending_refund_bytes", 0))
print(json.dumps(high, separators=(",", ":")))
PY
)
  jq -c --argjson initial_agent "$initial_agent" --argjson final_agent "$final_agent" \
    --argjson lane_high_water "$lane_high_water" --argjson session_identities "$initial_session_identities" \
    --argjson origin_observation "$origin_observation" \
    '. + {measurement_stage:"canceled-upload",transport_mode:"aggregate-v1",
      agent_runtime_before:$initial_agent,agent_runtime_after:$final_agent,
      agent_session_identities:$session_identities,
      agent_lane_cancel_high_water:$lane_high_water,origin_upload_observation:$origin_observation,
      origin_receiving_payload_observed:true,
      agent_lane_refunds_quiescent:true}' "$raw" >> "$output"
}

run_pressure_qualification() {
  local label=$1 server_pid=$2 public_url=$3 server_admin=$4 agent_pid=$5 agent_admin=$6
  local server_cgroup agent_cgroup baseline_oom baseline_oom_kill server_baseline_oom server_baseline_oom_kill baseline_health
  close_idle_agent_transports "$server_admin"
  wait_aggregate_lanes_grown_and_quiescent "$server_admin" "$agent_admin" "$((required_pool_mib << 20))"
  server_cgroup=$(cgroup_snapshot "$server_pid")
  agent_cgroup=$(cgroup_snapshot "$agent_pid")
  jq -e '
    .memory_max_bytes == 4294967296 and .memory_swap_max_bytes == 0 and
    (.memory_events | has("oom") and has("oom_kill")) and .memory_events.oom == 0 and .memory_events.oom_kill == 0
  ' <<<"$server_cgroup" >/dev/null
  jq -e '
    .memory_max_bytes == 4294967296 and .memory_swap_max_bytes == 0 and
    (.memory_events | has("oom") and has("oom_kill")) and .memory_events.oom == 0 and .memory_events.oom_kill == 0
  ' <<<"$agent_cgroup" >/dev/null
  [[ $(jq -r .cgroup_path <<<"$server_cgroup") != $(jq -r .cgroup_path <<<"$agent_cgroup") ]] || {
    echo "server and agent must use distinct constrained cgroups" >&2
    return 1
  }
  baseline_oom=$(jq -r '.memory_events.oom' <<<"$agent_cgroup")
  baseline_oom_kill=$(jq -r '.memory_events.oom_kill' <<<"$agent_cgroup")
  server_baseline_oom=$(jq -r '.memory_events.oom' <<<"$server_cgroup")
  server_baseline_oom_kill=$(jq -r '.memory_events.oom_kill' <<<"$server_cgroup")
  baseline_health=$(agent_health_roundtrip "$server_admin")
  jq -e '.ok == true and .http_status == 200 and .status_code >= 200 and .status_code < 400' <<<"$baseline_health" >/dev/null
  wait_aggregate_lanes_grown_and_quiescent "$server_admin" "$agent_admin" "$((required_pool_mib << 20))"
  local cancel_payload_mib=$warmup_mib
  ((cancel_payload_mib > 32)) && cancel_payload_mib=32
  run_canceled_upload_trace "$label" "$server_pid" "$public_url" "$server_admin" "$agent_pid" "$agent_admin" "$((cancel_payload_mib << 20))"
  append_pressure_sample pressure-baseline 0 "$server_pid" "$server_admin" "$agent_pid" "$agent_admin"

  local ballast_mib=0 current maximum next_limit margin pressure_reached=0
  while ((ballast_mib < 3840)); do
    agent_cgroup=$(cgroup_snapshot "$agent_pid")
    current=$(jq -r .memory_current_bytes <<<"$agent_cgroup")
    maximum=$(jq -r .memory_max_bytes <<<"$agent_cgroup")
    next_limit=$((current + (64 << 20)))
    ((next_limit <= maximum - (256 << 20))) || {
      echo "cannot safely reach 90% pressure while preserving the 256 MiB cgroup margin" >&2
      return 1
    }
    ballast_mib=$((ballast_mib + 64))
    set_agent_ballast "$agent_pid" "$agent_admin" "$ballast_mib" >/dev/null
    sleep .25
    append_pressure_sample pressure-step "$ballast_mib" "$server_pid" "$server_admin" "$agent_pid" "$agent_admin"
    agent_cgroup=$(cgroup_snapshot "$agent_pid")
    jq -e --argjson oom "$baseline_oom" --argjson kill "$baseline_oom_kill" '
      .memory_events.oom == $oom and .memory_events.oom_kill == $kill
    ' <<<"$agent_cgroup" >/dev/null
    current=$(jq -r .memory_current_bytes <<<"$agent_cgroup")
    maximum=$(jq -r .memory_max_bytes <<<"$agent_cgroup")
    if ((current * 100 >= maximum * 90)); then
      ((maximum - current >= 256 << 20)) || {
        echo "90% pressure sample has less than the required 256 MiB cgroup margin" >&2
        return 1
      }
      pressure_reached=1
      break
    fi
  done
  ((pressure_reached)) || { echo "agent cgroup never reached 90% actual usage" >&2; return 1; }

  local agent_runtime
  for ((attempt=0; attempt<50; attempt++)); do
    agent_runtime=$(query_agent_json "$agent_pid" "$agent_admin")
    if jq -e '.agent_capacity.pressure == "critical" and .agent_capacity.pressure_reason == "memory" and (.agent_capacity.sample_error // "") == ""' <<<"$agent_runtime" >/dev/null; then
      break
    fi
    sleep .1
  done
  jq -e '.agent_capacity.pressure == "critical" and .agent_capacity.pressure_reason == "memory" and (.agent_capacity.sample_error // "") == ""' <<<"$agent_runtime" >/dev/null
  append_pressure_sample pressure-critical "$ballast_mib" "$server_pid" "$server_admin" "$agent_pid" "$agent_admin"
  local rejected_before health_at_pressure health_agent_before health_agent_after health_agent_quiescent
  rejected_before=$(jq -r .agent_capacity.rejected_pressure <<<"$agent_runtime")
  run_pressure_rejection "$label" "$server_pid" "$public_url" "$server_admin" "$agent_pid" "$agent_admin" "$rejected_before"
  health_agent_before=$(query_agent_json "$agent_pid" "$agent_admin")
  health_at_pressure=$(agent_health_roundtrip "$server_admin")
  health_agent_after=$(query_agent_json "$agent_pid" "$agent_admin")
  health_agent_quiescent=$health_agent_after
  local control_baseline
  control_baseline=$(jq -r '.agent_capacity.control_in_use' <<<"$health_agent_before")
  for ((attempt=0; attempt<100; attempt++)); do
    health_agent_quiescent=$(query_agent_json "$agent_pid" "$agent_admin")
    if jq -e --argjson baseline "$control_baseline" '
      .agent_capacity.control_in_use == $baseline and .agent_capacity.in_use == $baseline
    ' <<<"$health_agent_quiescent" >/dev/null; then
      break
    fi
    sleep .05
  done
  jq -e --argjson baseline "$control_baseline" '
    .agent_capacity.control_in_use == $baseline and .agent_capacity.in_use == $baseline
  ' <<<"$health_agent_quiescent" >/dev/null
  local health_sessions_before health_sessions_quiescent
  health_sessions_before=$(jq -c '[.tunnel_sessions[] | {lane,session_identity}] | sort_by(.lane)' <<<"$health_agent_before")
  health_sessions_quiescent=$(jq -c '[.tunnel_sessions[] | {lane,session_identity}] | sort_by(.lane)' <<<"$health_agent_quiescent")
  [[ $health_sessions_before == "$health_sessions_quiescent" ]] || {
    echo "pressure control roundtrip replaced a tunnel session" >&2
    return 1
  }
  jq -cn --argjson health "$health_at_pressure" --argjson ballast_mib "$ballast_mib" \
    --argjson agent_before "$health_agent_before" --argjson agent_immediate "$health_agent_after" \
    --argjson agent_after "$health_agent_quiescent" --argjson session_identities "$health_sessions_before" \
    '{measurement_stage:"pressure-control-roundtrip",transport_mode:"aggregate-v1",configured_agent_ballast_mib:$ballast_mib,
      control_roundtrip:$health,control_progress_proven:($health.ok == true),
      ordinary_admission_limit_before:$agent_before.agent_capacity.ordinary_admission_limit,
      ordinary_admission_limit_after:$agent_after.agent_capacity.ordinary_admission_limit,
      control_in_use_before:$agent_before.agent_capacity.control_in_use,
      control_in_use_immediate:$agent_immediate.agent_capacity.control_in_use,
      control_in_use_after:$agent_after.agent_capacity.control_in_use,
      control_reserved_slots:$agent_after.agent_capacity.control_reserved_slots,
      control_reserved_base_bytes:$agent_after.agent_capacity.control_reserved_base_bytes,
      control_reserved_base_fds:$agent_after.agent_capacity.control_reserved_base_fds,
      agent_rejected_pressure_before:$agent_before.agent_capacity.rejected_pressure,
      agent_rejected_pressure_after:$agent_after.agent_capacity.rejected_pressure,
      agent_session_identities:$session_identities,
      agent_runtime_before:$agent_before,agent_runtime_immediate:$agent_immediate,agent_runtime_after:$agent_after}' >> "$output"

  local server_runtime
  server_runtime=$(query_server_json "$server_admin")
  agent_runtime=$(query_agent_json "$agent_pid" "$agent_admin")
  jq -e '
    .agent_tunnels | length == 1 and .[0].connected and .[0].control_connected and .[0].bulk_sessions == 4 and
    (.[0].lanes | length == 5) and
    (.[0].lanes[0].lane == 0 and (.[0].lanes[0].aggregate // false) == false) and
    ([.[0].lanes[] | select(.lane > 0 and .aggregate == true)] | length == 4)
  ' <<<"$server_runtime" >/dev/null
  jq -e '
    (.tunnel_sessions | length == 5) and
    ([.tunnel_sessions[] | select(.lane == 0 and .aggregate == true)] | length == 1) and
    ([.tunnel_sessions[] | select(.lane > 0 and .aggregate == true)] | length == 4)
  ' <<<"$agent_runtime" >/dev/null

  set_agent_ballast "$agent_pid" "$agent_admin" 0 >/dev/null
  local recovered=0
  for ((attempt=0; attempt<600; attempt++)); do
    agent_cgroup=$(cgroup_snapshot "$agent_pid")
    agent_runtime=$(query_agent_json "$agent_pid" "$agent_admin")
    current=$(jq -r .memory_current_bytes <<<"$agent_cgroup")
    maximum=$(jq -r .memory_max_bytes <<<"$agent_cgroup")
    if ((current * 100 < maximum * 75)) && jq -e '.agent_capacity.pressure == "healthy" and .agent_capacity.in_use == 0' <<<"$agent_runtime" >/dev/null; then
      recovered=1
      break
    fi
    sleep .1
  done
  ((recovered)) || { echo "agent did not recover below 75% actual memory with healthy admission" >&2; return 1; }
  jq -e --argjson oom "$baseline_oom" --argjson kill "$baseline_oom_kill" '
    .memory_events.oom == $oom and .memory_events.oom_kill == $kill
  ' <<<"$agent_cgroup" >/dev/null
  server_cgroup=$(cgroup_snapshot "$server_pid")
  jq -e --argjson oom "$server_baseline_oom" --argjson kill "$server_baseline_oom_kill" '
    .memory_events.oom == $oom and .memory_events.oom_kill == $kill
  ' <<<"$server_cgroup" >/dev/null
  wait_aggregate_lanes_grown_and_quiescent "$server_admin" "$agent_admin" "$((required_pool_mib << 20))"
  append_pressure_sample pressure-released 0 "$server_pid" "$server_admin" "$agent_pid" "$agent_admin"
  local recovery_health
  recovery_health=$(agent_health_roundtrip "$server_admin")
  jq -e '.ok == true and .http_status == 200 and .status_code >= 200 and .status_code < 400' <<<"$recovery_health" >/dev/null
  run_transfer "$label" aggregate-v1 "$server_pid" "$public_url" "$server_admin" "$agent_pid" "$agent_admin" pressure-recovery download "$((16 << 20))" 1
  run_transfer "$label" aggregate-v1 "$server_pid" "$public_url" "$server_admin" "$agent_pid" "$agent_admin" pressure-recovery upload "$((16 << 20))" 1
  append_pressure_sample pressure-recovered 0 "$server_pid" "$server_admin" "$agent_pid" "$agent_admin"
  jq -cn --argjson baseline_health "$baseline_health" --argjson pressure_health "$health_at_pressure" \
    --argjson recovery_health "$recovery_health" --argjson ballast_mib "$ballast_mib" \
    '{measurement_stage:"pressure-result",transport_mode:"aggregate-v1",pressure_reached:true,
      pressure_ballast_mib:$ballast_mib,baseline_control_roundtrip:$baseline_health,
      pressure_control_roundtrip:$pressure_health,recovery_control_roundtrip:$recovery_health,
      control_progress_during_pressure:($pressure_health.ok == true),functional_recovery:true}' >> "$output"
}

run_cohort() {
  local label=$1 mode=$2 endpoint_server_binary=$3 agent_binary=$4 iteration=$5
  local endpoint_server_binary_sha256 endpoint_agent_binary_sha256
  endpoint_server_binary_sha256=$(sha256sum "$endpoint_server_binary" | awk '{print $1}')
  endpoint_agent_binary_sha256=$(sha256sum "$agent_binary" | awk '{print $1}')
  if ((pressure_mode)) && [[ $mode != aggregate-v1 ]]; then
    echo "pressure qualification requires aggregate-v1" >&2
    return 2
  fi
  local server_ready=$run_dir/$label-$iteration-server.ready.json server_stop=$run_dir/$label-$iteration-server.stop
  local management_ca=$run_dir/$label-$iteration-management-ca.pem
  local server_process_pid server_unit="p2p-qual-server-$BASHPID-$iteration"
  if ((pressure_mode)); then
    setsid systemd-run --user --scope --quiet --unit="$server_unit" \
      -p MemoryMax=4294967296 -p MemorySwapMax=0 -- \
      nsenter -t "$$" -n env \
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
      taskset -c "$server_cpu_set" "$endpoint_server_binary" -test.run '^TestExternalProxyQualificationServerProcess$' -test.timeout "${overall_timeout_seconds}s" \
      >"$run_dir/$label-$iteration-server.log" 2>&1 &
  else
    setsid env \
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
      taskset -c "$server_cpu_set" "$endpoint_server_binary" -test.run '^TestExternalProxyQualificationServerProcess$' -test.timeout "${overall_timeout_seconds}s" \
      >"$run_dir/$label-$iteration-server.log" 2>&1 &
  fi
  server_process_pid=$!
  owned_groups[$server_process_pid]=1
  stops+=("$server_stop")
  wait_ready "$server_ready" "$server_process_pid"
  local server_pid public_url server_admin management_url public_id token
  server_pid=$(jq -r .pid "$server_ready")
  public_url=$(jq -r .url "$server_ready")
  server_admin=$(jq -r .admin_url "$server_ready")
  management_url=$(jq -r .management_url "$server_ready")
  public_id=$(jq -r '.agents[0].public_id' "$server_ready")
  token=$(jq -r '.agents[0].token' "$server_ready")

  local agent_ready=$run_dir/$label-$iteration-agent.ready.json agent_stop=$run_dir/$label-$iteration-agent.stop
  local agent_process_pid agent_unit="p2p-qual-agent-$BASHPID-$iteration"
  if ((pressure_mode)); then
    setsid systemd-run --user --scope --quiet --unit="$agent_unit" \
      -p MemoryMax=4294967296 -p MemorySwapMax=0 -- \
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
  else
    setsid nsenter -t "$agent_namespace_pid" -n env \
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
  fi
  agent_process_pid=$!
  owned_groups[$agent_process_pid]=1
  stops+=("$agent_stop")
  wait_ready "$agent_ready" "$agent_process_pid"
  local agent_pid agent_admin expected_bulk
  agent_pid=$(jq -r .pid "$agent_ready")
  agent_admin=$(jq -r .admin_url "$agent_ready")
  expected_bulk=3
  [[ $mode == aggregate-v1 ]] && expected_bulk=4
  wait_mode "$server_admin" "$mode" "$expected_bulk"
  capture_tunnel_socket_snapshot_pair "$label" "$iteration" before-load "$server_admin" "$agent_pid" "$agent_admin"

  if ((tiny_response_mode)); then
    local tiny_specs=(h1:16 h1:128 h2:16 h2:128)
    if ((iteration % 2 == 0)); then tiny_specs=(h2:128 h2:16 h1:128 h1:16); fi
    local tiny_spec tiny_protocol tiny_concurrency
    for tiny_spec in "${tiny_specs[@]}"; do
      tiny_protocol=${tiny_spec%%:*}
      tiny_concurrency=${tiny_spec##*:}
      run_tiny_response "$label" "$mode" "$server_pid" "$public_url" "$server_admin" "$agent_pid" "$agent_admin" \
        "$tiny_protocol" "$tiny_concurrency" "$iteration"
    done
    stop_process "$agent_process_pid" "$agent_stop"
    stop_process "$server_process_pid" "$server_stop"
    return
  fi

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
  if ((pressure_mode)); then
    run_pressure_qualification "$label" "$server_pid" "$public_url" "$server_admin" "$agent_pid" "$agent_admin"
  else
    for ((repetition=1; repetition<=transfer_repetitions; repetition++)); do
      if ((repetition % 2)); then directions=(download upload); else directions=(upload download); fi
      for direction in "${directions[@]}"; do
        run_transfer "$label" "$mode" "$server_pid" "$public_url" "$server_admin" "$agent_pid" "$agent_admin" steady "$direction" "$((steady_mib << 20))" "$repetition"
      done
    done
  fi

  capture_tunnel_socket_snapshot_pair "$label" "$iteration" after-load "$server_admin" "$agent_pid" "$agent_admin"

  stop_process "$agent_process_pid" "$agent_stop"
  stop_process "$server_process_pid" "$server_stop"
}

for ((iteration=1; iteration<=runs; iteration++)); do
  if ((iteration % 2)); then
    run_cohort tunnel-lowat-default-control aggregate-v1 "$control_server_binary" "$control_agent_binary" "$iteration"
    run_cohort tunnel-lowat-32k aggregate-v1 "$lowat_server_binary" "$lowat_agent_binary" "$iteration"
  else
    run_cohort tunnel-lowat-32k aggregate-v1 "$lowat_server_binary" "$lowat_agent_binary" "$iteration"
    run_cohort tunnel-lowat-default-control aggregate-v1 "$control_server_binary" "$control_agent_binary" "$iteration"
  fi
done
stop_process "$origin_pid" "$origin_stop"
summary=${output%.jsonl}.summary.json
python3 - "$output" "$tunnel_socket_output" "$summary" <<'PY'
import json
import re
import statistics
import sys
from pathlib import Path

source, socket_source, destination = map(Path, sys.argv[1:])
rows = [json.loads(line) for line in source.read_text(encoding="utf-8").splitlines() if line.strip()]
socket_rows = [json.loads(line) for line in socket_source.read_text(encoding="utf-8").splitlines() if line.strip()]
sha256 = re.compile(r"[0-9a-f]{64}")
git_ref = re.compile(r"[0-9a-f]{40}")
variants = {
    "control": "tunnel-lowat-default-control",
    "lowat": "tunnel-lowat-32k",
}
expected_orders = [
    [variants["control"], variants["lowat"]],
    [variants["lowat"], variants["control"]],
    [variants["control"], variants["lowat"]],
]
result = {
    "scope": "diagnostic_only",
    "row_count": len(rows),
    "tunnel_socket_snapshot_count": len(socket_rows),
    "predeclared_gates": {
        "every_pair_download_lowat_to_control_ratio_minimum": 1.10,
        "every_pair_upload_lowat_to_control_ratio_minimum": 0.90,
        "lowat_all_24_download_samples_minimum_bytes_per_second": 29_700_000,
        "control_pairs_with_subthreshold_download_sample_minimum": 2,
        "physical_tunnel_socket_records": "exactly five per endpoint, reverse-correlated and unchanged after load",
        "lowat_effective_baseline": "role-matched across arms and greater than 32768",
        "candidate_notsent_lowat_after": 32768,
        "live_socket_option_polling": False,
        "payload_and_event_conservation": "exact",
        "selective_retries": False,
    },
    "fixed_workload": {
        "pairs": 3,
        "mode_order": ["control-then-lowat", "lowat-then-control", "control-then-lowat"],
        "transfers_per_variant": 16,
        "total_transfers": 96,
        "warmup_mib": 256,
        "steady_mib": 256,
        "transfer_repetitions": 3,
        "required_pool_mib": 8,
        "configured_rtt_ms": 80,
        "transport_mode_both_variants": "aggregate-v1",
    },
    "pairs": {},
}
failures = []
transition_threshold = 29_700_000
control_pairs_with_subthreshold_download = 0
lowat_download_samples = []
all_upload_pair_gates_passed = True
if len(rows) != 96:
    failures.append(f"row count {len(rows)}, want 96")
if len(socket_rows) != 12:
    failures.append(f"tunnel socket snapshot count {len(socket_rows)}, want 12")
expected_socket_sequence = [
    (iteration, label, stage)
    for iteration, order in enumerate(expected_orders, 1)
    for label in order
    for stage in ("before-load", "after-load")
]
observed_socket_sequence = [
    (row.get("iteration"), row.get("label"), row.get("stage")) for row in socket_rows
]
if observed_socket_sequence != expected_socket_sequence:
    failures.append("tunnel socket snapshot order does not match the fixed arm/stage sequence")


def socket_endpoint(value):
    import ipaddress
    if not isinstance(value, str) or not value:
        raise ValueError(value)
    if value.startswith("["):
        host, separator, port = value[1:].partition("]:")
        if not separator:
            raise ValueError(value)
    else:
        host, port = value.rsplit(":", 1)
    parsed_port = int(port)
    if parsed_port < 1 or parsed_port > 65535:
        raise ValueError(value)
    return str(ipaddress.ip_address(host)), parsed_port


socket_error_fields = (
    "namespace_notsent_lowat_error", "notsent_lowat_before_error",
    "notsent_lowat_after_error", "send_buffer_before_error",
    "send_buffer_after_error", "receive_buffer_before_error",
    "receive_buffer_after_error", "setter_error", "unwrap_error",
    "syscall_conn_error", "control_error",
)
socket_lowat_fields = (
    "requested_notsent_lowat", "namespace_notsent_lowat",
    "notsent_lowat_before", "notsent_lowat_after",
)
socket_buffer_fields = (
    "send_buffer_before", "send_buffer_after",
    "receive_buffer_before", "receive_buffer_after",
)
socket_boolean_fields = (
    "platform_supported", "apply_requested", "setter_attempted", "conclusive",
)


def valid_exact_int(mapping, name, minimum, maximum):
    return isinstance(mapping, dict) and name in mapping and type(mapping[name]) is int and minimum <= mapping[name] <= maximum


def valid_exact_bool(mapping, name):
    return isinstance(mapping, dict) and name in mapping and type(mapping[name]) is bool


def valid_empty_string(mapping, name):
    return isinstance(mapping, dict) and name in mapping and isinstance(mapping[name], str) and mapping[name] == ""


def validate_socket_response(response, role, candidate, context):
    if not isinstance(response, dict):
        failures.append(f"{context} {role} socket response is not an object")
        return [], [], [], []
    if not valid_exact_int(response, "schema_version", 1, 1) or response.get("process_role") != role:
        failures.append(f"{context} {role} socket schema/role mismatch")
    if "process_role" not in response or not isinstance(response["process_role"], str):
        failures.append(f"{context} {role} socket process role is missing or mistyped")
    if not valid_exact_int(response, "captured_unix_nano", 1, (1 << 63) - 1):
        failures.append(f"{context} {role} socket capture timestamp is missing")
    if (
        not valid_exact_int(response, "capacity", 0, (1 << 31) - 1)
        or not valid_exact_int(response, "dropped_observations", 0, (1 << 64) - 1)
        or response.get("capacity") != 64
        or response.get("dropped_observations") != 0
    ):
        failures.append(f"{context} {role} socket capacity/drop mismatch")
    observations = response.get("observations") if "observations" in response else None
    if not isinstance(observations, list) or len(observations) != 5:
        failures.append(f"{context} {role} socket observation count is not exactly 5")
        return [], [], [], []
    effective_baselines = []
    raw_baselines = []
    namespace_baselines = []
    for expected_index, item in enumerate(observations):
        if not isinstance(item, dict) or not valid_exact_int(item, "index", 0, 63) or item["index"] != expected_index:
            failures.append(f"{context} {role} socket indexes are not exactly 0..4")
        if not isinstance(item, dict) or "socket" not in item or not isinstance(item["socket"], dict):
            failures.append(f"{context} {role} socket object is missing or mistyped")
            continue
        socket = item["socket"]
        try:
            if "local_address" not in socket or "remote_address" not in socket:
                raise ValueError("missing address")
            socket_endpoint(socket["local_address"])
            socket_endpoint(socket["remote_address"])
        except (TypeError, ValueError):
            failures.append(f"{context} {role} has an invalid socket tuple")
        if any(not valid_exact_bool(socket, field) for field in socket_boolean_fields):
            failures.append(f"{context} {role} has a missing or mistyped socket boolean")
        elif not socket["conclusive"] or not socket["platform_supported"]:
            failures.append(f"{context} {role} has a nonconclusive socket")
        if any(not valid_empty_string(socket, field) for field in (*socket_error_fields, "inconclusive_reason")):
            failures.append(f"{context} {role} has a missing, mistyped, or nonempty socket error field")
        if any(not valid_exact_int(socket, field, 0, (1 << 32) - 1) for field in socket_lowat_fields):
            failures.append(f"{context} {role} has a missing or invalid LOWAT integer")
            raw_baseline = 0
            namespace_baseline = 0
        else:
            raw_baseline = socket["notsent_lowat_before"]
            namespace_baseline = socket["namespace_notsent_lowat"]
            if socket["requested_notsent_lowat"] != 32768:
                failures.append(f"{context} {role} requested LOWAT is not 32768")
        if any(not valid_exact_int(socket, field, 1, (1 << 63) - 1) for field in socket_buffer_fields):
            failures.append(f"{context} {role} has a missing or invalid observed socket buffer")
        baseline = raw_baseline or namespace_baseline
        raw_baselines.append(raw_baseline)
        namespace_baselines.append(namespace_baseline)
        effective_baselines.append(baseline)
        if candidate:
            if socket.get("apply_requested") is not True or socket.get("setter_attempted") is not True:
                failures.append(f"{context} {role} candidate did not attempt LOWAT")
            if socket.get("requested_notsent_lowat") != 32768 or socket.get("notsent_lowat_after") != 32768:
                failures.append(f"{context} {role} candidate LOWAT is not 32768")
        else:
            if socket.get("apply_requested") is not False or socket.get("setter_attempted") is not False:
                failures.append(f"{context} {role} control attempted LOWAT")
            if socket.get("notsent_lowat_after") != socket.get("notsent_lowat_before"):
                failures.append(f"{context} {role} control LOWAT changed")
    return observations, sorted(effective_baselines), sorted(raw_baselines), sorted(namespace_baselines)


socket_evidence = {}
for iteration in range(1, 4):
    iteration_evidence = {}
    for variant, label in variants.items():
        selected = [row for row in socket_rows if row.get("iteration") == iteration and row.get("label") == label]
        by_stage = {row.get("stage"): row for row in selected}
        context = f"pair {iteration} {variant}"
        if len(selected) != 2 or set(by_stage) != {"before-load", "after-load"}:
            failures.append(f"{context} socket stages are not exactly before-load/after-load")
            continue
        before, after = by_stage["before-load"], by_stage["after-load"]
        candidate = variant == "lowat"
        if before.get("validation_ok") is not True or before.get("validation_error"):
            failures.append(f"{context} before-load online socket validation failed")
        if after.get("validation_ok") is not True or after.get("validation_error"):
            failures.append(f"{context} after-load online socket validation failed")
        before_server, server_baselines, server_raw, server_namespace = validate_socket_response(
            before.get("server") or {}, "server", candidate, context + " before-load"
        )
        before_agent, agent_baselines, agent_raw, agent_namespace = validate_socket_response(
            before.get("agent") or {}, "agent", candidate, context + " before-load"
        )
        after_server, after_server_baselines, after_server_raw, after_server_namespace = validate_socket_response(
            after.get("server") or {}, "server", candidate, context + " after-load"
        )
        after_agent, after_agent_baselines, after_agent_raw, after_agent_namespace = validate_socket_response(
            after.get("agent") or {}, "agent", candidate, context + " after-load"
        )
        if before_server != after_server or before_agent != after_agent:
            failures.append(f"{context} physical socket observation set changed during load")
        if (
            server_baselines != after_server_baselines or agent_baselines != after_agent_baselines
            or server_raw != after_server_raw or agent_raw != after_agent_raw
            or server_namespace != after_server_namespace or agent_namespace != after_agent_namespace
        ):
            failures.append(f"{context} physical socket effective baseline changed during load")
        server_lane_fields_valid = True
        for row in before_server:
            if (
                not valid_exact_int(row, "lane", 0, 4)
                or "lane_role" not in row or not isinstance(row["lane_role"], str)
                or not valid_exact_bool(row, "aggregate")
            ):
                server_lane_fields_valid = False
                failures.append(f"{context} server lane metadata is missing or mistyped")
        if not server_lane_fields_valid or sorted(row["lane"] for row in before_server if valid_exact_int(row, "lane", 0, 4)) != list(range(5)):
            failures.append(f"{context} server lanes are not exactly 0..4")
        for row in (before_server if server_lane_fields_valid else []):
            lane = row.get("lane")
            if row.get("lane_role") != ("control" if lane == 0 else "bulk") or row.get("aggregate") is not True:
                failures.append(f"{context} server lane {lane!r} role/mode mismatch")
        try:
            agent_tuples = {
                (socket_endpoint(row["socket"]["local_address"]), socket_endpoint(row["socket"]["remote_address"])): row["index"]
                for row in before_agent
            }
            if len(agent_tuples) != len(before_agent):
                failures.append(f"{context} contains duplicate agent socket tuples")
            matches = []
            for row in sorted(before_server, key=lambda value: value["lane"]):
                reverse = (
                    socket_endpoint(row["socket"]["remote_address"]),
                    socket_endpoint(row["socket"]["local_address"]),
                )
                if reverse not in agent_tuples:
                    raise KeyError(reverse)
                matches.append({"lane": row["lane"], "server_index": row["index"], "agent_index": agent_tuples.pop(reverse)})
            if agent_tuples:
                failures.append(f"{context} contains unmatched agent socket tuples")
        except (KeyError, TypeError, ValueError):
            matches = []
            failures.append(f"{context} reverse socket tuple matching failed")
        if before.get("reverse_tuple_matches") != matches or after.get("reverse_tuple_matches") != matches:
            failures.append(f"{context} recorded reverse tuple mapping mismatch")
        iteration_evidence[variant] = {
            "server_effective_baselines": server_baselines,
            "agent_effective_baselines": agent_baselines,
            "server_raw_baselines": server_raw,
            "agent_raw_baselines": agent_raw,
            "server_namespace_baselines": server_namespace,
            "agent_namespace_baselines": agent_namespace,
            "reverse_tuple_matches": matches,
            "observations_stable_during_load": before_server == after_server and before_agent == after_agent,
        }
    if set(iteration_evidence) == {"control", "lowat"}:
        for role in ("server", "agent"):
            control_baselines = iteration_evidence["control"][f"{role}_effective_baselines"]
            lowat_baselines = iteration_evidence["lowat"][f"{role}_effective_baselines"]
            if control_baselines != lowat_baselines:
                failures.append(f"pair {iteration} {role} effective LOWAT baselines differ across arms")
            if iteration_evidence["control"][f"{role}_raw_baselines"] != iteration_evidence["lowat"][f"{role}_raw_baselines"]:
                failures.append(f"pair {iteration} {role} raw LOWAT baselines differ across arms")
            if iteration_evidence["control"][f"{role}_namespace_baselines"] != iteration_evidence["lowat"][f"{role}_namespace_baselines"]:
                failures.append(f"pair {iteration} {role} namespace LOWAT baselines differ across arms")
            if len(control_baselines) != 5 or any(value <= 32768 for value in control_baselines):
                failures.append(f"pair {iteration} {role} effective LOWAT baseline does not establish a >32768 contrast")
    socket_evidence[str(iteration)] = iteration_evidence
result["tunnel_socket_evidence"] = socket_evidence


def rate_summary(selected):
    values = [row["useful_transfer_bytes_per_second"] for row in selected]
    return {
        "count": len(values),
        "samples": values,
        "minimum": min(values) if values else None,
        "median": statistics.median(values) if values else None,
        "maximum": max(values) if values else None,
    }

for index, row in enumerate(rows, 1):
    if row.get("transport_mode") != "aggregate-v1":
        failures.append(f"row {index} transport mode is not aggregate-v1")
    exact = (
        row.get("attempted") == 1
        and row.get("useful_completed") == 1
        and row.get("errors") == 0
        and row.get("status_classes", {}).get("2xx") == 1
        and row.get("status_codes") == {"200": 1}
        and row.get("observed_http_major") == 2
        and row.get("observed_origin_http_major") == 1
        and row.get("configured_payload_bytes") == 256 * 1024 * 1024
        and row.get("response_bytes")
        == (256 * 1024 * 1024 if row.get("transfer_direction") == "download" else 32)
        and row.get("persisted_event_delta") == 1
        and row.get("dropped_event_delta", 0) == 0
        and row.get("event_queue_after_drain", 0) == 0
        and not row.get("event_drain_error")
        and row.get("configured_rtt_ms") == 80
        and row.get("shaped_rate_bits_per_second") == 1_000_000_000
        and len(row.get("agent_tunnels_final", [])) == 1
        and row.get("agent_tunnels_final", [{}])[0].get("flow_control") == "aggregate-v1"
    )
    if row.get("measurement_stage") == "steady":
        exact = exact and row.get("receiving_pool_observed_bytes", 0) > 8 * 1024 * 1024
    if not exact:
        failures.append(f"row {index} failed request/payload/event conservation")
    provenance = row.get("provenance") or {}
    expected_server_key = (
        "control_server_binary_sha256"
        if row.get("label") == variants["control"]
        else "lowat_server_binary_sha256"
    )
    expected_agent_key = (
        "control_agent_binary_sha256"
        if row.get("label") == variants["control"]
        else "lowat_agent_binary_sha256"
    )
    provenance_exact = (
        row.get("label") in variants.values()
        and all(
            sha256.fullmatch(provenance.get(key) or "")
            for key in (
                "driver_binary_sha256",
                "control_server_binary_sha256",
                "lowat_server_binary_sha256",
                "endpoint_server_binary_sha256",
                "endpoint_agent_binary_sha256",
                "control_agent_binary_sha256",
                "lowat_agent_binary_sha256",
                "common_scaffold_sha256",
                "lowat_32k_overlay_sha256",
            )
        )
        and git_ref.fullmatch(provenance.get("base_source_ref") or "")
        and git_ref.fullmatch(provenance.get("control_source_ref") or "")
        and git_ref.fullmatch(provenance.get("lowat_source_ref") or "")
        and provenance.get("endpoint_server_binary_sha256")
        == provenance.get(expected_server_key)
        and provenance.get("endpoint_agent_binary_sha256")
        == provenance.get(expected_agent_key)
    )
    if not provenance_exact:
        failures.append(f"row {index} failed endpoint/provenance identity")

for pair_index, expected_order in enumerate(expected_orders, 1):
    pair_rows = rows[(pair_index - 1) * 32 : pair_index * 32]
    observed_order = []
    for row in pair_rows:
        label = row.get("label")
        if label and (not observed_order or observed_order[-1] != label):
            observed_order.append(label)
    if observed_order != expected_order:
        failures.append(f"pair {pair_index} order {observed_order!r}, want {expected_order!r}")
    pair = {"label_order": observed_order, "variants": {}, "gates": {}}
    for variant, label in variants.items():
        variant_rows = [row for row in pair_rows if row.get("label") == label]
        if len(variant_rows) != 16:
            failures.append(f"pair {pair_index} {variant} has {len(variant_rows)} rows, want 16")
        variant_summary = {}
        for direction in ("download", "upload"):
            directional = [row for row in variant_rows if row.get("transfer_direction") == direction]
            warm = [row for row in directional if row.get("measurement_stage") == "warmup"]
            retained = [row for row in directional if row.get("measurement_stage") == "retained-warmup"]
            timed = sorted(
                [row for row in directional if row.get("measurement_stage") == "steady"],
                key=lambda row: row.get("repetition", 0),
            )
            if len(warm) != 4 or len(retained) != 1 or len(timed) != 3:
                failures.append(
                    f"pair {pair_index} {variant} {direction} stages are "
                    f"warm={len(warm)}, retained={len(retained)}, timed={len(timed)}"
                )
            variant_summary[direction] = {
                "fresh_lane_warmups": rate_summary(warm),
                "retained_transport_warmup": rate_summary(retained),
                "timed_all": rate_summary(timed),
                "timed_first_direction_uses": rate_summary(timed[:2]),
                "timed_reused_direction": rate_summary(timed[2:3]),
            }
        pair["variants"][variant] = variant_summary
    for direction in ("download", "upload"):
        control_median = pair["variants"]["control"][direction]["timed_all"]["median"]
        lowat_median = pair["variants"]["lowat"][direction]["timed_all"]["median"]
        ratio = lowat_median / control_median if control_median else None
        minimum = 1.10 if direction == "download" else 0.90
        passed = ratio is not None and ratio >= minimum
        pair["gates"][direction] = {
            "control_median_useful_bytes_per_second": control_median,
            "lowat_median_useful_bytes_per_second": lowat_median,
            "lowat_to_control_ratio": ratio,
            "minimum": minimum,
            "passed": passed,
        }
        if not passed:
            failures.append(f"pair {pair_index} {direction} lowat/control ratio {ratio!r} below {minimum}")
        if direction == "upload" and not passed:
            all_upload_pair_gates_passed = False
    control_pair_download = [
        row["useful_transfer_bytes_per_second"]
        for row in pair_rows
        if row.get("label") == variants["control"] and row.get("transfer_direction") == "download"
    ]
    lowat_pair_download = [
        row["useful_transfer_bytes_per_second"]
        for row in pair_rows
        if row.get("label") == variants["lowat"] and row.get("transfer_direction") == "download"
    ]
    if control_pair_download and min(control_pair_download) < transition_threshold:
        control_pairs_with_subthreshold_download += 1
    lowat_download_samples.extend(lowat_pair_download)
    pair["transition_screen"] = {
        "threshold_bytes_per_second": transition_threshold,
        "control_download_minimum": min(control_pair_download) if control_pair_download else None,
        "control_has_subthreshold_sample": bool(control_pair_download and min(control_pair_download) < transition_threshold),
        "lowat_download_minimum": min(lowat_pair_download) if lowat_pair_download else None,
        "lowat_all_eight_download_samples_pass": bool(
            len(lowat_pair_download) == 8 and min(lowat_pair_download) >= transition_threshold
        ),
    }
    result["pairs"][str(pair_index)] = pair

for direction in ("download", "upload"):
    control_rows = [
        row for row in rows
        if row.get("label") == variants["control"]
        and row.get("measurement_stage") == "steady"
        and row.get("transfer_direction") == direction
    ]
    lowat_rows = [
        row for row in rows
        if row.get("label") == variants["lowat"]
        and row.get("measurement_stage") == "steady"
        and row.get("transfer_direction") == direction
    ]
    control_summary = rate_summary(control_rows)
    lowat_summary = rate_summary(lowat_rows)
    result.setdefault("pooled_diagnostic", {})[direction] = {
        "control": control_summary,
        "lowat": lowat_summary,
        "lowat_to_control_ratio_of_medians": lowat_summary["median"] / control_summary["median"] if control_summary["median"] else None,
    }

ratio_failure = re.compile(r"^pair [1-3] (?:download|upload) lowat/control ratio .+ below (?:1\.1|0\.9)$")
timing_failures = [failure for failure in failures if ratio_failure.fullmatch(failure)]
non_timing_failures = [failure for failure in failures if not ratio_failure.fullmatch(failure)]
result["failures"] = failures
result["timing_gate_failures"] = timing_failures
result["non_timing_failures"] = non_timing_failures
result["acceptance"] = {
    "complete": len(rows) == 96,
    "all_rows_exact": not non_timing_failures,
    "no_non_timing_failures": not non_timing_failures,
    "all_pair_gates_passed": not timing_failures,
    "overall": not failures,
}
result["transition_screen"] = {
    "diagnostic_only": True,
    "does_not_override_main_pair_gates": True,
    "threshold_bytes_per_second": transition_threshold,
    "lowat_download_sample_count": len(lowat_download_samples),
    "lowat_download_minimum": min(lowat_download_samples) if lowat_download_samples else None,
    "lowat_all_24_download_samples_pass": bool(
        len(lowat_download_samples) == 24 and min(lowat_download_samples) >= transition_threshold
    ),
    "control_pairs_with_subthreshold_download_sample": control_pairs_with_subthreshold_download,
    "control_pairs_with_subthreshold_download_sample_minimum": 2,
    "all_pair_upload_gates_passed": all_upload_pair_gates_passed,
    "exact_correctness_and_cleanup": bool(
        result["acceptance"]["complete"]
        and result["acceptance"]["all_rows_exact"]
        and result["acceptance"]["no_non_timing_failures"]
    ),
}
result["transition_screen"]["consistent_with_stall_elimination"] = bool(
    result["transition_screen"]["lowat_all_24_download_samples_pass"]
    and control_pairs_with_subthreshold_download >= 2
    and all_upload_pair_gates_passed
    and result["transition_screen"]["exact_correctness_and_cleanup"]
)
result["all_phase_rows"] = [
    {
        "sequence": index,
        "pair": (index - 1) // 32 + 1,
        "within_pair": (index - 1) % 32 + 1,
        "label": row.get("label"),
        "measurement_stage": row.get("measurement_stage"),
        "transfer_direction": row.get("transfer_direction"),
        "repetition": row.get("repetition"),
        "useful_transfer_bytes_per_second": row.get("useful_transfer_bytes_per_second"),
        "wall_seconds": row.get("wall_seconds"),
    }
    for index, row in enumerate(rows, 1)
]
destination.write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")
raise SystemExit(0 if not failures else 1)
PY
printf 'results=%s\nsummary=%s\ntunnel_socket_observations=%s\nraw=%s\n' "$output" "$summary" "$tunnel_socket_output" "$run_dir"

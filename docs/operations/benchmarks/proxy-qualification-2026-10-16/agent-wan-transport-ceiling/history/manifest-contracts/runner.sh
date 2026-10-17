#!/usr/bin/env bash
# Diagnostic-only raw-TLS/current-Yamux transport-ceiling comparison.
set -Eeuo pipefail

server_binary=""
agent_binary=""
source_ref=""
expected_payload_sha256=""
build_manifest=""
output=""
fixture_smoke=0

while (($#)); do
  case "$1" in
    --server-binary) server_binary=$2; shift 2 ;;
    --agent-binary) agent_binary=$2; shift 2 ;;
    --source-ref) source_ref=$2; shift 2 ;;
    --expected-payload-sha256) expected_payload_sha256=$2; shift 2 ;;
    --build-manifest) build_manifest=$2; shift 2 ;;
    --output) output=$2; shift 2 ;;
    --fixture-smoke) fixture_smoke=1; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

[[ -x $server_binary && -x $agent_binary && -f $build_manifest && $source_ref =~ ^[0-9a-f]{40}$ && -n $output && $expected_payload_sha256 =~ ^[0-9a-f]{64}$ ]] || {
  echo "--server-binary, --agent-binary, --source-ref, --expected-payload-sha256, --build-manifest and --output are required" >&2
  exit 2
}
[[ ${P2PSTREAM_TRANSPORT_CEILING_INNER_FIXTURE:-} == 1 ]] || {
  echo "runner must be launched by the reviewed wrapper in a private network namespace" >&2
  exit 2
}
[[ $$ -eq 1 ]] || {
  echo "runner must be PID 1 in its owned fixture container" >&2
  exit 2
}
for tool in awk cmp ip jq nsenter openssl ping python3 readlink realpath setsid sha256sum sysctl taskset tc unshare; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 2; }
done
mapfile -t initial_interfaces < <(ip -o link show | awk -F': ' '{sub(/@.*/,"",$2); print $2}')
[[ ${#initial_interfaces[@]} -eq 1 && ${initial_interfaces[0]} == lo && -z $(ip route show) && -z $(ip -6 route show) ]] || {
  echo "runner requires a clean container network namespace containing only loopback and no IPv4/IPv6 routes" >&2
  exit 2
}

configured_rtt_ms=80
one_way_netem_ms=40
payload_bytes=268435456
phase_count=16
pair_count=2
pool_limit_bytes=2147483648
working_limit_bytes=67108864
overall_timeout_seconds=1500
server_address=10.211.0.1:17443
server_admin=http://127.0.0.1:18080
agent_admin=http://127.0.0.1:18080
if ((fixture_smoke)); then
  configured_rtt_ms=10
  one_way_netem_ms=5
  pair_count=1
fi

mkdir -p "$(dirname "$output")"
run_dir=$(mktemp -d -p "$(dirname "$output")" transport-ceiling.XXXXXXXX)
: > "$output"
snapshots_output=${output%.jsonl}.snapshots.jsonl
: > "$snapshots_output"
topologies_output=${output%.jsonl}.topologies.jsonl
: > "$topologies_output"

declare -A owned_groups=()
declare -A seen_namespace_identities=()
cleanup() {
  local pid attempt live
  for pid in "${!owned_groups[@]}"; do kill -TERM -- "-$pid" 2>/dev/null || true; done
  for ((attempt=0; attempt<50; attempt++)); do
    live=0
    for pid in "${!owned_groups[@]}"; do kill -0 -- "-$pid" 2>/dev/null && live=1; done
    ((live)) || break
    sleep .1
  done
  for pid in "${!owned_groups[@]}"; do
    kill -KILL -- "-$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

server_namespace_pid=""
router_namespace_pid=""
agent_namespace_pid=""
server_ns() { nsenter -t "$server_namespace_pid" -n "$@"; }
router_ns() { nsenter -t "$router_namespace_pid" -n "$@"; }
agent_ns() { nsenter -t "$agent_namespace_pid" -n "$@"; }

start_topology() {
  local pid attempt iface
  setsid unshare --net sleep "$overall_timeout_seconds" &
  server_namespace_pid=$!
  owned_groups[$server_namespace_pid]=1
  setsid unshare --net sleep "$overall_timeout_seconds" &
  router_namespace_pid=$!
  owned_groups[$router_namespace_pid]=1
  setsid unshare --net sleep "$overall_timeout_seconds" &
  agent_namespace_pid=$!
  owned_groups[$agent_namespace_pid]=1
  for pid in "$server_namespace_pid" "$router_namespace_pid" "$agent_namespace_pid"; do
    for ((attempt=0; attempt<100; attempt++)); do
      [[ -e /proc/$pid/ns/net && $(readlink "/proc/$pid/ns/net") != "$(readlink /proc/self/ns/net)" ]] && break
      sleep .01
    done
    [[ -e /proc/$pid/ns/net && $(readlink "/proc/$pid/ns/net") != "$(readlink /proc/self/ns/net)" ]]
  done
  server_namespace_identity=$(readlink "/proc/$server_namespace_pid/ns/net")
  router_namespace_identity=$(readlink "/proc/$router_namespace_pid/ns/net")
  agent_namespace_identity=$(readlink "/proc/$agent_namespace_pid/ns/net")
  for identity in "$server_namespace_identity" "$router_namespace_identity" "$agent_namespace_identity"; do
    [[ $identity =~ ^net:\[[0-9]+\]$ && -z ${seen_namespace_identities[$identity]+x} ]] || {
      echo "network namespace identity $identity is invalid or reused" >&2
      return 1
    }
    seen_namespace_identities[$identity]=1
  done

  server_ns ip link set lo up
  agent_ns ip link set lo up
  router_ns ip link set lo up
  ip link add ceil-server type veth peer name ceil-rs
  ip link set ceil-server netns "$server_namespace_pid"
  ip link set ceil-rs netns "$router_namespace_pid"
  ip link add ceil-agent type veth peer name ceil-ra
  ip link set ceil-agent netns "$agent_namespace_pid"
  ip link set ceil-ra netns "$router_namespace_pid"
  server_ns ip addr add 10.211.0.1/24 dev ceil-server
  server_ns ip link set ceil-server up
  router_ns ip addr add 10.211.0.2/24 dev ceil-rs
  router_ns ip addr add 10.212.0.1/24 dev ceil-ra
  router_ns ip link set ceil-rs up
  router_ns ip link set ceil-ra up
  agent_ns ip addr add 10.212.0.2/24 dev ceil-agent
  agent_ns ip link set ceil-agent up
  server_ns ip route add 10.212.0.0/24 via 10.211.0.2
  agent_ns ip route add 10.211.0.0/24 via 10.212.0.1
  router_ns sysctl -qw net.ipv4.ip_forward=1
  for iface in ceil-rs ceil-ra; do
    router_ns tc qdisc add dev "$iface" root netem delay "${one_way_netem_ms}ms" rate 1gbit limit 100000
  done
  measured_rtt_ms=$(agent_ns ping -c 5 -q 10.211.0.1 | awk -F/ '/rtt|round-trip/ {print $5}')
  python3 - "$measured_rtt_ms" "$configured_rtt_ms" <<'PY'
import sys
measured=float(sys.argv[1])
configured=int(sys.argv[2])
minimum,maximum=((5,25) if configured==10 else (60,120))
if not minimum <= measured <= maximum:
    raise SystemExit(f"measured RTT {measured}ms is outside the configured {configured}ms diagnostic envelope")
PY
}

capture_topology() {
  local pair=$1 order_position=$2 variant=$3 destination=$4
  local server_addresses server_routes router_addresses router_routes router_qdisc agent_addresses agent_routes
  server_addresses=$(server_ns ip -j address show)
  server_routes=$(server_ns ip -j route show)
  router_addresses=$(router_ns ip -j address show)
  router_routes=$(router_ns ip -j route show)
  router_qdisc=$(router_ns tc -j qdisc show)
  agent_addresses=$(agent_ns ip -j address show)
  agent_routes=$(agent_ns ip -j route show)
  jq -n \
    --argjson pair "$pair" --argjson order_position "$order_position" --arg variant "$variant" \
    --arg server_namespace_identity "$server_namespace_identity" \
    --arg router_namespace_identity "$router_namespace_identity" \
    --arg agent_namespace_identity "$agent_namespace_identity" \
    --arg server_cpu_set "$server_cpu_set" --arg agent_cpu_set "$agent_cpu_set" --arg load_cpu_set "$load_cpu_set" \
    --argjson measured_rtt_ms "$measured_rtt_ms" \
    --argjson configured_rtt_ms "$configured_rtt_ms" --argjson one_way_netem_ms "$one_way_netem_ms" \
    --argjson server_addresses "$server_addresses" --argjson server_routes "$server_routes" \
    --argjson router_addresses "$router_addresses" --argjson router_routes "$router_routes" --argjson router_qdisc "$router_qdisc" \
    --argjson agent_addresses "$agent_addresses" --argjson agent_routes "$agent_routes" \
    '{schema_version:1,pair:$pair,order_position:$order_position,variant:$variant,
      namespace_identities:{server:$server_namespace_identity,router:$router_namespace_identity,agent:$agent_namespace_identity},
      configured:{rtt_ms:$configured_rtt_ms,one_way_netem_ms:$one_way_netem_ms,rate_bits_per_second:1000000000,server_tunnel:"10.211.0.1:17443",server_admin:"127.0.0.1:18080",agent_admin:"127.0.0.1:18080"},
      cpu_sets:{server:$server_cpu_set,agent:$agent_cpu_set,load:$load_cpu_set},measured_rtt_ms:$measured_rtt_ms,
      network_state:{server:{addresses:$server_addresses,routes:$server_routes},router:{addresses:$router_addresses,routes:$router_routes,qdisc:$router_qdisc},agent:{addresses:$agent_addresses,routes:$agent_routes}}}' > "$destination"
  cat "$destination" >> "$topologies_output"
  printf '\n' >> "$topologies_output"
}

stop_namespace_helper() {
  local pid=$1 attempt
  [[ -n $pid ]] || return 0
  kill -TERM -- "-$pid" 2>/dev/null || true
  for ((attempt=0; attempt<50; attempt++)); do
    kill -0 -- "-$pid" 2>/dev/null || break
    sleep .02
  done
  kill -KILL -- "-$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  unset "owned_groups[$pid]"
}

stop_topology() {
  stop_namespace_helper "$agent_namespace_pid"
  stop_namespace_helper "$router_namespace_pid"
  stop_namespace_helper "$server_namespace_pid"
  agent_namespace_pid=""
  router_namespace_pid=""
  server_namespace_pid=""
}

mapfile -t cpus < <(python3 - <<'PY'
import os
for cpu in sorted(os.sched_getaffinity(0)): print(cpu)
PY
)
((${#cpus[@]} >= 9)) || { echo "at least nine logical CPUs are required" >&2; exit 2; }
server_cpu_set=$(IFS=,; echo "${cpus[*]:0:4}")
agent_cpu_set=$(IFS=,; echo "${cpus[*]:4:4}")
load_cpu_set=$(IFS=,; echo "${cpus[*]:8}")

cert=$run_dir/server.crt.pem
key=$run_dir/server.key.pem
auth_key=$run_dir/auth-key.hex
openssl ecparam -name prime256v1 -genkey -noout -out "$key"
openssl req -x509 -new -key "$key" -days 1 -sha256 \
  -subj /CN=transport-ceiling.test \
  -addext 'subjectAltName=DNS:transport-ceiling.test,IP:10.211.0.1' \
  -out "$cert" >/dev/null 2>&1
openssl rand -hex 32 > "$auth_key"

server_binary=$(realpath "$server_binary")
agent_binary=$(realpath "$agent_binary")
server_binary_sha256=$(sha256sum "$server_binary" | awk '{print $1}')
agent_binary_sha256=$(sha256sum "$agent_binary" | awk '{print $1}')
runner_sha256=$(sha256sum "$0" | awk '{print $1}')

http_json() {
  local namespace_pid=$1 method=$2 url=$3 body=${4:-}
  local -a prefix=(taskset -c "$load_cpu_set")
  [[ $namespace_pid == 0 ]] || prefix=(nsenter -t "$namespace_pid" -n taskset -c "$load_cpu_set")
  "${prefix[@]}" python3 - "$method" "$url" "$body" <<'PY'
import json,sys,urllib.request
method,url,body=sys.argv[1:]
data=body.encode() if body else None
request=urllib.request.Request(url,data=data,method=method,headers={"Content-Type":"application/json"})
with urllib.request.urlopen(request,timeout=300) as response:
    if response.status < 200 or response.status >= 300:
        raise SystemExit(f"HTTP {response.status} from {url}")
    payload=response.read()
    if payload:
        json.dump(json.loads(payload),sys.stdout,separators=(",",":"),sort_keys=True)
    else:
        sys.stdout.write("null")
PY
}

wait_admin_ready() {
  local namespace_pid=$1 url=$2 pid=$3 destination=$4
  local attempt
  for ((attempt=0; attempt<600; attempt++)); do
    if http_json "$namespace_pid" GET "$url/transport-ceiling/readiness" > "$destination.tmp" 2>/dev/null; then
      mv "$destination.tmp" "$destination"
      jq -e '.schema_version == 1 and .ready == true' "$destination" >/dev/null && return 0
    fi
    kill -0 -- "-$pid" 2>/dev/null || { echo "process $pid exited before readiness" >&2; return 1; }
    sleep .05
  done
  echo "timeout waiting for $url readiness" >&2
  return 1
}

wait_live_post16() {
  local namespace_pid=$1 url=$2 pid=$3 destination=$4
  local attempt
  for ((attempt=0; attempt<600; attempt++)); do
    if http_json "$namespace_pid" GET "$url/transport-ceiling/live-post16" > "$destination.tmp" 2>/dev/null; then
      mv "$destination.tmp" "$destination"
      jq -e '.schema_version == 1 and .completed_phases == 16 and .active_logical_streams == 0 and .joined_logical_relays == true' "$destination" >/dev/null && return 0
    fi
    kill -0 -- "-$pid" 2>/dev/null || { echo "process $pid exited before live-post16" >&2; return 1; }
    sleep .05
  done
  echo "timeout waiting for $url live-post16" >&2
  return 1
}

wait_natural_exit() {
  local pid=$1 label=$2
  local attempt status
  for ((attempt=0; attempt<300; attempt++)); do
    kill -0 -- "-$pid" 2>/dev/null || break
    sleep .1
  done
  if kill -0 -- "-$pid" 2>/dev/null; then
    kill -TERM -- "-$pid" 2>/dev/null || true
    for ((attempt=0; attempt<50; attempt++)); do
      kill -0 -- "-$pid" 2>/dev/null || break
      sleep .1
    done
    kill -KILL -- "-$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    unset "owned_groups[$pid]"
    echo "$label required forced shutdown" >&2
    return 1
  fi
  set +e
  wait "$pid"
  status=$?
  set -e
  unset "owned_groups[$pid]"
  ((status == 0)) || { echo "$label exited with status $status" >&2; return 1; }
}

wait_json_file() {
  local path=$1 label=$2
  local attempt
  for ((attempt=0; attempt<100; attempt++)); do
    [[ -s $path ]] && jq -e . "$path" >/dev/null && return 0
    sleep .05
  done
  echo "$label terminal report was not written" >&2
  return 1
}

validate_and_append_cohort() {
  local pair=$1 order_position=$2 variant=$3 initial_server=$4 initial_agent=$5 live_server=$6 live_agent=$7 terminal_server=$8 terminal_agent=$9 phases_dir=${10}
  local validation_output=${11:-$output} validation_snapshots=${12:-$snapshots_output}
  python3 - "$pair" "$order_position" "$variant" "$source_ref" "$server_binary_sha256" "$agent_binary_sha256" "$measured_rtt_ms" "$configured_rtt_ms" "$fixture_smoke" "$expected_payload_sha256" "$build_manifest" \
    "$initial_server" "$initial_agent" "$live_server" "$live_agent" "$terminal_server" "$terminal_agent" "$phases_dir" "$validation_output" "$validation_snapshots" <<'PY'
import ipaddress,json,math,re,sys
from pathlib import Path

(pair,order_position,variant,source_ref,server_sha,agent_sha,measured_rtt,configured_rtt,fixture_smoke,expected_payload_sha,build_manifest_path,
 initial_server_path,initial_agent_path,live_server_path,live_agent_path,
 terminal_server_path,terminal_agent_path,phases_dir,results_path,snapshots_path)=sys.argv[1:]
pair=int(pair); order_position=int(order_position); measured_rtt=float(measured_rtt); configured_rtt=int(configured_rtt); fixture_smoke=fixture_smoke=="1"
phase_dir=Path(phases_dir)
sha=re.compile(r"[0-9a-f]{64}\Z")
expected_arm={(1,1):"aggregate-yamux",(1,2):"raw-tls",(2,1):"raw-tls",(2,2):"aggregate-yamux"}
if (pair,order_position) not in expected_arm or variant!=expected_arm[(pair,order_position)]:
    raise ValueError("pair/order/variant input does not match the predeclared arm order")
if not re.fullmatch(r"[0-9a-f]{40}",source_ref) or not sha.fullmatch(server_sha) or not sha.fullmatch(agent_sha) or not sha.fullmatch(expected_payload_sha):
    raise ValueError("source or binary identity input is invalid")
minimum_rtt,maximum_rtt=((5,25) if fixture_smoke else (60,120))
if configured_rtt!=(10 if fixture_smoke else 80) or not math.isfinite(measured_rtt) or not minimum_rtt <= measured_rtt <= maximum_rtt:
    raise ValueError("measured RTT input is outside the diagnostic envelope")
schedule=[
 ("conditioning-download-lane-1",1,"download","conditioning","fresh-stream"),
 ("conditioning-download-lane-2",2,"download","conditioning","fresh-stream"),
 ("conditioning-download-lane-3",3,"download","conditioning","fresh-stream"),
 ("conditioning-download-lane-4",4,"download","conditioning","fresh-stream"),
 ("conditioning-upload-lane-1",1,"upload","conditioning","fresh-stream"),
 ("conditioning-upload-lane-2",2,"upload","conditioning","fresh-stream"),
 ("conditioning-upload-lane-3",3,"upload","conditioning","fresh-stream"),
 ("conditioning-upload-lane-4",4,"upload","conditioning","fresh-stream"),
 ("retained-download-lane-1",1,"download","retained","new-persistent"),
 ("retained-upload-lane-2",2,"upload","retained","new-persistent"),
 ("timed-1-download-lane-3",3,"download","timed","new-persistent"),
 ("timed-1-upload-lane-4",4,"upload","timed","new-persistent"),
 ("timed-2-upload-lane-1",1,"upload","timed","direction-transition"),
 ("timed-2-download-lane-2",2,"download","timed","direction-transition"),
 ("timed-3-download-lane-3",3,"download","timed","same-direction-reuse"),
 ("timed-3-upload-lane-4",4,"upload","timed","same-direction-reuse"),
]
assert len(schedule)==16

def load(path): return json.loads(Path(path).read_text())
def integer(value,name,minimum=0):
    if isinstance(value,bool) or not isinstance(value,int) or value < minimum: raise ValueError(f"{name} must be integer >= {minimum}")
    return value
def signed_integer(value,name):
    if isinstance(value,bool) or not isinstance(value,int): raise ValueError(f"{name} must be an integer")
    return value
def number(value,name,positive=False):
    if isinstance(value,bool) or not isinstance(value,(int,float)) or not math.isfinite(value) or (positive and value <= 0): raise ValueError(f"{name} invalid")
    return float(value)
def string(value,name,empty_ok=True):
    if not isinstance(value,str) or (not empty_ok and not value): raise ValueError(f"{name} invalid")
    return value
def address(value,name):
    string(value,name,False)
    host,port=value.rsplit(":",1)
    ipaddress.ip_address(host.strip("[]")); integer(int(port),name+" port",1)
    if int(port)>65535: raise ValueError(f"{name} port invalid")

configuration_roles={"server","agent"}
configuration_variants={"aggregate-yamux","raw-tls"}
build_manifest=load(build_manifest_path)
manifest_configuration=build_manifest.get("configuration") if isinstance(build_manifest,dict) else None
if not isinstance(manifest_configuration,dict) or set(manifest_configuration)!=configuration_roles:
    raise ValueError("build manifest configuration roles")
configuration_key_set=None
for manifest_role in sorted(configuration_roles):
    role_configuration=manifest_configuration.get(manifest_role)
    if not isinstance(role_configuration,dict) or set(role_configuration)!=configuration_variants:
        raise ValueError(f"build manifest {manifest_role} configuration variants")
    for manifest_variant in sorted(configuration_variants):
        expected=role_configuration.get(manifest_variant)
        if not isinstance(expected,dict) or not expected:
            raise ValueError(f"build manifest {manifest_role}/{manifest_variant} configuration schema")
        if configuration_key_set is None:
            configuration_key_set=set(expected)
        elif set(expected)!=configuration_key_set:
            raise ValueError("build manifest configuration key sets differ")
        for key,item in expected.items():
            if not isinstance(key,str) or not key:
                raise ValueError("build manifest configuration key invalid")
            if isinstance(item,bool):
                continue
            if isinstance(item,int):
                continue
            if isinstance(item,str):
                continue
            raise ValueError(f"build manifest configuration type invalid: {manifest_role}/{manifest_variant}/{key}")

def configuration(value,role):
    expected=manifest_configuration[role][variant]
    if not isinstance(value,dict) or set(value)!=set(expected):
        raise ValueError(f"{role} configuration schema")
    for key,expected_item in expected.items():
        item=value[key]
        if isinstance(expected_item,bool):
            if not isinstance(item,bool): raise ValueError(f"{role}.configuration.{key} must be boolean")
        elif isinstance(expected_item,int):
            if isinstance(item,bool) or not isinstance(item,int): raise ValueError(f"{role}.configuration.{key} must be integer")
        elif isinstance(expected_item,str):
            if not isinstance(item,str): raise ValueError(f"{role}.configuration.{key} must be string")
        else:
            raise ValueError(f"{role}.configuration.{key} has unsupported manifest type")
    if value!=expected:
        raise ValueError(f"{role} configuration differs from pinned manifest")

socket_keys={"lane","lane_role","local_address","remote_address","connection_identity","tls_version","tls_cipher_suite","connected","tcp_info_captured","tcp_info_error","tcp_state"}
socket_identity_keys=socket_keys-{"connected","tcp_info_captured","tcp_info_error","tcp_state"}
def sockets(rows,role,connected,require_established=False):
    if not isinstance(rows,list) or len(rows)!=5: raise ValueError(f"{role} must have five sockets")
    by_lane={}
    for row in rows:
        if not isinstance(row,dict) or set(row)!=socket_keys: raise ValueError(f"{role} socket schema")
        lane=integer(row["lane"],"lane")
        if lane in by_lane or lane>4: raise ValueError(f"{role} lane invalid")
        expected_role="control" if lane==0 else "data"
        if row["lane_role"]!=expected_role or row["connected"] is not connected: raise ValueError(f"{role} lane state invalid")
        if not isinstance(row["tcp_info_captured"],bool) or not isinstance(row["tcp_info_error"],str): raise ValueError(f"{role} lane TCP observation schema")
        integer(row["tcp_state"],f"{role}.tcp_state")
        if require_established and (row["tcp_info_captured"] is not True or row["tcp_info_error"]!="" or row["tcp_state"]!=1): raise ValueError(f"{role} lane is not observed TCP_ESTABLISHED")
        address(row["local_address"],"local_address"); address(row["remote_address"],"remote_address")
        string(row["connection_identity"],"connection_identity",False)
        integer(row["tls_version"],"tls_version",1); integer(row["tls_cipher_suite"],"tls_cipher_suite",1)
        by_lane[lane]=row
    if set(by_lane)!=set(range(5)): raise ValueError(f"{role} lane set invalid")
    return by_lane

ledger_keys={"limit_bytes","initial_reserved_bytes","growth_reserved_bytes","current_bytes","high_water_bytes","denied_reservations","release_count","final_bytes"}
categories={"session_metadata","stream_metadata","relay_buffers","frame_buffers","tls_app_buffers"}
def ledger(value,name,limit,terminal=False,raw=False,working=False):
    if not isinstance(value,dict): raise ValueError(f"{name} schema")
    required=ledger_keys | ({"categories","actual_heap_alloc_bytes","actual_open_fds","actual_socket_fds"} if working else set())
    if not required <= set(value): raise ValueError(f"{name} missing fields")
    for key in ledger_keys: integer(value[key],f"{name}.{key}")
    if value["limit_bytes"]!=limit or value["denied_reservations"]!=0: raise ValueError(f"{name} limit/denial")
    if not 0 <= value["current_bytes"] <= value["high_water_bytes"] <= value["limit_bytes"]: raise ValueError(f"{name} current/high-water accounting")
    if terminal and (value["current_bytes"]!=0 or value["final_bytes"]!=0): raise ValueError(f"{name} terminal ownership")
    if raw and any(value[key]!=0 for key in ("initial_reserved_bytes","growth_reserved_bytes","current_bytes","high_water_bytes","final_bytes","release_count")): raise ValueError(f"{name} raw ownership")
    if working:
        if value["initial_reserved_bytes"]!=0 or value["growth_reserved_bytes"]!=0: raise ValueError(f"{name} pool counters must remain zero")
        if not isinstance(value["categories"],dict) or set(value["categories"])!=categories: raise ValueError("working categories")
        for key,item in value["categories"].items(): integer(item,f"working.{key}")
        if sum(value["categories"].values())!=value["current_bytes"]: raise ValueError(f"{name} category accounting")
        if terminal and any(value["categories"].values()): raise ValueError(f"{name} terminal categories")
        if terminal and value["release_count"]<=0: raise ValueError(f"{name} terminal release count")
        integer(value["actual_heap_alloc_bytes"],f"{name}.actual_heap_alloc_bytes",1)
        integer(value["actual_open_fds"],f"{name}.actual_open_fds",1)
        integer(value["actual_socket_fds"],f"{name}.actual_socket_fds",0 if terminal else 1)
    elif not raw:
        reserved=value["initial_reserved_bytes"]+value["growth_reserved_bytes"]
        if value["high_water_bytes"]!=reserved: raise ValueError(f"{name} reservation high-water accounting")
        if value["initial_reserved_bytes"]<=0: raise ValueError(f"{name} missing initial pool reservation")
        if terminal:
            if value["release_count"]<4: raise ValueError(f"{name} terminal release count")
        elif value["current_bytes"]!=reserved or value["release_count"]!=0:
            raise ValueError(f"{name} live reservation accounting/release")

def base(value,role,completed,terminal=False,require_source=False):
    if not isinstance(value,dict): raise ValueError(f"{role} top schema")
    if integer(value.get("schema_version"),f"{role}.schema_version")!=1 or value.get("process_role")!=role or value.get("variant")!=variant: raise ValueError(f"{role} top identity")
    if require_source and value.get("source_ref")!=source_ref: raise ValueError(f"{role} source_ref")
    if integer(value.get("expected_phases"),f"{role}.expected_phases")!=16 or integer(value.get("completed_phases"),f"{role}.completed_phases")!=completed: raise ValueError(f"{role} phase count")
    if value.get("invalid_reason")!="": raise ValueError(f"{role} invalid_reason")
    configuration(value.get("configuration"),role)
    ledger(value.get("pool_ledger"),f"{role}.pool",2147483648,terminal=terminal,raw=variant=="raw-tls")
    ledger(value.get("working_ledger"),f"{role}.working",67108864,terminal=terminal,working=True)
    if variant=="aggregate-yamux" and completed==16 and not fixture_smoke and value["pool_ledger"]["growth_reserved_bytes"]<=0:
        raise ValueError(f"{role} aggregate pool never grew")

def readiness(value,role):
    base(value,role,0,require_source=True)
    if value.get("ready") is not True or integer(value.get("reconnect_count"),f"{role}.reconnect_count")!=0 or value.get("errors")!=[]:
        raise ValueError(f"{role} not cleanly ready")
    return sockets(value.get("physical_sockets"),role,True,True)

def reverse(server,agent):
    for lane in range(5):
        s,a=server[lane],agent[lane]
        if (s["local_address"]!=a["remote_address"] or s["remote_address"]!=a["local_address"] or
            s["tls_version"]!=a["tls_version"] or s["tls_cipher_suite"]!=a["tls_cipher_suite"]):
            raise ValueError(f"lane {lane} reverse tuple/TLS mismatch")

tcp_keys={"captured","error","local_address","remote_address","state","ca_state","retransmits","probes","backoff","options","rto_us","ato_us","snd_mss","rcv_mss","unacked","sacked","lost","retrans","fackets","last_data_sent_ms","last_ack_sent_ms","last_data_recv_ms","last_ack_recv_ms","pmtu","rcv_ssthresh","rtt_us","rttvar_us","snd_ssthresh","snd_cwnd","advmss","reordering","rcv_rtt_us","rcv_space","total_retrans","pacing_rate","delivery_rate","bytes_acked","bytes_received","bytes_sent","notsent_bytes","snd_wnd","rcv_wnd","app_limited_available"}
def tcp(value,name,socket):
    if not isinstance(value,dict) or set(value)!=tcp_keys or value["captured"] is not True or value["error"]!="": raise ValueError(f"{name} TCP_INFO")
    if value["app_limited_available"] is not False: raise ValueError(f"{name} unavailable app_limited must remain false")
    if value["local_address"]!=socket["local_address"] or value["remote_address"]!=socket["remote_address"]: raise ValueError(f"{name} TCP tuple")
    for key in tcp_keys-{"captured","error","local_address","remote_address","app_limited_available"}: integer(value[key],f"{name}.{key}")

phase_keys={"schema_version","phase_index","phase_name","lane","direction","stage","reuse_kind","configured_payload_bytes","received_payload_bytes","expected_sha256","received_sha256","phase_wall_nanoseconds","setup_nanoseconds","payload_completion_nanoseconds","useful_bytes_per_second","tcp_info"}
def phase(value,index,role,socket):
    if not isinstance(value,dict) or set(value)!=phase_keys: raise ValueError(f"{role} phase {index} schema")
    if integer(value["schema_version"],"schema_version")!=1 or integer(value["phase_index"],"phase_index")!=index: raise ValueError(f"{role} phase {index} identity")
    phase_name,lane,direction,stage,reuse=schedule[index]
    integer(value["lane"],"lane",1)
    if (value["phase_name"],value["lane"],value["direction"],value["stage"],value["reuse_kind"])!=(phase_name,lane,direction,stage,reuse): raise ValueError(f"{role} phase {index} schedule")
    if integer(value["configured_payload_bytes"],"configured_payload_bytes")!=268435456 or integer(value["received_payload_bytes"],"received_payload_bytes")!=268435456: raise ValueError(f"{role} phase {index} bytes")
    if not isinstance(value["expected_sha256"],str) or not isinstance(value["received_sha256"],str) or value["expected_sha256"]!=expected_payload_sha or value["received_sha256"]!=expected_payload_sha: raise ValueError(f"{role} phase {index} predetermined digest")
    wall=integer(value["phase_wall_nanoseconds"],"phase_wall",1)
    setup=integer(value["setup_nanoseconds"],"setup")
    payload=integer(value["payload_completion_nanoseconds"],"payload",1)
    if setup+payload!=wall: raise ValueError(f"{role} phase {index} timing boundary")
    rate=number(value["useful_bytes_per_second"],"rate",True)
    expected=268435456*1e9/wall
    if abs(rate-expected)>max(1e-6*expected,1.0): raise ValueError(f"{role} phase {index} total-wall rate")
    tcp(value["tcp_info"],f"{role} phase {index}",socket[lane])
    return value

initial_server=load(initial_server_path); initial_agent=load(initial_agent_path)
server_initial=readiness(initial_server,"server"); agent_initial=readiness(initial_agent,"agent"); reverse(server_initial,agent_initial)

server_responses=[load(phase_dir/f"phase-{i:02d}.json") for i in range(16)]
live_server=load(live_server_path); live_agent=load(live_agent_path)
for value,role,initial in ((live_server,"server",server_initial),(live_agent,"agent",agent_initial)):
    base(value,role,16,require_source=True)
    if integer(value.get("reconnect_count"),f"{role}.reconnect_count")!=0 or integer(value.get("active_logical_streams"),f"{role}.active_logical_streams")!=0: raise ValueError(f"{role} live counters")
    if value.get("ready") is not True or value.get("graceful_shutdown") is not False or value.get("same_socket_identities") is not True or value.get("joined_logical_relays") is not True or value.get("joined_physical_relays") is not False or value.get("errors")!=[]: raise ValueError(f"{role} live state")
    first=sockets(value.get("physical_sockets_initial"),role,True,True); current=sockets(value.get("physical_sockets_terminal"),role,True,True)
    for lane in range(5):
        if any(first[lane][k]!=initial[lane][k] or current[lane][k]!=initial[lane][k] for k in socket_identity_keys): raise ValueError(f"{role} live lane {lane} identity")
reverse(sockets(live_server["physical_sockets_terminal"],"server live",True,True),sockets(live_agent["physical_sockets_terminal"],"agent live",True,True))
server_live_results=live_server.get("results"); agent_live_results=live_agent.get("results")
if not isinstance(server_live_results,list) or not isinstance(agent_live_results,list) or len(server_live_results)!=16 or len(agent_live_results)!=16: raise ValueError("live results count")

terminal_server=load(terminal_server_path); terminal_agent=load(terminal_agent_path)
for value,role,initial,live_results in ((terminal_server,"server",server_initial,server_live_results),(terminal_agent,"agent",agent_initial,agent_live_results)):
    base(value,role,16,terminal=True,require_source=True)
    if integer(value.get("reconnect_count"),f"{role}.reconnect_count")!=0 or integer(value.get("active_logical_streams"),f"{role}.active_logical_streams")!=0: raise ValueError(f"{role} terminal counters")
    if value.get("ready") is not False or value.get("graceful_shutdown") is not True or value.get("same_socket_identities") is not True or value.get("joined_logical_relays") is not True or value.get("joined_physical_relays") is not True or value.get("errors")!=[]: raise ValueError(f"{role} terminal state")
    first=sockets(value.get("physical_sockets_initial"),role,True,True); final=sockets(value.get("physical_sockets_terminal"),role,False)
    for lane in range(5):
        a,b=initial[lane],final[lane]
        if any(first[lane][k]!=a[k] or a[k]!=b[k] for k in socket_identity_keys): raise ValueError(f"{role} terminal lane {lane} identity")
    if value.get("results")!=live_results: raise ValueError(f"{role} terminal results changed")

joined=[]
for index in range(16):
    sr=phase(server_live_results[index],index,"server",server_initial)
    ar=phase(agent_live_results[index],index,"agent",agent_initial)
    if server_responses[index]!=sr: raise ValueError(f"server phase {index} response changed")
    without_tcp=lambda value:{k:v for k,v in value.items() if k!="tcp_info"}
    if without_tcp(sr)!=without_tcp(ar): raise ValueError(f"phase {index} role mismatch")
    row=without_tcp(sr)
    row.update({"pair":pair,"order_position":int(order_position),"variant":variant,"source_ref":source_ref,
                "server_binary_sha256":server_sha,"agent_binary_sha256":agent_sha,"configured_rtt_ms":configured_rtt,
                "measured_rtt_ms":measured_rtt,"server_tcp_info":sr["tcp_info"],"agent_tcp_info":ar["tcp_info"]})
    joined.append(row)

with Path(results_path).open("a") as out:
    for row in joined: out.write(json.dumps(row,separators=(",",":"),sort_keys=True)+"\n")
with Path(snapshots_path).open("a") as out:
    for stage,server,agent in (("initial",initial_server,initial_agent),("live-post16",live_server,live_agent),("terminal",terminal_server,terminal_agent)):
        out.write(json.dumps({"pair":pair,"variant":variant,"stage":stage,"server":server,"agent":agent},separators=(",",":"),sort_keys=True)+"\n")
PY
}

run_cohort() {
  local pair=$1 order_position=$2 variant=$3
  local prefix=$run_dir/pair-$pair-$variant
  local server_terminal=$prefix-server-terminal.json agent_terminal=$prefix-agent-terminal.json
  local server_log=$prefix-server.log agent_log=$prefix-agent.log
  local phases_dir=$prefix-phases
  mkdir -p "$phases_dir"
  start_topology
  printf '%s\n' "$measured_rtt_ms" > "$prefix-measured-rtt-ms.txt"
  capture_topology "$pair" "$order_position" "$variant" "$prefix-topology.json"

  setsid nsenter -t "$server_namespace_pid" -n env GOMAXPROCS=4 P2PSTREAM_TRANSPORT_CEILING_EXTERNAL=1 \
    P2PSTREAM_TRANSPORT_CEILING_SOURCE_REF="$source_ref" taskset -c "$server_cpu_set" "$server_binary" \
    -test.run '^TestTransportCeilingExternalServerRole$' -test.timeout 600s \
    --transport-ceiling-variant="$variant" \
    --transport-ceiling-listen="$server_address" \
    --transport-ceiling-admin=127.0.0.1:18080 \
    --transport-ceiling-cert="$cert" --transport-ceiling-key="$key" \
    --transport-ceiling-auth-key-file="$auth_key" \
    --transport-ceiling-output="$server_terminal" >"$server_log" 2>&1 &
  local server_pid=$!
  owned_groups[$server_pid]=1

  setsid nsenter -t "$agent_namespace_pid" -n env GOMAXPROCS=4 P2PSTREAM_TRANSPORT_CEILING_EXTERNAL=1 \
    P2PSTREAM_TRANSPORT_CEILING_SOURCE_REF="$source_ref" taskset -c "$agent_cpu_set" "$agent_binary" \
    -test.run '^TestTransportCeilingExternalAgentRole$' -test.timeout 600s \
    --transport-ceiling-variant="$variant" \
    --transport-ceiling-server="$server_address" \
    --transport-ceiling-admin=127.0.0.1:18080 \
    --transport-ceiling-ca="$cert" \
    --transport-ceiling-server-name=transport-ceiling.test \
    --transport-ceiling-auth-key-file="$auth_key" \
    --transport-ceiling-output="$agent_terminal" >"$agent_log" 2>&1 &
  local agent_pid=$!
  owned_groups[$agent_pid]=1

  local initial_server=$prefix-server-initial.json initial_agent=$prefix-agent-initial.json
  wait_admin_ready "$server_namespace_pid" "$server_admin" "$server_pid" "$initial_server"
  wait_admin_ready "$agent_namespace_pid" "$agent_admin" "$agent_pid" "$initial_agent"

  local phase body
  for ((phase=0; phase<phase_count; phase++)); do
    body=$(printf '{"schema_version":1,"phase_index":%d}' "$phase")
    http_json "$server_namespace_pid" POST "$server_admin/transport-ceiling/transfer" "$body" > "$phases_dir/phase-$(printf '%02d' "$phase").json"
    jq -e --argjson phase "$phase" --arg digest "$expected_payload_sha256" '.schema_version == 1 and .phase_index == $phase and .configured_payload_bytes == 268435456 and .received_payload_bytes == 268435456 and .expected_sha256 == $digest and .received_sha256 == $digest' "$phases_dir/phase-$(printf '%02d' "$phase").json" >/dev/null
  done

  local live_server=$prefix-server-live-post16.json live_agent=$prefix-agent-live-post16.json
  wait_live_post16 "$server_namespace_pid" "$server_admin" "$server_pid" "$live_server"
  wait_live_post16 "$agent_namespace_pid" "$agent_admin" "$agent_pid" "$live_agent"

  http_json "$server_namespace_pid" POST "$server_admin/transport-ceiling/shutdown" '{"schema_version":1}' > "$prefix-shutdown-response.json"
  jq -e 'type == "object" and keys == ["schema_version"] and .schema_version == 1' "$prefix-shutdown-response.json" >/dev/null
  wait_natural_exit "$server_pid" "$variant server"
  wait_natural_exit "$agent_pid" "$variant agent"
  wait_json_file "$server_terminal" "$variant server"
  wait_json_file "$agent_terminal" "$variant agent"
  validate_and_append_cohort "$pair" "$order_position" "$variant" "$initial_server" "$initial_agent" "$live_server" "$live_agent" "$server_terminal" "$agent_terminal" "$phases_dir"
  stop_topology
}

for ((pair=1; pair<=pair_count; pair++)); do
  if ((pair == 1)); then variants=(aggregate-yamux raw-tls); else variants=(raw-tls aggregate-yamux); fi
  for order_position in 1 2; do
    run_cohort "$pair" "$order_position" "${variants[$((order_position-1))]}"
  done
done

# Re-run the same pure validators from the immutable raw phase/snapshot/report
# files. The final classification is allowed only when this independent pass is
# byte-identical to the incrementally appended evidence.
revalidated_output=$run_dir/revalidated-results.jsonl
revalidated_snapshots=$run_dir/revalidated-snapshots.jsonl
: > "$revalidated_output"
: > "$revalidated_snapshots"
revalidation_ok=1
for ((pair=1; pair<=pair_count; pair++)); do
  if ((pair == 1)); then variants=(aggregate-yamux raw-tls); else variants=(raw-tls aggregate-yamux); fi
  for order_position in 1 2; do
    variant=${variants[$((order_position-1))]}
    prefix=$run_dir/pair-$pair-$variant
    measured_rtt_ms=$(<"$prefix-measured-rtt-ms.txt")
    set +e
    validate_and_append_cohort "$pair" "$order_position" "$variant" \
      "$prefix-server-initial.json" "$prefix-agent-initial.json" \
      "$prefix-server-live-post16.json" "$prefix-agent-live-post16.json" \
      "$prefix-server-terminal.json" "$prefix-agent-terminal.json" \
      "$prefix-phases" "$revalidated_output" "$revalidated_snapshots"
    validation_status=$?
    set -e
    ((validation_status == 0)) || revalidation_ok=0
  done
done
cmp -s "$output" "$revalidated_output" || revalidation_ok=0
cmp -s "$snapshots_output" "$revalidated_snapshots" || revalidation_ok=0

summary=${output%.jsonl}.summary.json
set +e
python3 - "$output" "$snapshots_output" "$topologies_output" "$summary" "$runner_sha256" "$server_binary_sha256" "$agent_binary_sha256" "$source_ref" "$expected_payload_sha256" "$revalidation_ok" "$fixture_smoke" "$configured_rtt_ms" "$one_way_netem_ms" "$pair_count" <<'PY'
import json,re,statistics,sys
from pathlib import Path
source,snap_source,topology_source,destination,runner_sha,server_sha,agent_sha,source_ref,expected_payload_sha,revalidation_ok,fixture_smoke,configured_rtt,one_way_netem,pair_count=sys.argv[1:]
fixture_smoke=fixture_smoke=="1"; configured_rtt=int(configured_rtt); one_way_netem=int(one_way_netem); pair_count=int(pair_count)
rows=[json.loads(x) for x in Path(source).read_text().splitlines() if x.strip()]
snapshots=[json.loads(x) for x in Path(snap_source).read_text().splitlines() if x.strip()]
topologies=[json.loads(x) for x in Path(topology_source).read_text().splitlines() if x.strip()]
all_order={1:["aggregate-yamux","raw-tls"],2:["raw-tls","aggregate-yamux"]}
expected_order={pair:all_order[pair] for pair in range(1,pair_count+1)}
pairs=tuple(range(1,pair_count+1))
expected_rows=pair_count*32; expected_snapshots=pair_count*6; expected_topologies=pair_count*2; expected_namespaces=pair_count*6
minimum_rtt,maximum_rtt=((5,25) if fixture_smoke else (60,120))
non_timing=[]
timing_failures=[]
if pair_count!=(1 if fixture_smoke else 2) or configured_rtt!=(10 if fixture_smoke else 80) or one_way_netem!=(5 if fixture_smoke else 40):
    non_timing.append("mode-specific pair count or RTT configuration is invalid")
if revalidation_ok!="1": non_timing.append("raw evidence failed pure-validator replay or byte-identity comparison")
if len(rows)!=expected_rows: non_timing.append(f"expected {expected_rows} rows, found {len(rows)}")
if len(snapshots)!=expected_snapshots: non_timing.append(f"expected {expected_snapshots} snapshots, found {len(snapshots)}")
if len(topologies)!=expected_topologies: non_timing.append(f"expected {expected_topologies} topology records, found {len(topologies)}")
row_shape_ok=all(isinstance(r,dict) and all(k in r for k in ("pair","order_position","variant","phase_index","measured_rtt_ms","useful_bytes_per_second","phase_name","lane","direction","stage","reuse_kind")) for r in rows)
snapshot_shape_ok=all(isinstance(s,dict) and all(k in s for k in ("pair","variant","stage","server","agent")) for s in snapshots)
if not row_shape_ok: non_timing.append("one or more result rows are not objects with the required final-analysis fields")
if not snapshot_shape_ok: non_timing.append("one or more snapshots are not objects with the required final-analysis fields")
expected_sequence=[]
for pair in pairs:
    for position,variant in enumerate(expected_order[pair],1):
        expected_sequence.extend((pair,position,variant,phase) for phase in range(16))
actual_sequence=[(r.get("pair"),r.get("order_position"),r.get("variant"),r.get("phase_index")) if isinstance(r,dict) else None for r in rows]
if actual_sequence!=expected_sequence: non_timing.append(f"{expected_rows}-row chronological pair/arm/phase sequence mismatch")
expected_snapshot_sequence=[]
for pair in pairs:
    for variant in expected_order[pair]:
        expected_snapshot_sequence.extend((pair,variant,stage) for stage in ("initial","live-post16","terminal"))
actual_snapshot_sequence=[(s.get("pair"),s.get("variant"),s.get("stage")) if isinstance(s,dict) else None for s in snapshots]
if actual_snapshot_sequence!=expected_snapshot_sequence: non_timing.append(f"{expected_snapshots}-snapshot chronological pair/arm/stage sequence mismatch")
expected_topology_sequence=[(pair,position,variant) for pair in pairs for position,variant in enumerate(expected_order[pair],1)]
actual_topology_sequence=[(t.get("pair"),t.get("order_position"),t.get("variant")) if isinstance(t,dict) else None for t in topologies]
if actual_topology_sequence!=expected_topology_sequence: non_timing.append(f"{expected_topologies}-topology chronological pair/arm sequence mismatch")
namespace_ids=[]
cpu_sets_seen=[]
topology_rtts={}
for topology in topologies:
    if not isinstance(topology,dict) or topology.get("schema_version")!=1 or not isinstance(topology.get("namespace_identities"),dict):
        non_timing.append("malformed topology record")
        continue
    identities=topology["namespace_identities"]
    if set(identities)!={"server","router","agent"} or not all(isinstance(v,str) and re.fullmatch(r"net:\[[0-9]+\]",v) for v in identities.values()):
        non_timing.append("invalid topology namespace identities")
    namespace_ids.extend(identities.values())
    configured=topology.get("configured")
    if not isinstance(configured,dict) or configured!={"rtt_ms":configured_rtt,"one_way_netem_ms":one_way_netem,"rate_bits_per_second":1000000000,"server_tunnel":"10.211.0.1:17443","server_admin":"127.0.0.1:18080","agent_admin":"127.0.0.1:18080"}:
        non_timing.append("topology configuration differs from the predeclared fixture")
    if not isinstance(topology.get("network_state"),dict) or set(topology["network_state"])!={"server","router","agent"}:
        non_timing.append("topology network-state evidence missing")
    cpu_sets=topology.get("cpu_sets")
    if not isinstance(cpu_sets,dict) or set(cpu_sets)!={"server","agent","load"} or not all(isinstance(v,str) and v for v in cpu_sets.values()):
        non_timing.append("topology CPU-set evidence missing")
    else:
        cpu_sets_seen.append(cpu_sets)
    topology_rtt=topology.get("measured_rtt_ms")
    if isinstance(topology_rtt,bool) or not isinstance(topology_rtt,(int,float)) or not minimum_rtt <= topology_rtt <= maximum_rtt:
        non_timing.append("topology measured RTT is outside the diagnostic envelope")
    else:
        topology_rtts[(topology.get("pair"),topology.get("variant"))]=topology_rtt
if len(namespace_ids)!=expected_namespaces or len(set(namespace_ids))!=expected_namespaces: non_timing.append(f"fresh server/router/agent network namespace identities were not unique for all {expected_topologies} arms")
if len(cpu_sets_seen)!=expected_topologies or any(value!=cpu_sets_seen[0] for value in cpu_sets_seen[1:]): non_timing.append("endpoint/load CPU sets changed between arms")
by_pair={}
rtt_by_pair_arm={}
for pair in pairs:
    by_pair[pair]={}
    rtt_by_pair_arm[str(pair)]={}
    if revalidation_ok!="1" or not row_shape_ok or actual_sequence!=expected_sequence:
        continue
    pair_rows=[r for r in rows if r.get("pair")==pair]
    observed=[]
    for variant in expected_order[pair]:
        selected=sorted((r for r in pair_rows if r.get("variant")==variant),key=lambda r:r["phase_index"])
        if len(selected)!=16 or [r["phase_index"] for r in selected]!=list(range(16)):
            non_timing.append(f"pair {pair} {variant} phase order invalid")
            continue
        expected_position=expected_order[pair].index(variant)+1
        if any(r.get("order_position")!=expected_position for r in selected):
            non_timing.append(f"pair {pair} {variant} arm order invalid")
            continue
        observed.append(variant)
        by_pair[pair][variant]=selected
        rtts={r.get("measured_rtt_ms") for r in selected}
        rtt=next(iter(rtts)) if len(rtts)==1 else None
        if isinstance(rtt,bool) or not isinstance(rtt,(int,float)) or not minimum_rtt <= rtt <= maximum_rtt:
            non_timing.append(f"pair {pair} {variant} measured RTT invalid or inconsistent")
        else:
            rtt_by_pair_arm[str(pair)][variant]=rtt
            if topology_rtts.get((pair,variant))!=rtt: non_timing.append(f"pair {pair} {variant} row/topology RTT mismatch")
    if observed!=expected_order[pair]: non_timing.append(f"pair {pair} variant order invalid")

pair_results={}
for pair in pairs:
    if set(by_pair[pair])!={"aggregate-yamux","raw-tls"}: continue
    yamux=by_pair[pair]["aggregate-yamux"]; raw=by_pair[pair]["raw-tls"]
    y_warm=statistics.median(yamux[i]["useful_bytes_per_second"] for i in range(4))
    y_transition={str(i):yamux[i]["useful_bytes_per_second"] for i in (8,10,13)}
    y_reused=yamux[14]["useful_bytes_per_second"]
    yamux_gate={"fresh_download_median":y_warm,"fresh_download_minimum":29700000,
                "fresh_download_passed":y_warm>=29700000,
                "transition_samples":y_transition,"at_least_one_transition_below_floor":any(v<29700000 for v in y_transition.values()),
                "reused_lane3_download":y_reused,"reused_lane3_minimum":29700000,"reused_lane3_passed":y_reused>=29700000}
    yamux_gate["passed"]=yamux_gate["fresh_download_passed"] and yamux_gate["at_least_one_transition_below_floor"] and yamux_gate["reused_lane3_passed"]
    r_warm=statistics.median(raw[i]["useful_bytes_per_second"] for i in range(4))
    raw_ratios={str(i):raw[i]["useful_bytes_per_second"]/r_warm for i in (8,10,13,14)}
    raw_gate={"fresh_download_median":r_warm,"postwarm_to_fresh_median_ratios":raw_ratios,"minimum":.90,"passed":all(v>=.90 for v in raw_ratios.values())}
    phase_table={}
    for variant,selected in (("aggregate-yamux",yamux),("raw-tls",raw)):
        phase_table[variant]=[{"phase_index":r["phase_index"],"phase_name":r["phase_name"],"lane":r["lane"],
                              "direction":r["direction"],"stage":r["stage"],"reuse_kind":r["reuse_kind"],
                              "useful_bytes_per_second":r["useful_bytes_per_second"]} for r in selected]
    pair_results[str(pair)]={"order":expected_order[pair],"all_phase_rates":phase_table}
    if fixture_smoke:
        pair_results[str(pair)]["timing_localization_gates_evaluated"]=False
    else:
        pair_results[str(pair)].update({"yamux_stall_signature":yamux_gate,"raw_no_analogous_stall":raw_gate})
        if not yamux_gate["passed"]: timing_failures.append(f"pair {pair} did not reproduce the Yamux transition signature")
        if not raw_gate["passed"]: timing_failures.append(f"pair {pair} raw TLS had a postwarm download below 90% of its own warm median")

growth_by_pair={}
for pair in pairs:
    selected=[s for s in snapshots if isinstance(s,dict) and s.get("pair")==pair and s.get("variant")=="aggregate-yamux" and s.get("stage")=="live-post16"]
    if len(selected)==1:
        growth_by_pair[str(pair)]={role:selected[0].get(role,{}).get("pool_ledger",{}).get("growth_reserved_bytes") for role in ("server","agent")}
growth_exercised=bool(growth_by_pair) and all(isinstance(value,int) and not isinstance(value,bool) and value>0 for values in growth_by_pair.values() for value in values.values())
failures=non_timing+timing_failures
diagnostic_reproduced=not failures and not fixture_smoke
smoke_passed=fixture_smoke and not non_timing
classification=("fixture_smoke_pass" if smoke_passed else "inconclusive_non_timing_failure") if fixture_smoke else ("transport_ceiling_contrast_reproduced" if diagnostic_reproduced else ("complete_timing_rejection" if not non_timing else "inconclusive_non_timing_failure"))
summary={"scope":"fixture_smoke_only" if fixture_smoke else "diagnostic_only","classification":classification,
         "diagnostic_reproduced":diagnostic_reproduced,"fixture_smoke_passed":smoke_passed,"row_count":len(rows),"snapshot_count":len(snapshots),
         "topology_count":len(topologies),"fresh_namespace_identity_count":len(set(namespace_ids)),
         "fixed_workload":{"pairs":pair_count,"order":["yamux-then-raw"] if fixture_smoke else ["yamux-then-raw","raw-then-yamux"],"transfers_per_variant":16,"total_transfers":expected_rows,"payload_bytes":268435456,"expected_payload_sha256":expected_payload_sha,"configured_rtt_ms":configured_rtt,"configured_one_way_netem_ms":one_way_netem,"measured_rtt_envelope_ms":{"minimum":minimum_rtt,"maximum":maximum_rtt},"measured_rtt_ms_by_pair_arm":rtt_by_pair_arm,
                           "total_phase_wall_used_for_both_arms":True,"tcp_info_collection":"post-transfer-only",
                           "tcp_info_unavailable_fields":["app_limited"],
                           "raw_lifecycle_difference":"persistent TLS and origin TCP relay per data lane; no Yamux Open/OpenRequest setup"},
         "gates":{"timing_localization_evaluated":not fixture_smoke,"yamux_fresh_download_median_minimum":None if fixture_smoke else 29700000,"yamux_transition_requires_at_least_one_subthreshold":None if fixture_smoke else True,"yamux_reused_lane3_minimum":None if fixture_smoke else 29700000,"raw_each_postwarm_to_own_warm_median_minimum":None if fixture_smoke else .90,"not_a_1_10_retention_or_legacy_acceptance_gate":True},
         "aggregate_growth":{"exercised":growth_exercised,"status":"exercised" if growth_exercised else ("untested_smoke_only" if fixture_smoke else "missing"),"live_growth_reserved_bytes_by_pair_role":growth_by_pair},
         "pairs":pair_results,"failures":failures,"non_timing_failures":non_timing,"timing_failures":timing_failures,"pure_validator_replay":revalidation_ok=="1",
         "provenance":{"runner_sha256":runner_sha,"server_binary_sha256":server_sha,"agent_binary_sha256":agent_sha,"source_ref":source_ref,"build_tag":"p2pstream_transport_ceiling"}}
Path(destination).write_text(json.dumps(summary,indent=2,sort_keys=True)+"\n")
raise SystemExit(0 if (smoke_passed if fixture_smoke else diagnostic_reproduced) else (2 if non_timing else 1))
PY

exit_status=$?
set -e
exit "$exit_status"

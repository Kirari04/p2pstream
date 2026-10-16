#!/usr/bin/env bash
set -Eeuo pipefail
[[ ${P2PSTREAM_TRANSPORT_CEILING_INNER_FIXTURE:-} == 1 && $$ -eq 1 ]] || { echo "owned inner PID/network namespace required" >&2; exit 2; }
mapfile -t initial_interfaces < <(ip -o link show | awk -F': ' '{sub(/@.*/,"",$2); print $2}')
[[ ${#initial_interfaces[@]} -eq 1 && ${initial_interfaces[0]} == lo && -z $(ip route show) && -z $(ip -6 route show) ]] || { echo "unclean initial network namespace" >&2; exit 2; }
overall_timeout_seconds=120
configured_rtt_ms=80
one_way_netem_ms=40
run_dir=$1
mkdir -p "$run_dir"
topologies_output=$run_dir/topologies.jsonl
: > "$topologies_output"
declare -A owned_groups=()
declare -A seen_helper_process_instances=()
topology_generation=0
last_joined_generation=0
boot_id=$(tr -d '\n' < /proc/sys/kernel/random/boot_id)
runner_namespace_identity=$(readlink /proc/self/ns/net)
[[ $boot_id =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ && $runner_namespace_identity =~ ^net:\[[0-9]+\]$ ]] || {
  echo "kernel boot/network namespace identity is unavailable" >&2
  exit 2
}
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

process_start_state() {
  python3 - "$1" <<'PY'
import sys
from pathlib import Path
text=Path(f"/proc/{int(sys.argv[1])}/stat").read_text()
right=text.rfind(")")
if right < 0:
    raise SystemExit("malformed /proc PID stat")
fields=text[right+2:].split()
if len(fields) < 20:
    raise SystemExit("short /proc PID stat")
print(int(fields[19]),fields[0])
PY
}

process_start_ticks() {
  local start state
  read -r start state < <(process_start_state "$1") || return 1
  printf '%s\n' "$start"
}

capture_namespace_instances() {
  python3 - "$boot_id" "$runner_namespace_identity" "$topology_generation" \
    server "$server_namespace_pid" router "$router_namespace_pid" agent "$agent_namespace_pid" <<'PY'
import json,os,re,sys
boot_id,runner_identity,generation,*items=sys.argv[1:]
generation=int(generation)
if len(items)!=6 or generation<1:
    raise SystemExit("invalid namespace instance inputs")
def capture(role,pid_text):
    pid=int(pid_text)
    def sample():
        text=open(f"/proc/{pid}/stat").read()
        right=text.rfind(")")
        if right<0: raise SystemExit(f"malformed stat for {role}")
        fields=text[right+2:].split()
        if len(fields)<20: raise SystemExit(f"short stat for {role}")
        start_ticks=int(fields[19])
        identity=os.readlink(f"/proc/{pid}/ns/net")
        match=re.fullmatch(r"net:\[([0-9]+)\]",identity)
        if not match: raise SystemExit(f"invalid netns identity for {role}")
        return start_ticks,identity,int(match.group(1))
    first=sample(); second=sample()
    if first!=second: raise SystemExit(f"changing process/netns identity for {role}")
    start_ticks,identity,inode=first
    if pid<=1 or start_ticks<=0 or identity==runner_identity:
        raise SystemExit(f"invalid helper identity for {role}")
    return identity,{"boot_id":boot_id,"helper_pid":pid,"helper_start_ticks":start_ticks,
                     "netns_inode":inode,"arm_generation":generation}
rows={}; identities={}
for index in range(0,len(items),2):
    role=items[index]
    identity,row=capture(role,items[index+1])
    identities[role]=identity; rows[role]=row
if set(rows)!={"server","router","agent"} or len({row["netns_inode"] for row in rows.values()})!=3:
    raise SystemExit("within-arm network namespaces are aliased")
if len({(row["boot_id"],row["helper_pid"],row["helper_start_ticks"]) for row in rows.values()})!=3:
    raise SystemExit("within-arm helper process instances are aliased")
print(json.dumps({"identities":identities,"instances":rows},sort_keys=True,separators=(",",":")))
PY
}

start_topology() {
  local pid attempt iface key
  ((last_joined_generation == topology_generation && ${#owned_groups[@]} == 0)) || {
    echo "prior topology helpers were not fully joined" >&2
    return 1
  }
  ((topology_generation+=1))
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
      [[ -e /proc/$pid/ns/net && $(readlink "/proc/$pid/ns/net") != "$runner_namespace_identity" ]] && break
      sleep .01
    done
    [[ -e /proc/$pid/ns/net && $(readlink "/proc/$pid/ns/net") != "$runner_namespace_identity" ]]
  done
  namespace_capture_before=$(capture_namespace_instances)
  namespace_instances_json=$(jq -c .instances <<<"$namespace_capture_before")
  server_namespace_identity=$(jq -r .identities.server <<<"$namespace_capture_before")
  router_namespace_identity=$(jq -r .identities.router <<<"$namespace_capture_before")
  agent_namespace_identity=$(jq -r .identities.agent <<<"$namespace_capture_before")
  while IFS=$'\t' read -r role instance_boot instance_pid instance_start instance_inode instance_generation; do
    key="$instance_boot:$instance_pid:$instance_start"
    [[ $instance_generation == "$topology_generation" && -z ${seen_helper_process_instances[$key]+x} ]] || {
      echo "network namespace helper process instance $key is invalid or reused" >&2
      return 1
    }
    seen_helper_process_instances[$key]=1
  done < <(jq -r '.instances|to_entries[]|[.key,.value.boot_id,.value.helper_pid,.value.helper_start_ticks,.value.netns_inode,.value.arm_generation]|@tsv' <<<"$namespace_capture_before")

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
  namespace_capture_after=$(capture_namespace_instances)
  [[ $namespace_capture_before == "$namespace_capture_after" ]] || {
    echo "network namespace helper identity changed during topology setup" >&2
    return 1
  }
}

capture_topology() {
  local pair=$1 order_position=$2 variant=$3 destination=$4 capture_before capture_after
  local server_addresses server_routes router_addresses router_routes router_qdisc agent_addresses agent_routes
  capture_before=$(capture_namespace_instances)
  [[ $capture_before == "$namespace_capture_before" ]] || { echo "network namespace helper identity changed before topology capture" >&2; return 1; }
  server_addresses=$(server_ns ip -j address show)
  server_routes=$(server_ns ip -j route show)
  router_addresses=$(router_ns ip -j address show)
  router_routes=$(router_ns ip -j route show)
  router_qdisc=$(router_ns tc -j qdisc show)
  agent_addresses=$(agent_ns ip -j address show)
  agent_routes=$(agent_ns ip -j route show)
  capture_after=$(capture_namespace_instances)
  [[ $capture_before == "$capture_after" ]] || { echo "network namespace helper identity changed during topology capture" >&2; return 1; }
  jq -n \
    --argjson pair "$pair" --argjson order_position "$order_position" --arg variant "$variant" \
    --arg server_namespace_identity "$server_namespace_identity" \
    --arg router_namespace_identity "$router_namespace_identity" \
    --arg agent_namespace_identity "$agent_namespace_identity" \
    --arg runner_namespace_identity "$runner_namespace_identity" \
    --argjson namespace_instances "$namespace_instances_json" \
    --argjson prior_joined_generation "$last_joined_generation" \
    --arg server_cpu_set "$server_cpu_set" --arg agent_cpu_set "$agent_cpu_set" --arg load_cpu_set "$load_cpu_set" \
    --argjson measured_rtt_ms "$measured_rtt_ms" \
    --argjson configured_rtt_ms "$configured_rtt_ms" --argjson one_way_netem_ms "$one_way_netem_ms" \
    --argjson server_addresses "$server_addresses" --argjson server_routes "$server_routes" \
    --argjson router_addresses "$router_addresses" --argjson router_routes "$router_routes" --argjson router_qdisc "$router_qdisc" \
    --argjson agent_addresses "$agent_addresses" --argjson agent_routes "$agent_routes" \
    '{schema_version:1,pair:$pair,order_position:$order_position,variant:$variant,
      namespace_identities:{server:$server_namespace_identity,router:$router_namespace_identity,agent:$agent_namespace_identity},runner_namespace_identity:$runner_namespace_identity,
      namespace_instances:$namespace_instances,prior_joined_generation:$prior_joined_generation,namespace_helpers_joined:false,
      configured:{rtt_ms:$configured_rtt_ms,one_way_netem_ms:$one_way_netem_ms,rate_bits_per_second:1000000000,server_tunnel:"10.211.0.1:17443",server_admin:"127.0.0.1:18080",agent_admin:"127.0.0.1:18080"},
      cpu_sets:{server:$server_cpu_set,agent:$agent_cpu_set,load:$load_cpu_set},measured_rtt_ms:$measured_rtt_ms,
      network_state:{server:{addresses:$server_addresses,routes:$server_routes},router:{addresses:$router_addresses,routes:$router_routes,qdisc:$router_qdisc},agent:{addresses:$agent_addresses,routes:$agent_routes}}}' > "$destination"
}

stop_namespace_helper() {
  local pid=$1 expected_start=$2 expected_inode=$3 expected_boot=$4 attempt current_start current_identity forced=0
  [[ -n $pid && $expected_start =~ ^[0-9]+$ && $expected_inode =~ ^[0-9]+$ && $expected_boot == "$boot_id" ]] || return 1
  current_start=$(process_start_ticks "$pid") || return 1
  current_identity=$(readlink "/proc/$pid/ns/net") || return 1
  [[ $current_start == "$expected_start" && $current_identity == "net:[$expected_inode]" && $(tr -d '\n' < /proc/sys/kernel/random/boot_id) == "$expected_boot" ]] || return 1
  kill -TERM -- "-$pid" 2>/dev/null || return 1
  for ((attempt=0; attempt<50; attempt++)); do
    [[ -r /proc/$pid/stat ]] || break
    read -r current_start current_state < <(process_start_state "$pid") || break
    [[ $current_start == "$expected_start" ]] || return 1
    [[ $current_state == Z ]] && break
    sleep .02
  done
  if [[ -r /proc/$pid/stat ]]; then
    read -r current_start current_state < <(process_start_state "$pid") || true
    if [[ $current_start == "$expected_start" && $current_state != Z ]]; then
      forced=1
      current_identity=$(readlink "/proc/$pid/ns/net") || return 1
      [[ $current_identity == "net:[$expected_inode]" ]] || return 1
      kill -KILL -- "-$pid" 2>/dev/null || true
    fi
  fi
  wait "$pid" 2>/dev/null || true
  kill -0 -- "-$pid" 2>/dev/null && return 1
  unset "owned_groups[$pid]"
  if [[ -r /proc/$pid/stat ]]; then
    current_start=$(process_start_ticks "$pid") || true
    [[ $current_start != "$expected_start" ]] || return 1
  fi
  ((forced == 0))
}

stop_topology() {
  local destination=$1 status=0 tmp
  local server_start router_start agent_start server_inode router_inode agent_inode
  server_start=$(jq -r .server.helper_start_ticks <<<"$namespace_instances_json")
  router_start=$(jq -r .router.helper_start_ticks <<<"$namespace_instances_json")
  agent_start=$(jq -r .agent.helper_start_ticks <<<"$namespace_instances_json")
  server_inode=$(jq -r .server.netns_inode <<<"$namespace_instances_json")
  router_inode=$(jq -r .router.netns_inode <<<"$namespace_instances_json")
  agent_inode=$(jq -r .agent.netns_inode <<<"$namespace_instances_json")
  stop_namespace_helper "$agent_namespace_pid" "$agent_start" "$agent_inode" "$boot_id" || status=1
  stop_namespace_helper "$router_namespace_pid" "$router_start" "$router_inode" "$boot_id" || status=1
  stop_namespace_helper "$server_namespace_pid" "$server_start" "$server_inode" "$boot_id" || status=1
  agent_namespace_pid=""; router_namespace_pid=""; server_namespace_pid=""
  ((status == 0 && ${#owned_groups[@]} == 0)) || return 1
  last_joined_generation=$topology_generation
  tmp=$destination.tmp
  jq --argjson generation "$last_joined_generation" '.namespace_helpers_joined=true|.joined_generation=$generation' "$destination" > "$tmp"
  mv "$tmp" "$destination"
  jq -c . "$destination" >> "$topologies_output"
}

mapfile -t cpus < <(python3 - <<'PYCPU'
import os
for cpu in sorted(os.sched_getaffinity(0)): print(cpu)
PYCPU
)
((${#cpus[@]} >= 9))
server_cpu_set=$(IFS=,; echo "${cpus[*]:0:4}")
agent_cpu_set=$(IFS=,; echo "${cpus[*]:4:4}")
load_cpu_set=$(IFS=,; echo "${cpus[*]:8}")
for generation in 1 2 3 4; do
  case $generation in
    1) pair=1; order_position=1; variant=aggregate-yamux ;;
    2) pair=1; order_position=2; variant=raw-tls ;;
    3) pair=2; order_position=1; variant=raw-tls ;;
    4) pair=2; order_position=2; variant=aggregate-yamux ;;
  esac
  start_topology
  destination=$run_dir/generation-$generation-topology.json
  capture_topology "$pair" "$order_position" "$variant" "$destination"
  stop_topology "$destination"
done
((last_joined_generation == 4 && topology_generation == 4 && ${#owned_groups[@]} == 0))
trap - EXIT INT TERM

#!/usr/bin/env bash
# Diagnostic-only native HTTP/1 stage over the production aggregate agent path.
# One HTTP16 reproduction screen; not a production or legacy qualification.
set -Eeuo pipefail
server_binary=""; agent_binary=""; source_ref=""; build_manifest=""; output=""
while (($#)); do
  case "$1" in
    --server-binary) server_binary=$2; shift 2;;
    --agent-binary) agent_binary=$2; shift 2;;
    --source-ref) source_ref=$2; shift 2;;
    --build-manifest) build_manifest=$2; shift 2;;
    --output) output=$2; shift 2;;
    *) echo "unknown argument: $1" >&2; exit 2;;
  esac
done
[[ -x $server_binary && -x $agent_binary && -f $build_manifest && $source_ref =~ ^[0-9a-f]{40}$ && -n $output ]] || { echo "required arguments invalid" >&2; exit 2; }
[[ ${P2PSTREAM_TRANSPORT_CEILING_INNER_FIXTURE:-} == 1 && $$ -eq 1 ]] || { echo "reviewed private PID/network namespace wrapper required" >&2; exit 2; }
for tool in awk ip jq nsenter openssl ping python3 readlink realpath setsid sha256sum sysctl taskset tc unshare; do command -v "$tool" >/dev/null || exit 2; done
mapfile -t initial_interfaces < <(ip -o link show | awk -F': ' '{sub(/@.*/,"",$2); print $2}')
[[ ${#initial_interfaces[@]} -eq 1 && ${initial_interfaces[0]} == lo && -z $(ip route show) && -z $(ip -6 route show) ]] || { echo "nonempty fixture network namespace" >&2; exit 2; }
configured_rtt_ms=80; one_way_netem_ms=40; payload_bytes=268435456; phase_count=16
overall_timeout_seconds=900; server_address=10.211.0.1:17443
server_admin=http://127.0.0.1:18080; agent_admin=http://127.0.0.1:18080
mkdir -p "$(dirname "$output")"
run_dir=$(mktemp -d -p "$(dirname "$output")" http-stage.XXXXXXXX)
: >"$output"; snapshots_output=${output%.jsonl}.snapshots.jsonl; : >"$snapshots_output"
topologies_output=${output%.jsonl}.topologies.jsonl; : >"$topologies_output"
boundaries_output=${output%.jsonl}.conditioning-boundaries.jsonl; : >"$boundaries_output"
declare -A owned_groups=(); declare -A seen_helper_process_instances=()
topology_generation=0; last_joined_generation=0
boot_id=$(tr -d '\n' </proc/sys/kernel/random/boot_id); runner_namespace_identity=$(readlink /proc/self/ns/net)
[[ $boot_id =~ ^[0-9a-f-]{36}$ && $runner_namespace_identity =~ ^net:\[[0-9]+\]$ ]] || exit 2
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
try:
    text=Path(f"/proc/{int(sys.argv[1])}/stat").read_text()
except FileNotFoundError:
    raise SystemExit(3)
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
  local line start state status
  if line=$(process_start_state "$1"); then
    read -r start state <<<"$line" || return 1
  else
    status=$?
    return "$status"
  fi
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
  local pid=$1 expected_start=$2 expected_inode=$3 expected_boot=$4 attempt current_start current_state current_identity state_line state_status forced=0
  [[ -n $pid && $expected_start =~ ^[0-9]+$ && $expected_inode =~ ^[0-9]+$ && $expected_boot == "$boot_id" ]] || return 1
  current_start=$(process_start_ticks "$pid") || return 1
  current_identity=$(readlink "/proc/$pid/ns/net") || return 1
  [[ $current_start == "$expected_start" && $current_identity == "net:[$expected_inode]" && $(tr -d '\n' < /proc/sys/kernel/random/boot_id) == "$expected_boot" ]] || return 1
  kill -TERM -- "-$pid" 2>/dev/null || return 1
  for ((attempt=0; attempt<50; attempt++)); do
    if state_line=$(process_start_state "$pid"); then
      read -r current_start current_state <<<"$state_line" || return 1
    else
      state_status=$?
      ((state_status == 3)) && break
      return 1
    fi
    [[ $current_start == "$expected_start" ]] || return 1
    [[ $current_state == Z ]] && break
    sleep .02
  done
  if state_line=$(process_start_state "$pid"); then
    read -r current_start current_state <<<"$state_line" || return 1
  else
    state_status=$?
    ((state_status == 3)) || return 1
    current_start=""
    current_state=""
  fi
  if [[ -n $current_start && $current_start == "$expected_start" && $current_state != Z ]]; then
    forced=1
    current_identity=$(readlink "/proc/$pid/ns/net") || return 1
    [[ $current_identity == "net:[$expected_inode]" ]] || return 1
    kill -KILL -- "-$pid" 2>/dev/null || true
  fi
  wait "$pid" 2>/dev/null || true
  kill -0 -- "-$pid" 2>/dev/null && return 1
  unset "owned_groups[$pid]"
  if current_start=$(process_start_ticks "$pid"); then
    [[ $current_start != "$expected_start" ]] || return 1
  else
    state_status=$?
    ((state_status == 3)) || return 1
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


mapfile -t cpus < <(python3 - <<'PY'
import os
for cpu in sorted(os.sched_getaffinity(0)): print(cpu)
PY
)
python3 - "${cpus[@]}" <<'PY'
import sys
available={int(value) for value in sys.argv[1:]}
required=set(range(16))
if not required <= available:
    raise SystemExit(f"fixed CPUs 0-15 are required; missing {sorted(required-available)}")
PY
server_cpu_set=0,1,2,3; agent_cpu_set=4,5,6,7; load_cpu_set=8,9,10,11,12,13,14,15
cert=$run_dir/server.crt.pem; key=$run_dir/server.key.pem; auth_key=$run_dir/auth-key.hex
openssl ecparam -name prime256v1 -genkey -noout -out "$key"
openssl req -x509 -new -key "$key" -days 1 -sha256 -subj /CN=transport-ceiling.test -addext 'subjectAltName=DNS:transport-ceiling.test,IP:10.211.0.1' -out "$cert" >/dev/null 2>&1
openssl rand -hex 32 >"$auth_key"
server_binary=$(realpath "$server_binary"); agent_binary=$(realpath "$agent_binary")
server_binary_sha256=$(sha256sum "$server_binary"|awk '{print $1}'); agent_binary_sha256=$(sha256sum "$agent_binary"|awk '{print $1}')
build_manifest_sha256=$(sha256sum "$build_manifest"|awk '{print $1}'); runner_sha256=$(sha256sum "$0"|awk '{print $1}')
http_json() {
  local ns=$1 method=$2 url=$3 body=${4:-} request_timeout=${5:-300}; local -a prefix=(taskset -c "$load_cpu_set")
  [[ $ns == 0 ]] || prefix=(nsenter -t "$ns" -n taskset -c "$load_cpu_set")
  "${prefix[@]}" python3 - "$method" "$url" "$body" "$request_timeout" <<'PY'
import json,sys,urllib.request
method,url,body,timeout=sys.argv[1:]; data=body.encode() if body else None
request=urllib.request.Request(url,data=data,method=method,headers={"Content-Type":"application/json"})
with urllib.request.urlopen(request,timeout=float(timeout)) as response:
    if not 200<=response.status<300: raise SystemExit(f"HTTP {response.status}")
    raw=response.read(1048577)
    if len(raw)>1048576: raise SystemExit("admin response exceeds 1 MiB")
    sys.stdout.write(json.dumps(json.loads(raw),sort_keys=True,separators=(",",":")) if raw else "null")
PY
}
wait_snapshot() {
  local ns=$1 url=$2 pid=$3 dst=$4 pred=$5 label=$6 attempt deadline now
  deadline=$(python3 - <<'PY'
import time
print(time.monotonic()+35.0)
PY
)
  for ((attempt=0;attempt<700;attempt++)); do
    if http_json "$ns" GET "$url" '' 1 >"$dst.tmp" 2>/dev/null && jq -e "$pred" "$dst.tmp" >/dev/null; then mv "$dst.tmp" "$dst"; return; fi
    kill -0 -- "-$pid" 2>/dev/null || { echo "$label exited" >&2; return 1; }; sleep .05
    now=$(python3 - <<'PY'
import time
print(time.monotonic())
PY
)
    python3 - "$now" "$deadline" <<'PY' || break
import sys
raise SystemExit(0 if float(sys.argv[1]) < float(sys.argv[2]) else 1)
PY
  done
  echo "timeout: $label" >&2; return 1
}
wait_natural_exit() {
  local pid=$1 label=$2 attempt status
  for ((attempt=0;attempt<450;attempt++)); do kill -0 -- "-$pid" 2>/dev/null || break; sleep .1; done
  if kill -0 -- "-$pid" 2>/dev/null; then
    kill -TERM -- "-$pid" 2>/dev/null||true; for ((attempt=0;attempt<50;attempt++)); do kill -0 -- "-$pid" 2>/dev/null||break; sleep .1; done
    kill -KILL -- "-$pid" 2>/dev/null||true; wait "$pid" 2>/dev/null||true; unset "owned_groups[$pid]"; echo "$label forced stop" >&2; return 1
  fi
  set +e; wait "$pid"; status=$?; set -e; unset "owned_groups[$pid]"
  ((status==0)) || { echo "$label exit $status" >&2; return 1; }
}
wait_json_file() { local path=$1 label=$2 i; for ((i=0;i<100;i++)); do [[ -s $path ]]&&jq -e . "$path" >/dev/null&&return; sleep .05; done; echo "$label report missing" >&2; return 1; }

validate_http_stage() {
python3 - "$source_ref" "$server_binary_sha256" "$agent_binary_sha256" "$build_manifest_sha256" "$build_manifest" "$runner_sha256" "$measured_rtt_ms" \
 "$prefix-server-initial.json" "$prefix-agent-initial.json" "$prefix-server-live.json" "$prefix-agent-live.json" "$server_terminal" "$agent_terminal" "$prefix-phases" "$output" "$snapshots_output" "$boundaries_output" "$prefix-topology.json" <<'PY'
import ipaddress,json,math,re,sys
from pathlib import Path
(source,server_sha,agent_sha,manifest_sha,manifest_path,runner_sha,rtt,si_path,ai_path,sl_path,al_path,st_path,at_path,phases_dir,results_path,snapshots_path,boundaries_path,topology_path)=sys.argv[1:]
rtt=float(rtt); phases_dir=Path(phases_dir)
if not re.fullmatch(r"[0-9a-f]{40}",source) or any(not re.fullmatch(r"[0-9a-f]{64}",x) for x in (server_sha,agent_sha,manifest_sha,runner_sha)): raise ValueError("provenance")
if not 60<=rtt<=120: raise ValueError("RTT envelope")
schedule=[("conditioning-download-lane-1",1,"download","conditioning","fresh-stream"),("conditioning-download-lane-2",2,"download","conditioning","fresh-stream"),("conditioning-download-lane-3",3,"download","conditioning","fresh-stream"),("conditioning-download-lane-4",4,"download","conditioning","fresh-stream"),("conditioning-upload-lane-1",1,"upload","conditioning","fresh-stream"),("conditioning-upload-lane-2",2,"upload","conditioning","fresh-stream"),("conditioning-upload-lane-3",3,"upload","conditioning","fresh-stream"),("conditioning-upload-lane-4",4,"upload","conditioning","fresh-stream"),("retained-download-lane-1",1,"download","retained","new-persistent"),("retained-upload-lane-2",2,"upload","retained","new-persistent"),("timed-1-download-lane-3",3,"download","timed","new-persistent"),("timed-1-upload-lane-4",4,"upload","timed","new-persistent"),("timed-2-upload-lane-1",1,"upload","timed","direction-transition"),("timed-2-download-lane-2",2,"download","timed","direction-transition"),("timed-3-download-lane-3",3,"download","timed","same-direction-reuse"),("timed-3-upload-lane-4",4,"upload","timed","same-direction-reuse")]
def load(p): return json.loads(Path(p).read_text())
def integer(v,n,m=0):
 if isinstance(v,bool) or not isinstance(v,int) or v<m: raise ValueError(f"{n} integer")
 return v
def number(v,n,positive=False):
 if isinstance(v,bool) or not isinstance(v,(int,float)) or not math.isfinite(v) or positive and v<=0: raise ValueError(f"{n} number")
 return float(v)
def addr(v,n):
 if not isinstance(v,str) or not v: raise ValueError(n)
 host,port=v.rsplit(":",1); ipaddress.ip_address(host.strip("[]")); integer(int(port),n,1)
owner_keys={"capacity_total_in_use","capacity_pooled_in_use","capacity_opening","capacity_live","capacity_closing","capacity_registered_sessions","transport_entries","transport_draining","logical_opens","logical_closes","connection_reuses","origin_active_requests","lane_streams","lane_receive_limits"}
def owner(v,n):
 if not isinstance(v,dict) or set(v)!=owner_keys: raise ValueError(f"{n} owner schema")
 for k in owner_keys-{"lane_streams","lane_receive_limits"}: integer(v[k],f"{n}.{k}")
 for k in ("lane_streams","lane_receive_limits"):
  if not isinstance(v[k],list) or len(v[k])!=4: raise ValueError(f"{n}.{k}")
  for x in v[k]: integer(x,f"{n}.{k}")
 return v
manifest=load(manifest_path)
manifest_configuration=manifest.get("configuration") if isinstance(manifest,dict) else None
if not isinstance(manifest_configuration,dict) or set(manifest_configuration)!={"server","agent"}: raise ValueError("manifest configuration roles")
configuration_keys=None
def manifest_value(value,path):
 if isinstance(value,dict):
  if not value or any(not isinstance(key,str) or not key for key in value): raise ValueError(f"{path} object")
  for key,item in value.items(): manifest_value(item,f"{path}.{key}")
 elif isinstance(value,(str,int,bool)) and not isinstance(value,float):
  return
 else: raise ValueError(f"{path} type")
for manifest_role in ("server","agent"):
 expected=manifest_configuration[manifest_role]
 if not isinstance(expected,dict) or not expected: raise ValueError(f"manifest {manifest_role} configuration")
 if configuration_keys is None: configuration_keys=set(expected)
 elif set(expected)!=configuration_keys: raise ValueError("manifest configuration key sets")
 manifest_value(expected,f"manifest.{manifest_role}.configuration")
def same_typed_shape(value,expected,path):
 if isinstance(expected,dict):
  if not isinstance(value,dict) or set(value)!=set(expected): raise ValueError(f"{path} schema")
  for key,item in expected.items(): same_typed_shape(value[key],item,f"{path}.{key}")
 elif isinstance(expected,bool):
  if not isinstance(value,bool): raise ValueError(f"{path} boolean")
 elif isinstance(expected,int):
  if isinstance(value,bool) or not isinstance(value,int): raise ValueError(f"{path} integer")
 elif not isinstance(value,str): raise ValueError(f"{path} string")
def configuration(v,role):
 expected=manifest_configuration[role]
 same_typed_shape(v,expected,f"{role}.configuration")
 if v!=expected: raise ValueError(f"{role} configuration differs from manifest")
socket_keys={"lane","lane_role","local_address","remote_address","connection_identity","tls_version","tls_cipher_suite","tcp_info_captured","tcp_info_error","tcp_state","connected"}; id_keys=socket_keys-{"tcp_info_captured","tcp_info_error","tcp_state","connected"}
def sockets(rows,n,connected,established=False):
 if not isinstance(rows,list) or len(rows)!=5: raise ValueError(f"{n} sockets")
 out={}
 for row in rows:
  if not isinstance(row,dict) or set(row)!=socket_keys: raise ValueError(f"{n} schema")
  lane=integer(row["lane"],f"{n}.lane")
  if lane>4 or lane in out or row["lane_role"]!=("control" if lane==0 else "data") or row["connected"] is not connected: raise ValueError(f"{n} lane")
  addr(row["local_address"],n); addr(row["remote_address"],n); integer(row["tls_version"],n,1); integer(row["tls_cipher_suite"],n,1); integer(row["tcp_state"],n)
  if not isinstance(row["connection_identity"],str) or not row["connection_identity"] or not isinstance(row["tcp_info_captured"],bool) or not isinstance(row["tcp_info_error"],str): raise ValueError(f"{n} fields")
  if established and (row["tcp_info_captured"] is not True or row["tcp_info_error"]!="" or row["tcp_state"]!=1): raise ValueError(f"{n} established")
  out[lane]=row
 if set(out)!=set(range(5)): raise ValueError(f"{n} lane set")
 return out
def reverse(s,a):
 for lane in range(5):
  if (s[lane]["local_address"],s[lane]["remote_address"],s[lane]["tls_version"],s[lane]["tls_cipher_suite"])!=(a[lane]["remote_address"],a[lane]["local_address"],a[lane]["tls_version"],a[lane]["tls_cipher_suite"]): raise ValueError(f"reverse {lane}")
ledger_base={"limit_bytes","initial_reserved_bytes","growth_reserved_bytes","current_bytes","high_water_bytes","denied_reservations","release_count","final_bytes","actual_heap_alloc_bytes","actual_open_fds","actual_socket_fds"}
def ledger(v,n,limit,terminal=False,working=False):
 required=ledger_base|({"categories"} if working else set())
 if not isinstance(v,dict) or set(v)!=required: raise ValueError(f"{n} schema")
 for k in ledger_base: integer(v[k],f"{n}.{k}")
 if v["limit_bytes"]!=limit or v["denied_reservations"]!=0 or not 0<=v["current_bytes"]<=v["high_water_bytes"]<=limit: raise ValueError(f"{n} accounting")
 if terminal and (v["current_bytes"] or v["final_bytes"]): raise ValueError(f"{n} terminal")
 if working:
  cats={"session_metadata","stream_metadata","relay_buffers","frame_buffers","tls_app_buffers"}
  if not isinstance(v["categories"],dict) or set(v["categories"])!=cats: raise ValueError(f"{n} categories")
  for x in v["categories"].values(): integer(x,n)
  if sum(v["categories"].values())!=v["current_bytes"] or terminal and (any(v["categories"].values()) or v["release_count"]<=0): raise ValueError(f"{n} category accounting")
 else:
  reserved=v["initial_reserved_bytes"]+v["growth_reserved_bytes"]
  if v["high_water_bytes"]!=reserved or v["initial_reserved_bytes"]<=0 or (terminal and v["release_count"]<4) or (not terminal and v["current_bytes"]!=reserved): raise ValueError(f"{n} pool")
def base(v,role,completed,terminal=False):
 if not isinstance(v,dict) or integer(v.get("schema_version"),role)!=1 or v.get("process_role")!=role or v.get("variant")!="aggregate-http1" or v.get("source_ref")!=source: raise ValueError(f"{role} identity")
 if integer(v.get("expected_phases"),role)!=16 or integer(v.get("completed_phases"),role)!=completed or integer(v.get("reconnect_count"),role)!=0 or v.get("invalid_reason")!="" or v.get("errors")!=[]: raise ValueError(f"{role} state")
 configuration(v.get("configuration"),role)
 ledger(v.get("pool_ledger"),role+" pool",2147483648,terminal); ledger(v.get("working_ledger"),role+" working",67108864,terminal,True); owner(v.get("ownership"),role)
 if completed==16 and v["pool_ledger"]["growth_reserved_bytes"]<=0: raise ValueError(f"{role} receive pool never grew")
def initial(v,role):
 base(v,role,0)
 if v.get("ready") is not True or v.get("graceful_shutdown") is not False or v.get("same_socket_identities") is not True: raise ValueError(role+" readiness")
 first=sockets(v.get("physical_sockets_initial"),role,True,True); current=sockets(v.get("physical_sockets_current"),role,True,True)
 for lane in range(5):
  if any(first[lane][k]!=current[lane][k] for k in id_keys): raise ValueError(role+" readiness identity")
 return first
def live(v,role,first):
 base(v,role,16)
 if v.get("ready") is not True or v.get("graceful_shutdown") is not False or v.get("same_socket_identities") is not True: raise ValueError(role+" live")
 current=sockets(v.get("physical_sockets_current"),role,True,True)
 for lane in range(5):
  if any(first[lane][k]!=current[lane][k] for k in id_keys): raise ValueError(role+" live identity")
 return current
def terminal(v,role,first,results):
 base(v,role,16,True)
 if v.get("ready") is not False or v.get("graceful_shutdown") is not True or v.get("same_socket_identities") is not True or v.get("results")!=results: raise ValueError(role+" terminal")
 for index,result in enumerate(v["results"]): phase(result,index,role)
 terminal_owner=owner(v["ownership"],role+" terminal")
 for key in ("capacity_total_in_use","capacity_pooled_in_use","capacity_opening","capacity_live","capacity_closing","capacity_registered_sessions","transport_entries","transport_draining","origin_active_requests"):
  if terminal_owner[key]!=0: raise ValueError(f"{role} terminal ownership {key}")
 if any(terminal_owner["lane_streams"]): raise ValueError(role+" terminal lane streams")
 current=sockets(v.get("physical_sockets_current"),role,False)
 for lane in range(5):
  if any(first[lane][k]!=current[lane][k] for k in id_keys): raise ValueError(role+" terminal identity")
conn_keys={"connection_identity","origin_connection_identity","reused","was_idle","idle_nanoseconds","put_idle","put_idle_error","dial_opened"}
phase_keys={"schema_version","phase_index","phase_name","lane","direction","stage","planned_reuse_kind","configured_payload_bytes","received_payload_bytes","expected_sha256","received_sha256","http_status","origin_http_major","response_content_length","phase_wall_nanoseconds","useful_bytes_per_second","connection","ownership_before","ownership_after","conditioning_pool_closed","conditioning_quiescent","response_pump_buffer_bytes","final_consumer_read_bytes","error"}
def phase(v,index,role):
 if not isinstance(v,dict) or set(v)!=phase_keys: raise ValueError(f"{role} phase schema")
 name,lane,direction,stage,reuse=schedule[index]
 if integer(v["schema_version"],role)!=1 or integer(v["phase_index"],role)!=index or integer(v["lane"],role,1)!=lane or (v["phase_name"],v["direction"],v["stage"],v["planned_reuse_kind"])!=(name,direction,stage,reuse): raise ValueError(f"{role} phase identity")
 digest="e1a511c229b7fdd398a94b74c98b7f16411fa0ec93302744f7e5d959275a907a" if direction=="download" else "a6d72ac7690f53be6ae46ba88506bd97302a093f7108472bd9efc3cefda06484"
 if integer(v["configured_payload_bytes"],role)!=268435456 or integer(v["received_payload_bytes"],role)!=268435456 or v["expected_sha256"]!=digest or v["received_sha256"]!=digest or integer(v["http_status"],role)!=200 or integer(v["origin_http_major"],role)!=1 or v["error"]!="": raise ValueError(f"{role} phase correctness")
 if integer(v["response_content_length"],role)!=(268435456 if direction=="download" else 32) or integer(v["response_pump_buffer_bytes"],role)!=32768 or integer(v["final_consumer_read_bytes"],role)!=8192: raise ValueError(f"{role} phase boundary")
 if not isinstance(v["connection"],dict) or set(v["connection"])!=conn_keys: raise ValueError(f"{role} connection schema")
 for key in ("connection_identity","origin_connection_identity","put_idle_error"):
  if not isinstance(v["connection"][key],str): raise ValueError(f"{role} connection {key}")
 for key in ("reused","was_idle","put_idle","dial_opened"):
  if not isinstance(v["connection"][key],bool): raise ValueError(f"{role} connection {key}")
 integer(v["connection"]["idle_nanoseconds"],f"{role} connection idle")
 owner(v["ownership_before"],role+" before"); after=owner(v["ownership_after"],role+" after")
 if role=="server":
  wall=integer(v["phase_wall_nanoseconds"],role,1); rate=number(v["useful_bytes_per_second"],role,True)
  if abs(rate-268435456e9/wall)>max(rate*1e-6,1): raise ValueError("rate")
  if v["conditioning_pool_closed"] is not (stage=="conditioning") or v["conditioning_quiescent"] is not (stage=="conditioning"): raise ValueError("conditioning lifecycle")
  if stage=="conditioning":
   for key in ("capacity_total_in_use","capacity_pooled_in_use","capacity_opening","capacity_live","capacity_closing","transport_entries","transport_draining"):
    if after[key]!=0: raise ValueError(f"server conditioning ownership {key}")
   if any(after["lane_streams"]): raise ValueError("server conditioning lane streams")
 else:
  if integer(v["phase_wall_nanoseconds"],role)!=0 or number(v["useful_bytes_per_second"],role)!=0 or v["conditioning_pool_closed"] is not False or v["conditioning_quiescent"] is not False: raise ValueError("agent timing/lifecycle")
 return v
si_doc,ai_doc=load(si_path),load(ai_path); si,ai=initial(si_doc,"server"),initial(ai_doc,"agent"); reverse(si,ai)
sl_doc,al_doc=load(sl_path),load(al_path); sl,al=live(sl_doc,"server",si),live(al_doc,"agent",ai); reverse(sl,al)
sr,ar=sl_doc.get("results"),al_doc.get("results")
if not isinstance(sr,list) or len(sr)!=16 or not isinstance(ar,list) or len(ar)!=16: raise ValueError("results count")
st_doc,at_doc=load(st_path),load(at_path); terminal(st_doc,"server",si,sr); terminal(at_doc,"agent",ai,ar)
rows=[]
for i in range(16):
 response=phase(load(phases_dir/f"phase-{i:02d}.json"),i,"server"); server=phase(sr[i],i,"server"); agent=phase(ar[i],i,"agent")
 if response!=server: raise ValueError("response changed")
 row=dict(server); row.update({"source_ref":source,"server_binary_sha256":server_sha,"agent_binary_sha256":agent_sha,"build_manifest_sha256":manifest_sha,"runner_sha256":runner_sha,"configured_rtt_ms":80,"measured_rtt_ms":rtt,"agent_result":agent}); rows.append(row)
boundaries=[json.loads(x) for x in Path(boundaries_path).read_text().splitlines() if x]
if len(boundaries)!=8: raise ValueError("boundary count")
for i,v in enumerate(boundaries):
 base(v,"agent",i+1); own=owner(v["ownership"],"boundary")
 if own["capacity_total_in_use"] or own["origin_active_requests"] or any(own["lane_streams"]): raise ValueError("boundary not quiescent")
topology=load(topology_path)
topology_keys={"schema_version","pair","order_position","variant","namespace_identities","runner_namespace_identity","namespace_instances","prior_joined_generation","namespace_helpers_joined","joined_generation","configured","cpu_sets","measured_rtt_ms","network_state"}
if not isinstance(topology,dict) or set(topology)!=topology_keys: raise ValueError("topology schema")
if integer(topology["schema_version"],"topology schema")!=1 or integer(topology["pair"],"topology pair",1)!=1 or integer(topology["order_position"],"topology order",1)!=1 or topology["variant"]!="aggregate-http1": raise ValueError("topology identity")
identities=topology["namespace_identities"]; instances=topology["namespace_instances"]; runner_identity=topology["runner_namespace_identity"]
if not isinstance(identities,dict) or set(identities)!={"server","router","agent"} or not isinstance(instances,dict) or set(instances)!={"server","router","agent"}: raise ValueError("topology namespace roles")
if not isinstance(runner_identity,str) or not re.fullmatch(r"net:\[[0-9]+\]",runner_identity): raise ValueError("runner namespace identity")
instance_keys={"boot_id","helper_pid","helper_start_ticks","netns_inode","arm_generation"}; process_keys=[]; inodes=[]; boots=[]
for role in ("server","router","agent"):
 identity=identities[role]; instance=instances[role]
 if not isinstance(identity,str) or not re.fullmatch(r"net:\[[0-9]+\]",identity) or identity==runner_identity or not isinstance(instance,dict) or set(instance)!=instance_keys: raise ValueError(f"{role} namespace schema")
 boot=instance["boot_id"]
 if not isinstance(boot,str) or not re.fullmatch(r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}",boot): raise ValueError(f"{role} boot identity")
 pid=integer(instance["helper_pid"],f"{role} helper pid",2); start=integer(instance["helper_start_ticks"],f"{role} helper start",1); inode=integer(instance["netns_inode"],f"{role} inode",1); generation=integer(instance["arm_generation"],f"{role} generation",1)
 if generation!=1 or identity!=f"net:[{inode}]": raise ValueError(f"{role} namespace binding")
 process_keys.append((boot,pid,start)); inodes.append(inode); boots.append(boot)
if len(set(process_keys))!=3 or len(set(inodes))!=3 or len(set(boots))!=1: raise ValueError("topology helper uniqueness")
if integer(topology["prior_joined_generation"],"prior generation")!=0 or topology["namespace_helpers_joined"] is not True or integer(topology["joined_generation"],"joined generation",1)!=1: raise ValueError("topology teardown chronology")
expected_config={"rtt_ms":80,"one_way_netem_ms":40,"rate_bits_per_second":1000000000,"server_tunnel":"10.211.0.1:17443","server_admin":"127.0.0.1:18080","agent_admin":"127.0.0.1:18080"}
if topology["configured"]!=expected_config or topology["cpu_sets"]!={"server":"0,1,2,3","agent":"4,5,6,7","load":"8,9,10,11,12,13,14,15"}: raise ValueError("topology configuration/CPU binding")
if not isinstance(topology["network_state"],dict) or set(topology["network_state"])!={"server","router","agent"}: raise ValueError("topology network-state evidence")
if number(topology["measured_rtt_ms"],"topology RTT",True)!=rtt: raise ValueError("topology/result RTT mismatch")
with Path(results_path).open("w") as out:
 for row in rows: out.write(json.dumps(row,sort_keys=True,separators=(",",":"))+"\n")
with Path(snapshots_path).open("w") as out:
 for stage,s,a in (("initial",si_doc,ai_doc),("live-post16",sl_doc,al_doc),("terminal",st_doc,at_doc)): out.write(json.dumps({"stage":stage,"server":s,"agent":a},sort_keys=True,separators=(",",":"))+"\n")
condition=sorted(row["useful_bytes_per_second"] for row in rows[:4]); median=(condition[1]+condition[2])/2
signature=median>=29_700_000 and any(rows[i]["useful_bytes_per_second"]<29_700_000 for i in (8,10,13)) and rows[14]["useful_bytes_per_second"]>=29_700_000
all_post_conditioning_downloads_healthy=all(rows[i]["useful_bytes_per_second"]>=29_700_000 for i in (8,10,13,14))
classification=("inconclusive_conditioning" if median<29_700_000 else
                "reproduced_signature" if signature else
                "healthy_nonreproduction" if all_post_conditioning_downloads_healthy else
                "inconclusive_other_slowdown")
summary={"schema_version":1,"kind":"p2pstream-agent-http-stage-screen-v1","complete":True,"correctness_pass":True,"classification":classification,"reproduction_signature":signature,"all_post_conditioning_downloads_healthy":all_post_conditioning_downloads_healthy,"conditioning_download_median_bytes_per_second":median,"threshold_bytes_per_second":29_700_000,"post_conditioning_downloads":{str(i):rows[i]["useful_bytes_per_second"] for i in (8,10,13,14)},"source_ref":source,"server_binary_sha256":server_sha,"agent_binary_sha256":agent_sha,"build_manifest_sha256":manifest_sha,"runner_sha256":runner_sha,"configured_rtt_ms":80,"measured_rtt_ms":rtt,"standing_working_ledger_scope":"fixture allowance/promise, not a hard TLS/heap/resident bound","omitted_pipeline_layers":["public TLS/H2","ReverseProxy and Director/ErrorHandler","public retries/cache/WAF/shaping"],"upload_origin_difference":"native io.Copy into SHA-256 with exact Content-Length differs from bare 8 KiB sink","existing_bare64_role":"context only, not a causal comparison","next_step":"fixed contemporaneous bare/HTTP order confirmation" if signature else ("separate public-H2 stage" if classification=="healthy_nonreproduction" else "diagnose invalid baseline or alternate slowdown before interpretation")}
Path(str(results_path).replace(".jsonl",".summary.json")).write_text(json.dumps(summary,sort_keys=True,separators=(",",":"))+"\n")
PY
}
start_topology
prefix=$run_dir/http-stage; capture_topology 1 1 aggregate-http1 "$prefix-topology.json"; printf '%s\n' "$measured_rtt_ms" >"$prefix-measured-rtt-ms.txt"
server_terminal=$prefix-server-terminal.json; agent_terminal=$prefix-agent-terminal.json; mkdir -p "$prefix-phases"
setsid nsenter -t "$server_namespace_pid" -n env GOMAXPROCS=4 P2PSTREAM_HTTP_STAGE_EXTERNAL=1 P2PSTREAM_HTTP_STAGE_SOURCE_REF="$source_ref" taskset -c "$server_cpu_set" "$server_binary" -test.run '^TestHTTPStageExternalServerRole$' -test.timeout 600s --http-stage-listen="$server_address" --http-stage-admin=127.0.0.1:18080 --http-stage-origin=127.0.0.1:19090 --http-stage-cert="$cert" --http-stage-key="$key" --http-stage-auth-key-file="$auth_key" --http-stage-output="$server_terminal" >"$prefix-server.log" 2>&1 &
server_pid=$!; owned_groups[$server_pid]=1
setsid nsenter -t "$agent_namespace_pid" -n env GOMAXPROCS=4 P2PSTREAM_HTTP_STAGE_EXTERNAL=1 P2PSTREAM_HTTP_STAGE_SOURCE_REF="$source_ref" taskset -c "$agent_cpu_set" "$agent_binary" -test.run '^TestHTTPStageExternalAgentRole$' -test.timeout 600s --http-stage-server="$server_address" --http-stage-admin=127.0.0.1:18080 --http-stage-origin=127.0.0.1:19090 --http-stage-ca="$cert" --http-stage-server-name=transport-ceiling.test --http-stage-auth-key-file="$auth_key" --http-stage-output="$agent_terminal" >"$prefix-agent.log" 2>&1 &
agent_pid=$!; owned_groups[$agent_pid]=1
initial_predicate='.schema_version==1 and .ready==true and .completed_phases==0 and .invalid_reason==""'
wait_snapshot "$server_namespace_pid" "$server_admin/http-stage/readiness" "$server_pid" "$prefix-server-initial.json" "$initial_predicate" "server readiness"
wait_snapshot "$agent_namespace_pid" "$agent_admin/http-stage/readiness" "$agent_pid" "$prefix-agent-initial.json" "$initial_predicate" "agent readiness"
for ((phase=0;phase<phase_count;phase++)); do
 body=$(printf '{"schema_version":1,"phase_index":%d}' "$phase"); result=$prefix-phases/phase-$(printf '%02d' "$phase").json
 http_json "$server_namespace_pid" POST "$server_admin/http-stage/transfer" "$body" >"$result"
 jq -e --argjson p "$phase" '.schema_version==1 and .phase_index==$p and .configured_payload_bytes==268435456 and .received_payload_bytes==268435456 and .http_status==200 and .origin_http_major==1 and .error==""' "$result" >/dev/null
 if ((phase<8)); then
  boundary=$prefix-agent-boundary-$(printf '%02d' "$phase").json
  predicate=$(printf '.schema_version==1 and .completed_phases==%d and .invalid_reason=="" and .ownership.capacity_total_in_use==0 and .ownership.origin_active_requests==0 and (.ownership.lane_streams|all(.==0))' "$((phase+1))")
  wait_snapshot "$agent_namespace_pid" "$agent_admin/http-stage/readiness" "$agent_pid" "$boundary" "$predicate" "agent boundary $phase"
  jq -c . "$boundary" >>"$boundaries_output"
 fi
done
live_predicate='.schema_version==1 and .ready==true and .completed_phases==16 and .same_socket_identities==true and .invalid_reason==""'
wait_snapshot "$server_namespace_pid" "$server_admin/http-stage/live-post16" "$server_pid" "$prefix-server-live.json" "$live_predicate" "server live-post16"
wait_snapshot "$agent_namespace_pid" "$agent_admin/http-stage/live-post16" "$agent_pid" "$prefix-agent-live.json" "$live_predicate" "agent live-post16"
http_json "$server_namespace_pid" POST "$server_admin/http-stage/shutdown" '{"schema_version":1}' >"$prefix-shutdown.json"; jq -e 'type=="null"' "$prefix-shutdown.json" >/dev/null
wait_natural_exit "$server_pid" "server"; wait_natural_exit "$agent_pid" "agent"
wait_json_file "$server_terminal" server; wait_json_file "$agent_terminal" agent
stop_topology "$prefix-topology.json"
validate_http_stage
cp "$prefix-topology.json" "${output%.jsonl}.topology.json"

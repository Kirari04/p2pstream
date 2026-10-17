#!/usr/bin/env bash
# Fixture-only exact-binary LOWAT lifecycle smoke; timings are not performance evidence.
set -Eeuo pipefail
bundle=$1
runner=$2
output_dir=$3
mkdir -p "$output_dir"
for tool in setsid timeout jq python3 openssl sha256sum taskset realpath; do command -v "$tool" >/dev/null; done
(
  cd "$bundle"
  sha256sum -c SHA256SUMS >/dev/null
)
[[ $(sha256sum "$bundle/build-manifest.json"|awk '{print $1}') == 1c3cc0f51ca0cbabc9d45b3b6d52cdba5334ef8170d870112b02dfcd83e34d06 ]]
[[ $(sha256sum "$runner"|awk '{print $1}') == 0adef903ef51c3bffe153b2a528442dda1ed8e9678078123520cb4c70c556d4b ]]
driver=$(realpath "$bundle/driver-origin.test")
control_server=$(realpath "$bundle/tunnel-lowat-default-control.server.test")
control_agent=$(realpath "$bundle/tunnel-lowat-default-control.agent.test")
lowat_server=$(realpath "$bundle/tunnel-lowat-32k.server.test")
lowat_agent=$(realpath "$bundle/tunnel-lowat-32k.agent.test")
declare -A expected=(
  [driver]=3f7c28fc43105efc69ffbbfea9b277c345e2fd7577653f5cb3be8e8708a7b388
  [control_server]=3f7c28fc43105efc69ffbbfea9b277c345e2fd7577653f5cb3be8e8708a7b388
  [control_agent]=e2129ca520e07f79186141d000ab2fd0e390574712868c05772fd7122a659c05
  [lowat_server]=7bb33c1ee36819be658976cd526051285d80e4f870457884e78216575580a10f
  [lowat_agent]=e18c465aaff113e642be4cf8e10eac13263a2b307dcee4f61573801f785862f8
)
for name in driver control_server control_agent lowat_server lowat_agent; do
  [[ $(sha256sum "${!name}"|awk '{print $1}') == "${expected[$name]}" ]] || { echo "$name hash mismatch" >&2; exit 2; }
done
mapfile -t cpus < <(python3 - <<'PY'
import os
for cpu in sorted(os.sched_getaffinity(0)): print(cpu)
PY
)
((${#cpus[@]} >= 9))
server_cpus=$(IFS=,; echo "${cpus[*]:0:4}")
agent_cpus=$(IFS=,; echo "${cpus[*]:4:4}")
load_cpus=$(IFS=,; echo "${cpus[*]:8}")
declare -A owned=()
stops=()
cleanup() {
  local stop pid attempt
  for stop in "${stops[@]:-}"; do [[ -n $stop ]] && touch "$stop"; done
  for pid in "${!owned[@]}"; do kill -TERM -- "-$pid" 2>/dev/null || true; done
  for ((attempt=0;attempt<50;attempt++)); do
    local live=0
    for pid in "${!owned[@]}"; do kill -0 -- "-$pid" 2>/dev/null && live=1; done
    ((live)) || break
    sleep .1
  done
  for pid in "${!owned[@]}"; do kill -KILL -- "-$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; done
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
wait_ready() {
  local ready=$1 pid=$2
  for ((attempt=0;attempt<400;attempt++)); do
    [[ -s $ready ]] && jq -e . "$ready" >/dev/null && return 0
    kill -0 "$pid" 2>/dev/null || return 1
    sleep .05
  done
  return 1
}
natural_stop() {
  local pid=$1 stop=$2 label=$3
  touch "$stop"
  for ((attempt=0;attempt<200;attempt++)); do kill -0 -- "-$pid" 2>/dev/null || break; sleep .05; done
  if kill -0 -- "-$pid" 2>/dev/null; then
    kill -TERM -- "-$pid" 2>/dev/null || true
    for ((attempt=0;attempt<50;attempt++)); do kill -0 -- "-$pid" 2>/dev/null || break; sleep .1; done
    kill -KILL -- "-$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; unset 'owned[$pid]'
    echo "$label required forced shutdown" >&2; return 1
  fi
  local status
  set +e; wait "$pid"; status=$?; set -e
  unset 'owned[$pid]'
  ((status == 0)) || { echo "$label exited $status" >&2; return 1; }
}
wait_aggregate() {
  python3 - "$1" <<'PY'
import json,sys,time,urllib.request
url=sys.argv[1]; deadline=time.monotonic()+30; last=None
while time.monotonic()<deadline:
    try:
        with urllib.request.urlopen(url,timeout=.5) as response: last=json.load(response).get('agent_tunnels',[])
        if len(last)==1 and last[0].get('connected') and last[0].get('flow_control')=='aggregate-v1' and last[0].get('control_connected') and last[0].get('bulk_sessions')==4:
            lanes=last[0].get('lanes',[])
            if len(lanes)==5 and sorted(x.get('lane') for x in lanes)==[0,1,2,3,4] and len([x for x in lanes if x.get('lane',0)>0 and x.get('aggregate')])==4: raise SystemExit(0)
    except Exception as error: last=repr(error)
    time.sleep(.05)
raise SystemExit(f'aggregate readiness failed: {last!r}')
PY
}
capture_socket_pair() {
  local label=$1 stage=$2 server_admin=$3 agent_admin=$4 destination=$5
  python3 - "$runner" "$label" "$stage" "$server_admin" "$agent_admin" "$destination" <<'PY'
import json,pathlib,subprocess,sys,urllib.parse,urllib.request
runner,label,stage,server_url,agent_url,destination=sys.argv[1:]
text=pathlib.Path(runner).read_text(); marker='python3 - "$label" "$stage" "$server_json" "$agent_json" <<\'PY\'\n'
validator=text[text.index(marker)+len(marker):].split('\nPY\n',1)[0]
def fetch(url):
    target=url+('&' if '?' in url else '?')+urllib.parse.urlencode({'tunnel_socket_snapshot':'1'})
    with urllib.request.urlopen(target,timeout=5) as response: return json.load(response)
server=fetch(server_url); agent=fetch(agent_url)
proc=subprocess.run([sys.executable,'-c',validator,label,stage,json.dumps(server,separators=(',',':')),json.dumps(agent,separators=(',',':'))],capture_output=True,text=True)
value={'label':label,'stage':stage,'server':server,'agent':agent,'validation_ok':proc.returncode==0,'validation_error':proc.stderr,'reverse_tuple_matches':json.loads(proc.stdout) if proc.returncode==0 else []}
pathlib.Path(destination).write_text(json.dumps(value,indent=2,sort_keys=True)+'\n')
if proc.returncode: raise SystemExit(proc.stderr)
PY
}
cert=$output_dir/public.crt.pem
key=$output_dir/public.key.pem
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj /CN=public.test -days 1 -keyout "$key" -out "$cert" >/dev/null 2>&1
origin_ready=$output_dir/origin.ready.json
origin_stop=$output_dir/origin.stop
setsid env P2PSTREAM_QUALIFICATION_ROLE=origin P2PSTREAM_QUALIFICATION_READY="$origin_ready" P2PSTREAM_QUALIFICATION_STOP="$origin_stop" P2PSTREAM_QUALIFICATION_BIND_ADDRESS=127.0.0.1 taskset -c "$agent_cpus" "$driver" -test.run '^TestProxyQualificationProcess$' -test.timeout 300s >"$output_dir/origin.log" 2>&1 &
origin_pid=$!; owned[$origin_pid]=1; stops+=("$origin_stop"); wait_ready "$origin_ready" "$origin_pid"
origin_url=$(jq -r .url "$origin_ready")
sequence=0
run_transfer() {
  local variant=$1 direction=$2 server_pid=$3 public_url=$4 server_admin=$5
  sequence=$((sequence+1))
  local scenario suffix expected_response
  if [[ $direction == download ]]; then scenario=growth-download; suffix=bulk; expected_response=$((4<<20)); else scenario=growth-upload; suffix=bulk-upload; expected_response=32; fi
  local result=$output_dir/$sequence-$variant-$direction.json
  local server_spec
  server_spec=$(jq -cn --argjson pid "$server_pid" --arg admin "$server_admin" --arg cpus "$server_cpus" '{role:"server",pid:$pid,admin_url:$admin,cpu_set:$cpus}')
  setsid timeout --signal=TERM --kill-after=5s 45s env \
    P2PSTREAM_QUALIFICATION_ROLE=load P2PSTREAM_QUALIFICATION_URL="$public_url/agent/$suffix" P2PSTREAM_QUALIFICATION_SCENARIO="$scenario" P2PSTREAM_QUALIFICATION_PROTOCOL=h2 \
    P2PSTREAM_QUALIFICATION_CONCURRENCY=1 P2PSTREAM_QUALIFICATION_REQUESTS=1 P2PSTREAM_QUALIFICATION_OFFERED_RPS=0 P2PSTREAM_QUALIFICATION_DURATION_SECONDS=0 \
    P2PSTREAM_QUALIFICATION_WARMUP_SECONDS=0 P2PSTREAM_QUALIFICATION_GROWTH_PAYLOAD_BYTES=$((4<<20)) P2PSTREAM_QUALIFICATION_LABEL="fixture-$variant" \
    P2PSTREAM_QUALIFICATION_PATH=tunnel-lowat-external-smoke P2PSTREAM_QUALIFICATION_PIDS="$server_pid" P2PSTREAM_QUALIFICATION_ADMIN_URL="$server_admin" \
    P2PSTREAM_QUALIFICATION_PROXY_CPU_SET="$server_cpus" P2PSTREAM_QUALIFICATION_SERVER_PROCESS="$server_spec" P2PSTREAM_QUALIFICATION_LOAD_CPU_SET="$load_cpus" \
    P2PSTREAM_QUALIFICATION_RESOURCE_ENVELOPE=p2pstream-tunnel-lowat-fixture-only-loopback P2PSTREAM_QUALIFICATION_SEQUENCE="$sequence" P2PSTREAM_QUALIFICATION_RESULT="$result" \
    taskset -c "$load_cpus" "$driver" -test.run '^TestProxyQualificationLoad$' -test.timeout 40s >"$result.log" 2>&1 &
  local load_group=$! load_status; owned[$load_group]=1
  if wait "$load_group"; then load_status=0; else load_status=$?; fi
  unset 'owned[$load_group]'
  ((load_status==0)) || { echo "$variant $direction load exited $load_status" >&2; return 1; }
  jq -e --argjson expected "$expected_response" '
    .attempted==1 and .useful_completed==1 and .errors==0 and .status_classes=={"2xx":1} and .status_codes=={"200":1} and
    .observed_http_major==2 and .observed_origin_http_major==1 and .configured_payload_bytes==(4*1024*1024) and .response_bytes==$expected and
    .event_stats_captured==true and .persisted_event_delta==1 and ((.dropped_event_delta//0)==0) and ((.event_queue_after_drain//0)==0) and
    ((.event_drain_error//"")=="") and (.agent_tunnels_final|length)==1 and .agent_tunnels_final[0].flow_control=="aggregate-v1"
  ' "$result" >/dev/null
}
run_variant() {
  local variant=$1 server_binary=$2 agent_binary=$3
  local ready=$output_dir/$variant-server.ready.json stop=$output_dir/$variant-server.stop ca=$output_dir/$variant-management-ca.pem
  setsid env P2PSTREAM_QUALIFICATION_ROLE=proxy-external-agent P2PSTREAM_QUALIFICATION_READY="$ready" P2PSTREAM_QUALIFICATION_STOP="$stop" \
    P2PSTREAM_QUALIFICATION_ORIGIN_URL="$origin_url" P2PSTREAM_QUALIFICATION_CERT="$cert" P2PSTREAM_QUALIFICATION_KEY="$key" \
    P2PSTREAM_QUALIFICATION_MANAGEMENT_CA_FILE="$ca" P2PSTREAM_QUALIFICATION_EXTERNAL_AGENTS=1 P2PSTREAM_QUALIFICATION_BIND_ADDRESS=127.0.0.1 GOMAXPROCS=4 \
    taskset -c "$server_cpus" "$server_binary" -test.run '^TestExternalProxyQualificationServerProcess$' -test.timeout 120s >"$output_dir/$variant-server.log" 2>&1 &
  local server_group=$!; owned[$server_group]=1; stops+=("$stop"); wait_ready "$ready" "$server_group"
  local server_pid public_url server_admin management_url public_id token
  server_pid=$(jq -r .pid "$ready"); public_url=$(jq -r .url "$ready"); server_admin=$(jq -r .admin_url "$ready"); management_url=$(jq -r .management_url "$ready"); public_id=$(jq -r '.agents[0].public_id' "$ready"); token=$(jq -r '.agents[0].token' "$ready")
  local agent_ready=$output_dir/$variant-agent.ready.json agent_stop=$output_dir/$variant-agent.stop
  setsid env P2PSTREAM_QUALIFICATION_ROLE=agent-external P2PSTREAM_QUALIFICATION_READY="$agent_ready" P2PSTREAM_QUALIFICATION_STOP="$agent_stop" \
    P2PSTREAM_QUALIFICATION_MANAGEMENT_URL="$management_url" P2PSTREAM_QUALIFICATION_MANAGEMENT_CA_FILE="$ca" P2PSTREAM_QUALIFICATION_AGENT_PUBLIC_ID="$public_id" \
    P2PSTREAM_QUALIFICATION_AGENT_TOKEN="$token" P2PSTREAM_QUALIFICATION_AGENT_TUNNELS=4 GOMAXPROCS=4 taskset -c "$agent_cpus" "$agent_binary" \
    -test.run '^TestExternalProxyQualificationAgentProcess$' -test.timeout 120s >"$output_dir/$variant-agent.log" 2>&1 &
  local agent_group=$!; owned[$agent_group]=1; stops+=("$agent_stop"); wait_ready "$agent_ready" "$agent_group"
  local agent_admin; agent_admin=$(jq -r .admin_url "$agent_ready")
  wait_aggregate "$server_admin"
  capture_socket_pair "$variant" before-load "$server_admin" "$agent_admin" "$output_dir/$variant-sockets-before.json"
  run_transfer "$variant" download "$server_pid" "$public_url" "$server_admin"
  run_transfer "$variant" upload "$server_pid" "$public_url" "$server_admin"
  capture_socket_pair "$variant" after-load "$server_admin" "$agent_admin" "$output_dir/$variant-sockets-after.json"
  python3 - "$output_dir/$variant-sockets-before.json" "$output_dir/$variant-sockets-after.json" <<'PY'
import json,sys
before,after=(json.load(open(path)) for path in sys.argv[1:])
if before['server']['observations']!=after['server']['observations'] or before['agent']['observations']!=after['agent']['observations']:
    raise SystemExit('physical tunnel observation set changed during smoke load')
if before['reverse_tuple_matches']!=after['reverse_tuple_matches']:
    raise SystemExit('reverse tuple mapping changed during smoke load')
PY
  natural_stop "$agent_group" "$agent_stop" "$variant agent"
  natural_stop "$server_group" "$stop" "$variant server"
  jq -cn --arg variant "$variant" --arg server_sha "$(sha256sum "$server_binary"|awk '{print $1}')" --arg agent_sha "$(sha256sum "$agent_binary"|awk '{print $1}')" \
    '{variant:$variant,transport_mode:"aggregate-v1",physical_lanes:5,exact_download_4mib:true,exact_upload_4mib:true,socket_observations_stable:true,agent_natural_exit:true,server_natural_exit:true,server_sha256:$server_sha,agent_sha256:$agent_sha}' >> "$output_dir/results.jsonl"
}
run_variant tunnel-lowat-default-control "$control_server" "$control_agent"
run_variant tunnel-lowat-32k "$lowat_server" "$lowat_agent"
natural_stop "$origin_pid" "$origin_stop" origin
python3 - "$output_dir" <<'PY'
import json,pathlib,sys
root=pathlib.Path(sys.argv[1]); rows=[json.loads(x) for x in (root/'results.jsonl').read_text().splitlines()]
if [x['variant'] for x in rows]!=['tunnel-lowat-default-control','tunnel-lowat-32k']: raise SystemExit('variant order mismatch')
if not all(x['transport_mode']=='aggregate-v1' and x['physical_lanes']==5 and x['exact_download_4mib'] and x['exact_upload_4mib'] and x['socket_observations_stable'] and x['agent_natural_exit'] and x['server_natural_exit'] for x in rows): raise SystemExit('fixture result mismatch')
control=json.loads((root/'tunnel-lowat-default-control-sockets-before.json').read_text()); lowat=json.loads((root/'tunnel-lowat-32k-sockets-before.json').read_text())
for role in ('server','agent'):
  def values(value,key): return sorted(row['socket'][key] for row in value[role]['observations'])
  raw_control=values(control,'notsent_lowat_before'); raw_lowat=values(lowat,'notsent_lowat_before')
  ns_control=values(control,'namespace_notsent_lowat'); ns_lowat=values(lowat,'namespace_notsent_lowat')
  effective_control=sorted(row['socket']['notsent_lowat_before'] or row['socket']['namespace_notsent_lowat'] for row in control[role]['observations'])
  effective_lowat=sorted(row['socket']['notsent_lowat_before'] or row['socket']['namespace_notsent_lowat'] for row in lowat[role]['observations'])
  if raw_control!=raw_lowat or ns_control!=ns_lowat or effective_control!=effective_lowat or len(effective_control)!=5 or any(v<=32768 for v in effective_control):
    raise SystemExit(f'{role} LOWAT baseline contrast invalid')
summary={'scope':'fixture_only','performance_evidence':False,'workload':'one exact 4MiB H2 download and upload per arm solely to validate active physical tunnel settings and lifecycle','variants':rows,'origin_natural_exit':True,'cross_arm_lowat_baseline_contrast':True}
(root/'summary.json').write_text(json.dumps(summary,indent=2,sort_keys=True)+'\n')
PY
sha256sum "$0" > "$output_dir/adapter.sha256"
(cd "$output_dir" && find . -maxdepth 1 -type f ! -name SHA256SUMS ! -name 'public.key.pem' ! -name '*management-ca.pem' ! -name '*.ready.json' -printf '%P\0' | sort -z | xargs -0 sha256sum > SHA256SUMS)
printf 'fixture smoke PASS: %s\n' "$output_dir/summary.json"

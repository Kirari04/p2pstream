#!/usr/bin/env bash
# Fixture-only external process lifecycle smoke. No timing is performance evidence.
set -Eeuo pipefail
bundle=$1
output_dir=$2
mkdir -p "$output_dir"
for tool in setsid timeout jq python3 openssl sha256sum taskset; do command -v "$tool" >/dev/null; done

driver=$(realpath "$bundle/bin/driver-origin.test")
gathered_server=$(realpath "$bundle/bin/refund-gathered-control-server.test")
gathered_agent=$(realpath "$bundle/bin/refund-gathered-control-agent.test")
split_server=$(realpath "$bundle/bin/refund-split-control-server.test")
split_agent=$(realpath "$bundle/bin/refund-split-control-agent.test")
declare -A expected=(
  [driver]=e8e1e6da00f4bc616058488c572ed3bf144839432ad3445f68361b721dafce44
  [gathered_server]=e8e1e6da00f4bc616058488c572ed3bf144839432ad3445f68361b721dafce44
  [gathered_agent]=3fd7b305b5d90d7c9e6548c89ffd6bbac6dd1c38fbd870b188dd842b52198bd2
  [split_server]=197dcf54f5236e0114ec8f899f687f9c474e8c51c685490640413da658a8661d
  [split_agent]=04ba7219b6108bbcfbf6242c1316ce4cb287906f965448db54be90be25c3a36f
)
for name in driver gathered_server gathered_agent split_server split_agent; do
  binary=${!name}
  actual=$(sha256sum "$binary" | awk '{print $1}')
  [[ $actual == "${expected[$name]}" ]] || { echo "$name hash mismatch" >&2; exit 2; }
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
  for pid in "${!owned[@]}"; do
    kill -KILL -- "-$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
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
  for ((attempt=0;attempt<200;attempt++)); do
    kill -0 -- "-$pid" 2>/dev/null || break
    sleep .05
  done
  if kill -0 -- "-$pid" 2>/dev/null; then
    kill -TERM -- "-$pid" 2>/dev/null || true
    for ((attempt=0;attempt<50;attempt++)); do
      kill -0 -- "-$pid" 2>/dev/null || break
      sleep .1
    done
    kill -KILL -- "-$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    unset 'owned[$pid]'
    echo "$label required forced shutdown" >&2
    return 1
  fi
  local status
  set +e; wait "$pid"; status=$?; set -e
  unset 'owned[$pid]'
  ((status == 0)) || { echo "$label exited $status" >&2; return 1; }
}
wait_aggregate() {
  local admin=$1
  python3 - "$admin" <<'PY'
import json,sys,time,urllib.request
url=sys.argv[1]; deadline=time.monotonic()+30; last=None
while time.monotonic()<deadline:
    try:
        with urllib.request.urlopen(url,timeout=.5) as response: last=json.load(response).get('agent_tunnels',[])
        if len(last)==1 and last[0].get('connected') and last[0].get('flow_control')=='aggregate-v1' and last[0].get('control_connected') and last[0].get('bulk_sessions')==4:
            lanes=last[0].get('lanes',[])
            if len(lanes)==5 and sorted(x.get('lane') for x in lanes)==[0,1,2,3,4] and len([x for x in lanes if x.get('lane',0)>0 and x.get('aggregate')])==4:
                raise SystemExit(0)
    except Exception as error: last=repr(error)
    time.sleep(.05)
raise SystemExit(f'aggregate readiness failed: {last!r}')
PY
}

cert=$output_dir/public.crt.pem
key=$output_dir/public.key.pem
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj /CN=public.test -days 1 -keyout "$key" -out "$cert" >/dev/null 2>&1
origin_ready=$output_dir/origin.ready.json
origin_stop=$output_dir/origin.stop
setsid env P2PSTREAM_QUALIFICATION_ROLE=origin P2PSTREAM_QUALIFICATION_READY="$origin_ready" \
  P2PSTREAM_QUALIFICATION_STOP="$origin_stop" P2PSTREAM_QUALIFICATION_BIND_ADDRESS=127.0.0.1 \
  taskset -c "$agent_cpus" "$driver" -test.run '^TestProxyQualificationProcess$' -test.timeout 300s >"$output_dir/origin.log" 2>&1 &
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
    P2PSTREAM_QUALIFICATION_ROLE=load P2PSTREAM_QUALIFICATION_URL="$public_url/agent/$suffix" \
    P2PSTREAM_QUALIFICATION_SCENARIO="$scenario" P2PSTREAM_QUALIFICATION_PROTOCOL=h2 \
    P2PSTREAM_QUALIFICATION_CONCURRENCY=1 P2PSTREAM_QUALIFICATION_REQUESTS=1 \
    P2PSTREAM_QUALIFICATION_OFFERED_RPS=0 P2PSTREAM_QUALIFICATION_DURATION_SECONDS=0 \
    P2PSTREAM_QUALIFICATION_WARMUP_SECONDS=0 P2PSTREAM_QUALIFICATION_GROWTH_PAYLOAD_BYTES=$((4<<20)) \
    P2PSTREAM_QUALIFICATION_LABEL="fixture-$variant" P2PSTREAM_QUALIFICATION_PATH=refund-emission-external-smoke \
    P2PSTREAM_QUALIFICATION_PIDS="$server_pid" P2PSTREAM_QUALIFICATION_ADMIN_URL="$server_admin" \
    P2PSTREAM_QUALIFICATION_PROXY_CPU_SET="$server_cpus" P2PSTREAM_QUALIFICATION_SERVER_PROCESS="$server_spec" \
    P2PSTREAM_QUALIFICATION_LOAD_CPU_SET="$load_cpus" P2PSTREAM_QUALIFICATION_RESOURCE_ENVELOPE=p2pstream-fixture-only-loopback \
    P2PSTREAM_QUALIFICATION_SEQUENCE="$sequence" P2PSTREAM_QUALIFICATION_RESULT="$result" \
    taskset -c "$load_cpus" "$driver" -test.run '^TestProxyQualificationLoad$' -test.timeout 40s >"$result.log" 2>&1 &
  local load_group=$! load_status
  owned[$load_group]=1
  if wait "$load_group"; then load_status=0; else load_status=$?; fi
  unset 'owned[$load_group]'
  ((load_status == 0)) || { echo "$variant $direction load exited $load_status" >&2; return 1; }
  jq -e --arg direction "$direction" --argjson expected "$expected_response" '
    .attempted==1 and .useful_completed==1 and .errors==0 and .status_classes["2xx"]==1 and
    .configured_payload_bytes==(4*1024*1024) and .response_bytes==$expected and
    .event_stats_captured==true and .persisted_event_delta==1 and
    ((.dropped_event_delta//0)==0) and ((.event_queue_after_drain//0)==0) and ((.event_drain_error//"")=="") and
    (.agent_tunnels_final|length)==1 and .agent_tunnels_final[0].flow_control=="aggregate-v1"
  ' "$result" >/dev/null
}

run_variant() {
  local variant=$1 server_binary=$2 agent_binary=$3
  local ready=$output_dir/$variant-server.ready.json stop=$output_dir/$variant-server.stop ca=$output_dir/$variant-management-ca.pem
  setsid env P2PSTREAM_QUALIFICATION_ROLE=proxy-external-agent P2PSTREAM_QUALIFICATION_READY="$ready" \
    P2PSTREAM_QUALIFICATION_STOP="$stop" P2PSTREAM_QUALIFICATION_ORIGIN_URL="$origin_url" \
    P2PSTREAM_QUALIFICATION_CERT="$cert" P2PSTREAM_QUALIFICATION_KEY="$key" \
    P2PSTREAM_QUALIFICATION_MANAGEMENT_CA_FILE="$ca" P2PSTREAM_QUALIFICATION_EXTERNAL_AGENTS=1 \
    P2PSTREAM_QUALIFICATION_BIND_ADDRESS=127.0.0.1 GOMAXPROCS=4 \
    taskset -c "$server_cpus" "$server_binary" -test.run '^TestExternalProxyQualificationServerProcess$' -test.timeout 120s \
    >"$output_dir/$variant-server.log" 2>&1 &
  local server_group=$!; owned[$server_group]=1; stops+=("$stop"); wait_ready "$ready" "$server_group"
  local server_pid public_url server_admin management_url public_id token
  server_pid=$(jq -r .pid "$ready"); public_url=$(jq -r .url "$ready"); server_admin=$(jq -r .admin_url "$ready")
  management_url=$(jq -r .management_url "$ready"); public_id=$(jq -r '.agents[0].public_id' "$ready"); token=$(jq -r '.agents[0].token' "$ready")
  local agent_ready=$output_dir/$variant-agent.ready.json agent_stop=$output_dir/$variant-agent.stop
  setsid env P2PSTREAM_QUALIFICATION_ROLE=agent-external P2PSTREAM_QUALIFICATION_READY="$agent_ready" \
    P2PSTREAM_QUALIFICATION_STOP="$agent_stop" P2PSTREAM_QUALIFICATION_MANAGEMENT_URL="$management_url" \
    P2PSTREAM_QUALIFICATION_MANAGEMENT_CA_FILE="$ca" P2PSTREAM_QUALIFICATION_AGENT_PUBLIC_ID="$public_id" \
    P2PSTREAM_QUALIFICATION_AGENT_TOKEN="$token" P2PSTREAM_QUALIFICATION_AGENT_TUNNELS=4 GOMAXPROCS=4 \
    taskset -c "$agent_cpus" "$agent_binary" -test.run '^TestExternalProxyQualificationAgentProcess$' -test.timeout 120s \
    >"$output_dir/$variant-agent.log" 2>&1 &
  local agent_group=$!; owned[$agent_group]=1; stops+=("$agent_stop"); wait_ready "$agent_ready" "$agent_group"
  wait_aggregate "$server_admin"
  run_transfer "$variant" download "$server_pid" "$public_url" "$server_admin"
  run_transfer "$variant" upload "$server_pid" "$public_url" "$server_admin"
  natural_stop "$agent_group" "$agent_stop" "$variant agent"
  natural_stop "$server_group" "$stop" "$variant server"
  jq -cn --arg variant "$variant" --arg server_sha "$(sha256sum "$server_binary"|awk '{print $1}')" \
    --arg agent_sha "$(sha256sum "$agent_binary"|awk '{print $1}')" \
    '{variant:$variant,transport_mode:"aggregate-v1",physical_lanes:5,exact_download_4mib:true,exact_upload_4mib:true,agent_natural_exit:true,server_natural_exit:true,server_sha256:$server_sha,agent_sha256:$agent_sha}' \
    >> "$output_dir/results.jsonl"
}

run_variant gathered "$gathered_server" "$gathered_agent"
run_variant split "$split_server" "$split_agent"
natural_stop "$origin_pid" "$origin_stop" origin
python3 - "$output_dir/results.jsonl" "$output_dir/summary.json" <<'PY'
import json,sys
from pathlib import Path
rows=[json.loads(x) for x in Path(sys.argv[1]).read_text().splitlines()]
assert [x['variant'] for x in rows]==['gathered','split']
assert all(x['transport_mode']=='aggregate-v1' and x['physical_lanes']==5 and x['exact_download_4mib'] and x['exact_upload_4mib'] and x['agent_natural_exit'] and x['server_natural_exit'] for x in rows)
Path(sys.argv[2]).write_text(json.dumps({'scope':'fixture_only','performance_evidence':False,'workload':'one exact 4MiB download and upload per runtime solely to establish active bulk streams before shutdown','variants':rows,'origin_natural_exit':True},indent=2,sort_keys=True)+'\n')
PY
sha256sum "$0" > "$output_dir/adapter.sha256"
printf 'fixture smoke PASS: %s\n' "$output_dir/summary.json"

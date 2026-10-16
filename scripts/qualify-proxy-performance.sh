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
candidate_label="candidate"
output=""
use_cgroup=1
origin_tls=0
origin_http1=0
ballast_mib=0
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
while (($#)); do
  case "$1" in
    --smoke) smoke=1; runs=1; shift ;;
    --runs) runs=$2; shift 2 ;;
    --baseline-binary) baseline_binary=$2; shift 2 ;;
    --baseline-label) baseline_label=$2; shift 2 ;;
    --candidate-binary) candidate_binary=$2; shift 2 ;;
    --driver-binary) driver_binary=$2; shift 2 ;;
    --candidate-label) candidate_label=$2; shift 2 ;;
    --output) output=$2; shift 2 ;;
    --host-processes) use_cgroup=0; shift ;;
    --origin-tls) origin_tls=1; shift ;;
    --origin-http1) origin_http1=1; shift ;;
    --ballast-mib) ballast_mib=$2; shift 2 ;;
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
if ((${#available_cpus[@]} > proxy_cpu_count)); then
  load_cpu_set=$(IFS=,; echo "${available_cpus[*]:proxy_cpu_count}")
else
  load_cpu_set=$proxy_cpu_set
fi
if [[ ! $runs =~ ^[1-9][0-9]*$ ]]; then
  echo "--runs must be a positive integer" >&2
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
if [[ -n $baseline_binary ]]; then
  baseline_binary=$(realpath "$baseline_binary")
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
  process_container=""
  if [[ $role == proxy ]] && ((use_cgroup)); then
    process_container="p2pstream-qualification-${name//[^a-zA-Z0-9_.-]/-}-$$"
    containers+=("$process_container")
    docker run --rm --name "$process_container" --network host \
      --user "$(id -u):$(id -g)" \
      --memory 4g --memory-swap 4g --cpuset-cpus "$proxy_cpu_set" --pids-limit 8192 \
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
      --env GOMAXPROCS="$proxy_cpu_count" \
      --env P2PSTREAM_QUALIFICATION_BALLAST_MIB="$ballast_mib" \
      --env P2PSTREAM_QUALIFICATION_PROFILE_DIR="${profile_root:+/qualification/profiles/$name}" \
      --env P2PSTREAM_QUALIFICATION_PROFILE_MODE="$profile_mode" \
      --env P2PSTREAM_QUALIFICATION_STDLIB_MAX_IDLE="${stdlib_max_idle:-32}" \
      --env P2PSTREAM_QUALIFICATION_STDLIB_POOL="$stdlib_pool" \
      --env P2PSTREAM_QUALIFICATION_STDLIB_SHARDS="$stdlib_shards" \
      debian:trixie-slim /server.test -test.run '^TestProxyQualificationProcess$' -test.timeout 15m >"$log" 2>&1 &
  else
    role_cpu_set=$proxy_cpu_set
    [[ $role == origin ]] && role_cpu_set=$load_cpu_set
    P2PSTREAM_QUALIFICATION_ROLE="$role" \
    P2PSTREAM_QUALIFICATION_READY="$ready" \
    P2PSTREAM_QUALIFICATION_STOP="$stop" \
    P2PSTREAM_QUALIFICATION_ORIGIN_URL="${origin_url:-}" \
    P2PSTREAM_QUALIFICATION_ORIGIN_TLS="$origin_tls" \
    P2PSTREAM_QUALIFICATION_ORIGIN_HTTP1="$origin_http1" \
    P2PSTREAM_QUALIFICATION_CERT="$cert" \
    P2PSTREAM_QUALIFICATION_KEY="$key" \
    GOMAXPROCS="$proxy_cpu_count" \
    P2PSTREAM_QUALIFICATION_BALLAST_MIB="$ballast_mib" \
    P2PSTREAM_QUALIFICATION_PROFILE_DIR="${profile_root:+$profile_root/$name}" \
    P2PSTREAM_QUALIFICATION_PROFILE_MODE="$profile_mode" \
    P2PSTREAM_QUALIFICATION_STDLIB_MAX_IDLE="${stdlib_max_idle:-32}" \
    P2PSTREAM_QUALIFICATION_STDLIB_POOL="$stdlib_pool" \
    P2PSTREAM_QUALIFICATION_STDLIB_SHARDS="$stdlib_shards" \
      taskset -c "$role_cpu_set" "$binary" -test.run '^TestProxyQualificationProcess$' -test.timeout 15m >"$log" 2>&1 &
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
  P2PSTREAM_QUALIFICATION_LOAD_CPU_SET="$load_cpu_set" \
  P2PSTREAM_QUALIFICATION_RESOURCE_ENVELOPE="${resource_envelope:-}" \
  P2PSTREAM_QUALIFICATION_SEQUENCE="$sequence" \
  P2PSTREAM_QUALIFICATION_BALLAST_MIB="${result_ballast_mib:-0}" \
  P2PSTREAM_QUALIFICATION_RESULT="$result" \
    taskset -c "$load_cpu_set" "$driver_binary" -test.run '^TestProxyQualificationLoad$' -test.timeout 10m \
    >"$result.log" 2>&1
  jq -c --argjson run "$sequence" '. + {sequence: $run}' "$result" >> "$output"
}

sequence=0
run_p2pstream() {
  local label=$1 binary=$2 iteration=$3
  start_test_process "$binary" proxy "${label}-${iteration}"
  local pid=$process_pid stop=$process_stop
  local sample_pid=$process_sample_pid
  local base_url admin_url
  base_url=$(jq -r .url "$process_ready")
  admin_url=$(jq -r .admin_url "$process_ready")
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

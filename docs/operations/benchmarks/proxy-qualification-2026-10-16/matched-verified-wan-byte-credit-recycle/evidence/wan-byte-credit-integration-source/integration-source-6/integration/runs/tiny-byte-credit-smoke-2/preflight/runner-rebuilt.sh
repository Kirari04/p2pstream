#!/usr/bin/env bash
# Finite negotiated-agent growth qualification. The public client and server
# share one disposable namespace; the real agent and its origin use a second,
# with all tunnel traffic crossing an 80 ms / 1 Gbit/s routed path.
set -Eeuo pipefail

if [[ ${1:-} != --inside ]]; then
  cd "$(dirname "$0")/.."
  server_binary=""
  aggregate_agent_binary=""
  output=""
  runs=2
  warmup_mib=256
  steady_mib=1024
  transfer_repetitions=3
  required_pool_mib=8
  configured_rtt_ms=80
  server_source_ref=""
  aggregate_agent_source_ref=""
  protocol=""
  cert=""
  key=""
  expected_leaf_sha256=""
  copy_record_validator=""
  copy_record_validator_sha256=""
  sender_wait_validator=""
  sender_wait_validator_sha256=""
  sender_wait_small=0
  smoke_mode=0
  pressure_mode=0
  tiny_response_mode=0
  while (($#)); do
    case "$1" in
      --server-binary) server_binary=$2; shift 2 ;;
      --aggregate-agent-binary) aggregate_agent_binary=$2; shift 2 ;;
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
      --server-source-ref) server_source_ref=$2; shift 2 ;;
      --aggregate-agent-source-ref) aggregate_agent_source_ref=$2; shift 2 ;;
      --protocol) protocol=$2; shift 2 ;;
      --cert) cert=$2; shift 2 ;;
      --key) key=$2; shift 2 ;;
      --expected-leaf-sha256) expected_leaf_sha256=$2; shift 2 ;;
      --copy-record-validator) copy_record_validator=$2; shift 2 ;;
      --copy-record-validator-sha256) copy_record_validator_sha256=$2; shift 2 ;;
      --sender-wait-validator) sender_wait_validator=$2; shift 2 ;;
      --sender-wait-validator-sha256) sender_wait_validator_sha256=$2; shift 2 ;;
      --sender-wait-small) sender_wait_small=1; shift ;;
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
  [[ -x $server_binary && -x $aggregate_agent_binary ]] || {
    echo "executable --server-binary and --aggregate-agent-binary are required" >&2
    exit 2
  }
  [[ $runs == 1 ]] || { echo "WAN protocol diagnostic requires exactly --runs 1" >&2; exit 2; }
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
  ((required_pool_mib > 2 || ((smoke_mode == 1 || sender_wait_small == 1) && required_pool_mib == 2))) || {
    echo "--required-pool-mib must be greater than the 2 MiB initial pool (exactly 2 is allowed only with --smoke or --sender-wait-small)" >&2
    exit 2
  }
  if ((sender_wait_small)); then
    ((smoke_mode == 0 && pressure_mode == 0 && tiny_response_mode == 0 && runs == 1 &&
      warmup_mib == 4 && steady_mib == 4 && transfer_repetitions == 3 && required_pool_mib == 2)) || {
      echo "--sender-wait-small requires the fixed 16-row 4/4 MiB, three-repetition, 2 MiB-pool schedule" >&2; exit 2;
    }
  fi
  ((pressure_mode == 0 || tiny_response_mode == 0)) || {
    echo "--pressure and --tiny-response are mutually exclusive" >&2
    exit 2
  }
  ((smoke_mode == 0 || tiny_response_mode == 0)) || {
    echo "--smoke and --tiny-response are mutually exclusive" >&2
    exit 2
  }
  [[ -n $server_source_ref && -n $aggregate_agent_source_ref ]] || {
    echo "--server-source-ref and --aggregate-agent-source-ref are required" >&2
    exit 2
  }
  [[ $protocol == h1 || $protocol == h2 ]] || { echo "--protocol must be h1 or h2" >&2; exit 2; }
  [[ -f $cert && -f $key ]] || { echo "--cert and --key are required" >&2; exit 2; }
  [[ $expected_leaf_sha256 =~ ^[0-9a-f]{64}$ ]] || { echo "--expected-leaf-sha256 must be lowercase SHA-256 hex" >&2; exit 2; }
  [[ -n $copy_record_validator && $copy_record_validator_sha256 =~ ^[0-9a-f]{64}$ ]] || {
    echo "--copy-record-validator and its lowercase SHA-256 are required" >&2; exit 2;
  }
  copy_record_validator=$(realpath "$copy_record_validator")
  [[ -f $copy_record_validator && -x $copy_record_validator ]] || {
    echo "copy record validator must be an executable regular file" >&2; exit 2;
  }
  [[ $(sha256sum "$copy_record_validator" | awk '{print $1}') == "$copy_record_validator_sha256" ]] || {
    echo "copy record validator hash mismatch" >&2; exit 2;
  }
  [[ -f $sender_wait_validator && $sender_wait_validator_sha256 =~ ^[0-9a-f]{64}$ ]] || {
    echo "--sender-wait-validator and its lowercase SHA-256 are required" >&2; exit 2;
  }
  [[ $(sha256sum "$sender_wait_validator" | awk '{print $1}') == "$sender_wait_validator_sha256" ]] || {
    echo "sender-wait validator hash mismatch" >&2; exit 2;
  }
  mkdir -p tmp/proxy-agent-growth
  [[ -n $output ]] || output=tmp/proxy-agent-growth/results.jsonl
  export P2PSTREAM_GROWTH_SERVER_BINARY P2PSTREAM_GROWTH_AGGREGATE_AGENT_BINARY
  export P2PSTREAM_GROWTH_OUTPUT P2PSTREAM_GROWTH_RUNS P2PSTREAM_GROWTH_PARENT_NETNS
  export P2PSTREAM_GROWTH_WARMUP_MIB P2PSTREAM_GROWTH_STEADY_MIB P2PSTREAM_GROWTH_TRANSFER_REPETITIONS P2PSTREAM_GROWTH_REQUIRED_MIB
  export P2PSTREAM_GROWTH_SERVER_SOURCE_REF P2PSTREAM_GROWTH_AGGREGATE_AGENT_SOURCE_REF
  export P2PSTREAM_GROWTH_PROTOCOL P2PSTREAM_GROWTH_CERT P2PSTREAM_GROWTH_KEY P2PSTREAM_GROWTH_EXPECTED_LEAF_SHA256
  export P2PSTREAM_GROWTH_COPY_RECORD_VALIDATOR P2PSTREAM_GROWTH_COPY_RECORD_VALIDATOR_SHA256
  export P2PSTREAM_GROWTH_SENDER_WAIT_VALIDATOR P2PSTREAM_GROWTH_SENDER_WAIT_VALIDATOR_SHA256 P2PSTREAM_GROWTH_SENDER_WAIT_SMALL
  export P2PSTREAM_GROWTH_SMOKE_MODE
  export P2PSTREAM_GROWTH_PRESSURE_MODE
  export P2PSTREAM_GROWTH_TINY_RESPONSE_MODE
  export P2PSTREAM_GROWTH_CONFIGURED_RTT_MS
  export P2PSTREAM_GROWTH_SERVER_SCOPE_PROCS P2PSTREAM_GROWTH_SERVER_SCOPE_CGROUP
  export P2PSTREAM_GROWTH_SERVER_SCOPE_CONTROL_DIR P2PSTREAM_GROWTH_SERVER_SCOPE_NONCE
  export P2PSTREAM_GROWTH_SERVER_SCOPE_ENTRYPOINT
  P2PSTREAM_GROWTH_SERVER_BINARY=$(realpath "$server_binary")
  P2PSTREAM_GROWTH_AGGREGATE_AGENT_BINARY=$(realpath "$aggregate_agent_binary")
  P2PSTREAM_GROWTH_OUTPUT=$(realpath -m "$output")
  P2PSTREAM_GROWTH_RUNS=$runs
  P2PSTREAM_GROWTH_WARMUP_MIB=$warmup_mib
  P2PSTREAM_GROWTH_STEADY_MIB=$steady_mib
  P2PSTREAM_GROWTH_TRANSFER_REPETITIONS=$transfer_repetitions
  P2PSTREAM_GROWTH_REQUIRED_MIB=$required_pool_mib
  P2PSTREAM_GROWTH_SERVER_SOURCE_REF=$server_source_ref
  P2PSTREAM_GROWTH_AGGREGATE_AGENT_SOURCE_REF=$aggregate_agent_source_ref
  P2PSTREAM_GROWTH_PROTOCOL=$protocol
  P2PSTREAM_GROWTH_CERT=$(realpath "$cert")
  P2PSTREAM_GROWTH_KEY=$(realpath "$key")
  P2PSTREAM_GROWTH_EXPECTED_LEAF_SHA256=$expected_leaf_sha256
  P2PSTREAM_GROWTH_COPY_RECORD_VALIDATOR=$copy_record_validator
  P2PSTREAM_GROWTH_COPY_RECORD_VALIDATOR_SHA256=$copy_record_validator_sha256
  P2PSTREAM_GROWTH_SENDER_WAIT_VALIDATOR=$(realpath "$sender_wait_validator")
  P2PSTREAM_GROWTH_SENDER_WAIT_VALIDATOR_SHA256=$sender_wait_validator_sha256
  P2PSTREAM_GROWTH_SENDER_WAIT_SMALL=$sender_wait_small
  P2PSTREAM_GROWTH_SMOKE_MODE=$smoke_mode
  P2PSTREAM_GROWTH_PRESSURE_MODE=$pressure_mode
  P2PSTREAM_GROWTH_TINY_RESPONSE_MODE=$tiny_response_mode
  P2PSTREAM_GROWTH_CONFIGURED_RTT_MS=$configured_rtt_ms
  P2PSTREAM_GROWTH_PARENT_NETNS=$(readlink /proc/self/ns/net)
  if unshare --user --map-root-user --net true 2>/dev/null; then
    exec unshare --user --map-root-user --net "$0" --inside
  fi
  exec sudo -n --preserve-env=P2PSTREAM_GROWTH_SERVER_BINARY,P2PSTREAM_GROWTH_AGGREGATE_AGENT_BINARY,P2PSTREAM_GROWTH_OUTPUT,P2PSTREAM_GROWTH_RUNS,P2PSTREAM_GROWTH_PARENT_NETNS,P2PSTREAM_GROWTH_WARMUP_MIB,P2PSTREAM_GROWTH_STEADY_MIB,P2PSTREAM_GROWTH_TRANSFER_REPETITIONS,P2PSTREAM_GROWTH_REQUIRED_MIB,P2PSTREAM_GROWTH_SERVER_SOURCE_REF,P2PSTREAM_GROWTH_AGGREGATE_AGENT_SOURCE_REF,P2PSTREAM_GROWTH_PROTOCOL,P2PSTREAM_GROWTH_CERT,P2PSTREAM_GROWTH_KEY,P2PSTREAM_GROWTH_EXPECTED_LEAF_SHA256,P2PSTREAM_GROWTH_COPY_RECORD_VALIDATOR,P2PSTREAM_GROWTH_COPY_RECORD_VALIDATOR_SHA256,P2PSTREAM_GROWTH_SENDER_WAIT_VALIDATOR,P2PSTREAM_GROWTH_SENDER_WAIT_VALIDATOR_SHA256,P2PSTREAM_GROWTH_SENDER_WAIT_SMALL,P2PSTREAM_GROWTH_SMOKE_MODE,P2PSTREAM_GROWTH_PRESSURE_MODE,P2PSTREAM_GROWTH_TINY_RESPONSE_MODE,P2PSTREAM_GROWTH_CONFIGURED_RTT_MS,P2PSTREAM_GROWTH_SERVER_SCOPE_PROCS,P2PSTREAM_GROWTH_SERVER_SCOPE_CGROUP,P2PSTREAM_GROWTH_SERVER_SCOPE_CONTROL_DIR,P2PSTREAM_GROWTH_SERVER_SCOPE_NONCE,P2PSTREAM_GROWTH_SERVER_SCOPE_ENTRYPOINT \
    unshare --net "$0" --inside
fi

[[ -n ${P2PSTREAM_GROWTH_PARENT_NETNS:-} && $(readlink /proc/self/ns/net) != "$P2PSTREAM_GROWTH_PARENT_NETNS" ]] || {
  echo "refusing to configure networking outside a disposable namespace" >&2
  exit 1
}
for tool in awk ip tc nsenter jq python3 taskset openssl sha256sum ping ps readlink setsid; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 1; }
done
copy_record_validator=${P2PSTREAM_GROWTH_COPY_RECORD_VALIDATOR:?copy record validator is required}
copy_record_validator_sha256=${P2PSTREAM_GROWTH_COPY_RECORD_VALIDATOR_SHA256:?copy record validator hash is required}
[[ $copy_record_validator == /* && -f $copy_record_validator && -x $copy_record_validator &&
   $copy_record_validator_sha256 =~ ^[0-9a-f]{64}$ ]] || {
	echo "copy record validator binding is unavailable" >&2; exit 2;
}
[[ $(readlink -f "$copy_record_validator") == "$copy_record_validator" ]] || {
	echo "copy record validator path changed across namespace re-exec" >&2; exit 2;
}
[[ $(sha256sum "$copy_record_validator" | awk '{print $1}') == "$copy_record_validator_sha256" ]] || {
	echo "copy record validator changed across namespace re-exec" >&2; exit 2;
}
if [[ ${P2PSTREAM_GROWTH_PRESSURE_MODE:-0} == 1 ]]; then
  command -v systemd-run >/dev/null || { echo "systemd-run is required for --pressure" >&2; exit 1; }
fi

server_binary=$P2PSTREAM_GROWTH_SERVER_BINARY
aggregate_agent_binary=$P2PSTREAM_GROWTH_AGGREGATE_AGENT_BINARY
output=$P2PSTREAM_GROWTH_OUTPUT
runs=$P2PSTREAM_GROWTH_RUNS
warmup_mib=$P2PSTREAM_GROWTH_WARMUP_MIB
steady_mib=$P2PSTREAM_GROWTH_STEADY_MIB
transfer_repetitions=$P2PSTREAM_GROWTH_TRANSFER_REPETITIONS
required_pool_mib=$P2PSTREAM_GROWTH_REQUIRED_MIB
server_source_ref=$P2PSTREAM_GROWTH_SERVER_SOURCE_REF
aggregate_agent_source_ref=$P2PSTREAM_GROWTH_AGGREGATE_AGENT_SOURCE_REF
protocol=$P2PSTREAM_GROWTH_PROTOCOL
cert=$P2PSTREAM_GROWTH_CERT
key=$P2PSTREAM_GROWTH_KEY
expected_leaf_sha256=$P2PSTREAM_GROWTH_EXPECTED_LEAF_SHA256
sender_wait_small=${P2PSTREAM_GROWTH_SENDER_WAIT_SMALL:-0}
smoke_mode=$P2PSTREAM_GROWTH_SMOKE_MODE
pressure_mode=${P2PSTREAM_GROWTH_PRESSURE_MODE:-0}
tiny_response_mode=${P2PSTREAM_GROWTH_TINY_RESPONSE_MODE:-0}
configured_rtt_ms=${P2PSTREAM_GROWTH_CONFIGURED_RTT_MS:-80}
server_scope_procs=${P2PSTREAM_GROWTH_SERVER_SCOPE_PROCS:?server scope cgroup.procs is required}
server_scope_cgroup=${P2PSTREAM_GROWTH_SERVER_SCOPE_CGROUP:?server scope cgroup path is required}
server_scope_control_dir=${P2PSTREAM_GROWTH_SERVER_SCOPE_CONTROL_DIR:?server scope control directory is required}
server_scope_nonce=${P2PSTREAM_GROWTH_SERVER_SCOPE_NONCE:?server scope nonce is required}
server_scope_entrypoint=${P2PSTREAM_GROWTH_SERVER_SCOPE_ENTRYPOINT:?server scope entrypoint is required}
[[ -w $server_scope_procs && -d $server_scope_control_dir && -x $server_scope_entrypoint ]] || {
  echo "server scope join/control contract is unavailable" >&2
  exit 2
}
[[ $server_scope_nonce =~ ^[0-9a-f-]{36}$ ]] || { echo "server scope nonce is malformed" >&2; exit 2; }
((pressure_mode == 0 && tiny_response_mode == 0)) || {
  echo "owned WAN pair derivative supports only the original large-transfer workload" >&2
  exit 2
}
largest_transfer_mib=$((warmup_mib > steady_mib ? warmup_mib : steady_mib))
transfer_timeout_seconds=$((60 + (largest_transfer_mib + 1) / 2))
warmup_lane_rounds=4
transfer_mib_per_cohort=$((2 * (warmup_lane_rounds + 1) * warmup_mib + 2 * transfer_repetitions * steady_mib))
overall_timeout_seconds=$((300 + runs * 2 * (transfer_mib_per_cohort + 1) / 2 + pressure_mode * 600))
if ((tiny_response_mode)); then overall_timeout_seconds=1200; fi
mkdir -p "$(dirname "$output")"
run_dir=$(mktemp -d -p "$(dirname "$output")" proxy-agent-growth.XXXXXX)
: > "$output"
sender_wait_control_dir=${output%.jsonl}.sender-wait-controls
[[ ! -e $sender_wait_control_dir ]] || { echo "sender-wait artifact directory already exists" >&2; exit 2; }
mkdir -m 0700 "$sender_wait_control_dir"
sender_wait_bearer=$(openssl rand -hex 32)
sender_wait_sequences=9,11,14,15
sender_wait_records=${output%.jsonl}.sender-wait-records.jsonl
[[ ! -e $sender_wait_records ]] || { echo "sender-wait record already exists" >&2; exit 2; }
: > "$sender_wait_records"
sender_wait_validator=${P2PSTREAM_GROWTH_SENDER_WAIT_VALIDATOR:?sender-wait validator is required}
sender_wait_validator_sha256=${P2PSTREAM_GROWTH_SENDER_WAIT_VALIDATOR_SHA256:?sender-wait validator hash is required}
[[ -f $sender_wait_validator && $sender_wait_validator_sha256 =~ ^[0-9a-f]{64}$ ]] || {
	echo "sender-wait validator binding is unavailable" >&2; exit 2;
}
[[ $(sha256sum "$sender_wait_validator" | awk '{print $1}') == "$sender_wait_validator_sha256" ]] || {
	echo "sender-wait validator changed across namespace re-exec" >&2; exit 2;
}
((smoke_mode == 0)) || { echo "sender-wait diagnostic has only the fixed sustained sequence set" >&2; exit 2; }

python3 - "$server_scope_control_dir/runner-owned-namespaces.json" "$$" <<'PY'
import json, os, pathlib, sys
out, shell_pid = sys.argv[1:]
value = {
    "schema_version": 1,
    "pid": int(shell_pid),
    "nspid": [int(v) for v in next(line.split()[1:] for line in pathlib.Path("/proc/self/status").read_text().splitlines() if line.startswith("NSpid:"))],
    "namespaces": {name: os.readlink(f"/proc/self/ns/{name}") for name in ("user", "pid", "mnt", "net", "cgroup")},
}
with pathlib.Path(out).open("x", encoding="utf-8") as stream:
    json.dump(value, stream, indent=2, sort_keys=True)
    stream.write("\n")
PY

declare -A owned_groups=()
stops=()
cleanup() {
  local original_status=$? forced=0 invalid=0 pid attempt live label
  local wait_status final_probe_status term_status kill_status record_status
  local term_attempted kill_attempted final_group_exists group_forced invalidates
  local -A cleanup_term_attempted=() cleanup_term_status=()
  local -A cleanup_kill_attempted=() cleanup_kill_status=()
  trap - EXIT INT TERM
  for stop in "${stops[@]:-}"; do [[ -n $stop ]] && touch "$stop"; done
  for pid in "${!owned_groups[@]}"; do
    cleanup_term_attempted[$pid]=false
    cleanup_term_status[$pid]=null
    cleanup_kill_attempted[$pid]=false
    cleanup_kill_status[$pid]=null
    if kill -0 -- "-$pid" 2>/dev/null; then
      forced=1
      cleanup_term_attempted[$pid]=true
      set +e
      kill -TERM -- "-$pid" 2>/dev/null
      cleanup_term_status[$pid]=$?
      set -e
    fi
  done
  for ((attempt=0; attempt<50; attempt++)); do
    live=0
    for pid in "${!owned_groups[@]}"; do kill -0 -- "-$pid" 2>/dev/null && live=1; done
    ((live)) || break
    sleep .1
  done
  for pid in "${!owned_groups[@]}"; do
    if kill -0 -- "-$pid" 2>/dev/null; then
      forced=1
      cleanup_kill_attempted[$pid]=true
      set +e
      kill -KILL -- "-$pid" 2>/dev/null
      cleanup_kill_status[$pid]=$?
      set -e
    fi
    set +e
    wait "$pid" 2>/dev/null
    wait_status=$?
    kill -0 -- "-$pid" 2>/dev/null
    final_probe_status=$?
    set -e
    term_attempted=${cleanup_term_attempted[$pid]}
    term_status=${cleanup_term_status[$pid]}
    kill_attempted=${cleanup_kill_attempted[$pid]}
    kill_status=${cleanup_kill_status[$pid]}
    final_group_exists=false
    ((final_probe_status == 0)) && final_group_exists=true
    group_forced=false
    [[ $term_attempted == true || $kill_attempted == true ]] && group_forced=true
    invalidates=false
    if [[ $group_forced == true || $final_group_exists == true ]] || ((wait_status != 0)); then
      invalidates=true
      invalid=1
    fi
    label=${owned_groups[$pid]}
    set +e
    jq -cn --arg label "$label" --argjson pid "$pid" --argjson status "$wait_status" \
      --argjson forced "$group_forced" --argjson term_attempted "$term_attempted" \
      --argjson term_status "$term_status" --argjson kill_attempted "$kill_attempted" \
      --argjson kill_status "$kill_status" --argjson final_probe_status "$final_probe_status" \
      --argjson final_group_exists "$final_group_exists" --argjson invalidates "$invalidates" \
      '{event:"process-exit",cleanup:true,label:$label,pid:$pid,status:$status,forced:$forced,
        term:{attempted:$term_attempted,status:$term_status},
        kill:{attempted:$kill_attempted,status:$kill_status},
        final_group_probe_status:$final_probe_status,group_joined:($final_group_exists|not),
        final_group_exists:$final_group_exists,invalidates_success:$invalidates}' \
      >> "${lifecycle_output:-/dev/null}"
    record_status=$?
    set -e
    if ((record_status != 0)); then
      invalid=1
      printf 'failed to retain cleanup lifecycle for %s pid %s (status %s)\n' "$label" "$pid" "$record_status" >&2
    fi
  done
  if ((forced)); then
    printf '{"event":"forced-cleanup","invalidates_success":true}\n' >> "${lifecycle_output:-/dev/null}"
  fi
  if ((forced || invalid)); then
    ((original_status != 0)) || original_status=1
  fi
  exit "$original_status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

register_owned_group() {
  local pid=$1 label=$2 pgid attempt
  owned_groups[$pid]=$label
  for ((attempt=0; attempt<100; attempt++)); do
    pgid=$(ps -o pgid= -p "$pid" 2>/dev/null | tr -d ' ' || true)
    [[ $pgid == "$pid" ]] && return 0
    kill -0 "$pid" 2>/dev/null || break
    sleep .01
  done
  [[ $pgid == "$pid" ]] || { echo "$label pid $pid does not own process group $pgid" >&2; return 1; }
}
process_start_state() {
  python3 - "$1" <<'PY'
import os, sys
pid = int(sys.argv[1])
text = open(f"/proc/{pid}/stat", encoding="utf-8").read()
right = text.rfind(")")
if right < 0:
    raise SystemExit("malformed process stat")
fields = text[right + 2:].split()
if len(fields) < 20:
    raise SystemExit("short process stat")
print(fields[19], os.readlink(f"/proc/{pid}/ns/net"))
PY
}
assert_namespace_helper() {
  local pid=$1 expected=$2 current
  current=$(process_start_state "$pid") || return 1
  [[ $current == "$expected" && $(tr -d '\n' < /proc/sys/kernel/random/boot_id) == "$boot_id" ]] || {
    echo "network namespace helper $pid changed process instance or namespace" >&2
    return 1
  }
}
lifecycle_output=${output%.jsonl}.lifecycle.jsonl
: > "$lifecycle_output"
boot_id=$(tr -d '\n' < /proc/sys/kernel/random/boot_id)
runner_namespace_identity=$(readlink /proc/self/ns/net)
agent_namespace_stop=$run_dir/agent-namespace.stop
router_namespace_stop=$run_dir/router-namespace.stop
setsid unshare --net bash -c 'namespace_holder() { local stop=$1 timeout=$2; python3 - "$stop" "$timeout" <<'"'"'PY'"'"'
import pathlib, sys, time
stop = pathlib.Path(sys.argv[1])
deadline = time.monotonic() + int(sys.argv[2])
while not stop.exists():
    if time.monotonic() >= deadline:
        raise SystemExit("namespace holder timed out")
    time.sleep(.05)
PY
}; namespace_holder "$1" "$2"' bash "$agent_namespace_stop" "$overall_timeout_seconds" &
agent_namespace_pid=$!
register_owned_group "$agent_namespace_pid" agent-namespace
stops+=("$agent_namespace_stop")
setsid unshare --net bash -c 'namespace_holder() { local stop=$1 timeout=$2; python3 - "$stop" "$timeout" <<'"'"'PY'"'"'
import pathlib, sys, time
stop = pathlib.Path(sys.argv[1])
deadline = time.monotonic() + int(sys.argv[2])
while not stop.exists():
    if time.monotonic() >= deadline:
        raise SystemExit("namespace holder timed out")
    time.sleep(.05)
PY
}; namespace_holder "$1" "$2"' bash "$router_namespace_stop" "$overall_timeout_seconds" &
router_namespace_pid=$!
register_owned_group "$router_namespace_pid" router-namespace
stops+=("$router_namespace_stop")
for pid in "$agent_namespace_pid" "$router_namespace_pid"; do
  for ((attempt=0; attempt<100; attempt++)); do
    [[ $(readlink "/proc/$pid/ns/net") != "$(readlink /proc/self/ns/net)" ]] && break
    sleep .01
  done
  [[ $(readlink "/proc/$pid/ns/net") != "$(readlink /proc/self/ns/net)" ]]
done
agent_namespace_instance=$(process_start_state "$agent_namespace_pid")
router_namespace_instance=$(process_start_state "$router_namespace_pid")
agent_namespace_start_ticks=${agent_namespace_instance%% *}
router_namespace_start_ticks=${router_namespace_instance%% *}
agent_namespace_identity=${agent_namespace_instance#* }
router_namespace_identity=${router_namespace_instance#* }
[[ $agent_namespace_identity != "$router_namespace_identity" && $agent_namespace_identity != "$runner_namespace_identity" && $router_namespace_identity != "$runner_namespace_identity" ]]
printf '{"event":"namespace-helpers-start","boot_id":"%s","runner_namespace":"%s","agent":{"pid":%s,"start_ticks":%s,"namespace":"%s"},"router":{"pid":%s,"start_ticks":%s,"namespace":"%s"}}\n' \
  "$boot_id" "$runner_namespace_identity" "$agent_namespace_pid" "$agent_namespace_start_ticks" "$agent_namespace_identity" \
  "$router_namespace_pid" "$router_namespace_start_ticks" "$router_namespace_identity" >> "$lifecycle_output"
agent_ns() { assert_namespace_helper "$agent_namespace_pid" "$agent_namespace_instance" && nsenter -t "$agent_namespace_pid" -n "$@"; }
router_ns() { assert_namespace_helper "$router_namespace_pid" "$router_namespace_instance" && nsenter -t "$router_namespace_pid" -n "$@"; }
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

server_binary_sha256=$(sha256sum "$server_binary" | awk '{print $1}')
aggregate_agent_binary_sha256=$(sha256sum "$aggregate_agent_binary" | awk '{print $1}')
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
  local label=$1 pid=$2 stop=$3 attempt status group_joined=false
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
    kill -0 -- "-$pid" 2>/dev/null && kill -KILL -- "-$pid" 2>/dev/null || true
    set +e
    wait "$pid" 2>/dev/null
    status=$?
    set -e
    for ((attempt=0; attempt<50; attempt++)); do
      kill -0 -- "-$pid" 2>/dev/null || { group_joined=true; break; }
      sleep .1
    done
    [[ $group_joined == false ]] || unset 'owned_groups[$pid]'
    printf '{"event":"process-exit","label":"%s","pid":%s,"status":%s,"forced":true,"group_joined":%s,"invalidates_success":true}\n' "$label" "$pid" "$status" "$group_joined" >> "$lifecycle_output"
    echo "$label required forced process-group cleanup" >&2
    return 1
  fi
  set +e
  wait "$pid"
  status=$?
  set -e
  unset 'owned_groups[$pid]'
  printf '{"event":"process-exit","label":"%s","pid":%s,"status":%s,"forced":false,"group_joined":true,"invalidates_success":%s}\n' \
    "$label" "$pid" "$status" "$([[ $status == 0 ]] && printf false || printf true)" >> "$lifecycle_output"
  ((status == 0)) || { echo "$label exited with status $status" >&2; return "$status"; }
}
query_agent_json() {
  local pid=$1 url=$2
  agent_ns python3 - "$url" <<'PY'
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

query_completed_copy_json() {
  local url=$1
  python3 - "$url" <<'PY'
import json, sys, time, urllib.request
deadline = time.monotonic() + 5
last = None
last_error = None
while time.monotonic() < deadline:
    try:
        with urllib.request.urlopen(sys.argv[1], timeout=.5) as response:
            last = json.load(response)
        matches = last.get("matches", []) if isinstance(last, dict) else []
        if len(matches) != 1 or matches[0].get("copy", {}).get("complete") is True:
            json.dump(last, sys.stdout, separators=(",", ":"))
            raise SystemExit(0)
    except SystemExit:
        raise
    except Exception as error:
        last_error = repr(error)
    time.sleep(.01)
if last is not None:
    json.dump(last, sys.stdout, separators=(",", ":"))
    raise SystemExit(0)
raise SystemExit(f"copy attribution query failed before any snapshot: {last_error}")
PY
}

wan_sender_wait_query() {
	local role=$1 action=$2 sequence=$3 admin_url=$4 destination=$5 timeout_seconds
	case "$role" in
		agent-drain) timeout_seconds=2 ;;
		server-capacity) timeout_seconds=5 ;;
		*) echo "invalid sender-wait query role" >&2; return 2 ;;
	esac
	local query_status
	local -a query_command=(python3)
	if [[ $role == agent-drain ]]; then
		query_command=(agent_ns python3)
	fi
	if "${query_command[@]}" /dev/fd/3 "$role" "$action" "$sequence" "$admin_url" "$destination" "$timeout_seconds" \
		3<<'PY' <<<"$sender_wait_bearer"
import http.client, json, pathlib, sys, time
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit

role, action, sequence_text, admin, destination, timeout_text = sys.argv[1:]
sequence = int(sequence_text)
bearer = sys.stdin.read().strip()
started_unix_ns = time.time_ns()
started_monotonic_ns = time.monotonic_ns()
status = 0
body = None
error = ""
response_bytes = 0
connection = None
try:
    parsed = urlsplit(admin)
    if parsed.scheme != "http" or parsed.hostname not in ("127.0.0.1", "localhost"):
        raise ValueError("sender-wait admin URL is not namespace loopback HTTP")
    query = dict(parse_qsl(parsed.query))
    if role == "agent-drain":
        query["wan_sender_wait_snapshot"] = "drain"
    else:
        query["wan_sender_wait_server_capacity"] = "1"
    query["sequence"] = str(sequence)
    path = urlunsplit(("", "", parsed.path or "/", urlencode(query), ""))
    connection = http.client.HTTPConnection(parsed.hostname, parsed.port, timeout=float(timeout_text))
    connection.request("GET", path, headers={"Accept": "application/json", "Authorization": "Bearer " + bearer})
    response = connection.getresponse()
    status = response.status
    raw = response.read((4 << 20) + 1)
    response_bytes = len(raw)
    if len(raw) > (4 << 20):
        raise ValueError("sender-wait response exceeds 4 MiB bound")
    body = json.loads(raw)
except BaseException as exc:
    error = repr(exc)
finally:
    if connection is not None:
        connection.close()
    bearer = ""
finished_unix_ns = time.time_ns()
record = {
    "schema_version": 1, "role": role, "action": action, "sequence": sequence,
    "request_started_unix_ns": started_unix_ns,
    "request_finished_unix_ns": finished_unix_ns,
    "request_elapsed_ns": time.monotonic_ns() - started_monotonic_ns,
    "http_status": status, "response_bytes": response_bytes,
    "response": body, "error": error, "authorization": "bearer-present",
}
with pathlib.Path(destination).open("x", encoding="utf-8") as stream:
    json.dump(record, stream, indent=2, sort_keys=True)
    stream.write("\n")
valid = status == 200 and error == "" and isinstance(body, dict)
if role == "agent-drain":
    valid = valid and body.get("schema_version") == 2 and body.get("capacity") == 4096
else:
    valid = valid and body.get("schema_version") == 1 and body.get("sequence") == str(sequence)
raise SystemExit(0 if valid else 1)
PY
	then
		query_status=0
	else
		query_status=$?
	fi
	if [[ ! -e $destination ]]; then
		jq -cn --arg role "$role" --arg action "$action" --argjson sequence "$sequence" --argjson status "$query_status" \
			'{schema_version:1,role:$role,action:$action,sequence:$sequence,http_status:0,response_bytes:0,
			  response:null,error:"query process exited before publishing its bounded response envelope",
			  authorization:"bearer-present",query_process_status:$status}' > "$destination"
	fi
	return "$query_status"
}

wan_sender_wait_server_capacity() {
	wan_sender_wait_query server-capacity "$1-capacity" "$1" "$2" "$3"
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
def scalar(name):
    value = (root / name).read_text().strip()
    return value if value == "max" else int(value)
def pairs(name):
    result = {}
    for line in (root / name).read_text().splitlines():
        key, value = line.split()
        result[key] = int(value)
    return result
events = pairs("memory.events")
stat = pairs("memory.stat")
for required in ("high", "max", "oom", "oom_kill", "oom_group_kill"):
    if required not in events:
        raise SystemExit(f"memory.events lacks {required} for pid {pid}")
status = pathlib.Path(f"/proc/{pid}/status").read_text().splitlines()
rss = next(int(line.split()[1]) * 1024 for line in status if line.startswith("VmRSS:"))
print(json.dumps({
    "pid": pid, "cgroup_path": unified, "memory_current_bytes": integer("memory.current"),
    "memory_peak_bytes": integer("memory.peak"), "memory_high_bytes": scalar("memory.high"),
    "memory_max_bytes": integer("memory.max"), "memory_swap_max_bytes": integer("memory.swap.max"),
    "memory_events": events, "memory_stat_sock_bytes": stat.get("sock", 0),
    "process_rss_bytes": rss, "process_fd_count": len(list(pathlib.Path(f"/proc/{pid}/fd").iterdir())),
}, separators=(",", ":")))
PY
}

set_agent_ballast() {
  local pid=$1 admin_url=$2 ballast_mib=$3
  agent_ns python3 - "$admin_url" "$ballast_mib" <<'PY'
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
    --argjson server_cgroup "$server_cgroup" --argjson agent_cgroup "$agent_cgroup" --arg server_source_ref "$server_source_ref" \
    --arg aggregate_agent_source_ref "$aggregate_agent_source_ref" \
    --arg server_binary_sha256 "$server_binary_sha256" \
    --arg aggregate_agent_binary_sha256 "$aggregate_agent_binary_sha256" \
    --arg script_sha256 "$script_sha256" '
      {measurement_stage:$stage,transport_mode:"aggregate-v1",configured_agent_ballast_mib:$ballast_mib,
       server_runtime:$server_runtime,agent_runtime:$agent_runtime,server_cgroup:$server_cgroup,agent_cgroup:$agent_cgroup,
       provenance:{server_source_ref:$server_source_ref,aggregate_agent_source_ref:$aggregate_agent_source_ref,
         server_binary_sha256:$server_binary_sha256,aggregate_agent_binary_sha256:$aggregate_agent_binary_sha256,
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

origin_ready=$run_dir/origin.ready.json
origin_stop=$run_dir/origin.stop
assert_namespace_helper "$agent_namespace_pid" "$agent_namespace_instance"
setsid nsenter -t "$agent_namespace_pid" -n env \
  P2PSTREAM_QUALIFICATION_ROLE=origin \
  P2PSTREAM_QUALIFICATION_READY="$origin_ready" \
  P2PSTREAM_QUALIFICATION_STOP="$origin_stop" \
  P2PSTREAM_QUALIFICATION_BIND_ADDRESS=10.212.0.2 \
  taskset -c "$agent_cpu_set" "$server_binary" -test.run '^TestProxyQualificationProcess$' -test.timeout "${overall_timeout_seconds}s" \
  >"$run_dir/origin.log" 2>&1 &
origin_pid=$!
register_owned_group "$origin_pid" origin
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
  local expected_major=1 expected_alpn=http/1.1
  [[ $protocol == h2 ]] && { expected_major=2; expected_alpn=h2; }
  local server_spec
  server_spec=$(jq -cn --argjson pid "$server_pid" --arg admin "$server_admin" --arg cpus "$server_cpu_set" '{role:"server",pid:$pid,admin_url:$admin,cpu_set:$cpus}')
  P2PSTREAM_QUALIFICATION_ROLE=load \
  P2PSTREAM_QUALIFICATION_URL="$public_url/agent/$suffix" \
  P2PSTREAM_QUALIFICATION_SCENARIO="$scenario" \
  P2PSTREAM_QUALIFICATION_PROTOCOL="$protocol" \
  P2PSTREAM_QUALIFICATION_WAN_PROTOCOL_DIAGNOSTIC=1 \
  P2PSTREAM_QUALIFICATION_WAN_ROOT_CA_FILE="$cert" \
  P2PSTREAM_QUALIFICATION_WAN_SERVER_NAME=public.test \
  P2PSTREAM_QUALIFICATION_WAN_EXPECTED_LEAF_SHA256="$expected_leaf_sha256" \
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
  P2PSTREAM_QUALIFICATION_RESOURCE_ENVELOPE="p2pstream-external-agent-tunnel-netns:${configured_rtt_ms}ms,1gbit,current-server,mode=$mode,public=$protocol,verified-tls" \
  P2PSTREAM_QUALIFICATION_SEQUENCE="$sequence" \
  P2PSTREAM_QUALIFICATION_RESULT="$raw" \
    taskset -c "$load_cpu_set" "$server_binary" -test.run '^TestProxyQualificationLoad$' -test.timeout "${transfer_timeout_seconds}s" \
    >"$raw.log" 2>&1
  local agent_runtime upstream_trace copy_attribution
  agent_runtime=$(query_agent_json "$agent_namespace_pid" "$agent_admin")
  upstream_trace=$(query_server_json "$server_admin?wan_trace=$sequence")
  copy_attribution=$(query_completed_copy_json "$server_admin?wan_copy_trace=$sequence")
  jq -e --argjson major "$expected_major" --arg alpn "$expected_alpn" --arg fingerprint "$expected_leaf_sha256" '
    ((.wan_response_tls | length) == .attempted) and .attempted == 1 and
    all(.wan_response_tls[]; .http_major == $major and .negotiated_alpn == $alpn and .peer_leaf_der_sha256 == $fingerprint and .origin_connection_id != "" and ((.validation_error // "") == ""))
  ' "$raw" >/dev/null
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
    --argjson upstream_trace "$upstream_trace" --argjson copy_attribution "$copy_attribution" --arg public_protocol "$protocol" \
    --argjson diagnostic_sequence "$sequence" \
    --argjson configured_rtt_ms "$configured_rtt_ms" --argjson measured_rtt_ms "$measured_rtt_ms" \
    --arg server_binary_sha256 "$server_binary_sha256" \
    --arg aggregate_agent_binary_sha256 "$aggregate_agent_binary_sha256" \
    --arg script_sha256 "$script_sha256" \
    --arg server_source_ref "$server_source_ref" \
    --arg aggregate_agent_source_ref "$aggregate_agent_source_ref" '
      ([.agent_tunnels_high_water[0].lanes[]? | select(.aggregate == true and .lane > 0) | .receive_limit_bytes]) as $server_growth_pools |
      ([.agent_tunnels_final[0].lanes[]? | select(.aggregate == true and .lane > 0) | .receive_limit_bytes]) as $server_final_pools |
      ([$agent_runtime.tunnel_sessions[]? | select(.aggregate == true and .lane > 0) | .receive_limit_bytes]) as $agent_pools |
      . + {
        measurement_stage:$stage,
        transfer_direction:$direction,
        transport_mode:$mode,
        public_protocol:$public_protocol,
        diagnostic_sequence:$diagnostic_sequence,
        upstream_transport_trace:$upstream_trace,
        response_copy_attribution:$copy_attribution,
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
          server_binary_sha256:$server_binary_sha256,
          aggregate_agent_binary_sha256:$aggregate_agent_binary_sha256,
          harness_sha256:$script_sha256,
          server_source_ref:$server_source_ref,
          aggregate_agent_source_ref:$aggregate_agent_source_ref
        }
      }
    ' "$raw" >> "$output"
}

wan_sender_wait_should_measure() {
	local target_sequence=$1 stage=$2 direction=$3 repetition=$4
	[[ $direction == download ]] || return 1
	case "$target_sequence:$stage:$repetition" in
		9:retained-warmup:1|11:steady:1|14:steady:2|15:steady:3) return 0 ;;
	esac
	return 1
}

sender_wait_capacity_healthy() {
	local sequence=$1
	jq -e --arg sequence "$sequence" '
		.schema_version == 1 and .sequence == $sequence and .adaptive == true and
		.adaptive_admission_role == "server_stream" and .reject_new == false and
		.memory_pressure == "healthy" and .resource_sample_error == "" and
		(.memory_source == "cgroup_v2" or .memory_source == "cgroup_v1" or .memory_source == "host" or .memory_source == "go") and
		.memory_limit_bytes > 0 and .memory_used_bytes >= 0 and .memory_used_bytes <= .memory_limit_bytes and
		.memory_headroom_to_hard_bytes >= 0 and .file_descriptors_limit > 0 and
		.file_descriptors_used >= 0 and .file_descriptors_used <= .file_descriptors_limit and
		(.resource_pressure_reason | type == "string" and length > 0) and
		.generation > 0 and .resource_sampled_unix_ns > 0 and .resource_last_good_unix_ns > 0 and
		.observed_unix_ns >= .resource_sampled_unix_ns and .observed_unix_ns >= .resource_last_good_unix_ns and
		.resource_next_sample_unix_ns >= .resource_sampled_unix_ns and
		.adaptive_raw_admission_limit >= 0 and .adaptive_admission_limit >= 0 and
		.adaptive_public_admission_limit >= 0 and .adaptive_admission_limit <= .adaptive_raw_admission_limit and
		.adaptive_public_admission_limit <= .adaptive_admission_limit
	'
}

run_maybe_sender_wait_transfer() {
	local stage=$8 direction=$9 repetition=${11}
	local target_sequence=$((sequence + 1))
	if ! wan_sender_wait_should_measure "$target_sequence" "$stage" "$direction" "$repetition"; then
		run_transfer "$@"
		return
	fi

	local prefix=$sender_wait_control_dir/sequence-$target_sequence
	local pre_runtime pre_cgroup pre_runtime_raw pre_cgroup_raw pre_capacity post_runtime post_cgroup post_capacity
	local pre_runtime_status=0 pre_cgroup_status=0 pre_capacity_query_status=0 pre_resource_status=0 pre_healthy_status=1
	local post_runtime_status=0 post_cgroup_status=0 post_capacity_query_status=0 post_healthy_status=1
	local sender_pre_status=0 sender_pre_validation_status=0 sender_post_status=0 sender_validation_status=0 transfer_status=0 resource_status=0
	local measurement_start_valid=false
	set +e
	pre_runtime_raw=$(query_server_json "$5"); pre_runtime_status=$?
	pre_cgroup_raw=$(cgroup_snapshot "$3"); pre_cgroup_status=$?
	wan_sender_wait_server_capacity "$target_sequence" "$5" "$prefix-pre-capacity.json"; pre_capacity_query_status=$?
	set -e
	pre_runtime=$pre_runtime_raw; pre_cgroup=$pre_cgroup_raw
	pre_capacity=$(jq -c '.response // {}' "$prefix-pre-capacity.json" 2>/dev/null || printf '{}')
	[[ $pre_runtime_status == 0 ]] || pre_runtime='{}'
	[[ $pre_cgroup_status == 0 ]] || pre_cgroup='{}'
	if ((pre_runtime_status != 0 || pre_cgroup_status != 0 || pre_capacity_query_status != 0)); then
		jq -cn --argjson sequence "$target_sequence" --arg stage "$stage" --arg direction "$direction" --argjson repetition "$repetition" \
			--argjson pre_runtime_status "$pre_runtime_status" --argjson pre_cgroup_status "$pre_cgroup_status" \
			--argjson pre_capacity_query_status "$pre_capacity_query_status" --arg pre_runtime_raw "$pre_runtime_raw" \
			--arg pre_cgroup_raw "$pre_cgroup_raw" --argjson pre_capacity "$pre_capacity" \
			--slurpfile pre_capacity_request "$prefix-pre-capacity.json" \
			'{schema_version:1,diagnostic:"sender-duration",qualification_result:false,sequence:$sequence,stage:$stage,
			  direction:$direction,repetition:$repetition,pre_runtime_status:$pre_runtime_status,
			  pre_cgroup_status:$pre_cgroup_status,pre_capacity_query_status:$pre_capacity_query_status,
			  pre_runtime_raw:$pre_runtime_raw,pre_cgroup_raw:$pre_cgroup_raw,pre_server_capacity:$pre_capacity,
			  pre_capacity_request:($pre_capacity_request[0] // null),complete:false,valid:false}' >> "$sender_wait_records"
		return 1
	fi
	set +e
	jq -e '.memory_max_bytes == 4294967296 and .memory_swap_max_bytes == 0 and
		([.memory_events.high,.memory_events.max,.memory_events.oom,.memory_events.oom_kill,.memory_events.oom_group_kill] | all(. == 0))' \
		<<<"$pre_cgroup" >/dev/null
	pre_resource_status=$?
	sender_wait_capacity_healthy "$target_sequence" <<<"$pre_capacity" >/dev/null
	pre_healthy_status=$?
	set -e
	if ((pre_resource_status != 0 || pre_healthy_status != 0)); then
		jq -cn --argjson sequence "$target_sequence" --arg stage "$stage" --arg direction "$direction" --argjson repetition "$repetition" \
			--argjson pre_runtime "$pre_runtime" --argjson pre_cgroup "$pre_cgroup" --argjson pre_capacity "$pre_capacity" \
			--argjson pre_resource_status "$pre_resource_status" --argjson pre_healthy_status "$pre_healthy_status" \
			--slurpfile pre_capacity_request "$prefix-pre-capacity.json" \
			'{schema_version:1,diagnostic:"sender-duration",qualification_result:false,sequence:$sequence,stage:$stage,
			  direction:$direction,repetition:$repetition,pre_runtime:$pre_runtime,pre_cgroup:$pre_cgroup,
			  pre_server_capacity:$pre_capacity,pre_capacity_request:($pre_capacity_request[0] // null),
			  pre_resource_status:$pre_resource_status,pre_healthy_status:$pre_healthy_status,complete:false,valid:false}' >> "$sender_wait_records"
		return 1
	fi

	set +e
	wan_sender_wait_query agent-drain pre "$target_sequence" "$7" "$prefix-pre-sender.json"
	sender_pre_status=$?
	python3 "$sender_wait_validator" --mode pre --pre "$prefix-pre-sender.json" \
		--sequence "$target_sequence" --output "$prefix-pre-validation.json"
	sender_pre_validation_status=$?
	set -e
	if ((sender_pre_status == 0 && sender_pre_validation_status == 0)); then
		measurement_start_valid=true
	fi

	# The inherited timed transfer remains byte-for-byte unchanged. This parent
	# alone owns endpoint traps and advances the fixed sequence once. A failed
	# sender pre-boundary irrevocably marks this row unmeasured, but does not
	# alter or retry the remaining fixed workload.
	set +e
	( trap - EXIT INT TERM ERR; set -Eeuo pipefail; run_transfer "$@" )
	transfer_status=$?
	set -e
	sequence=$target_sequence

	set +e
	wan_sender_wait_query agent-drain post "$target_sequence" "$7" "$prefix-post-sender.json"; sender_post_status=$?
	post_runtime=$(query_server_json "$5"); post_runtime_status=$?
	post_cgroup=$(cgroup_snapshot "$3"); post_cgroup_status=$?
	wan_sender_wait_server_capacity "$target_sequence" "$5" "$prefix-post-capacity.json"; post_capacity_query_status=$?
	python3 "$sender_wait_validator" --mode pair --pre "$prefix-pre-sender.json" --post "$prefix-post-sender.json" \
		--sequence "$target_sequence" --output "$prefix-validation.json"
	sender_validation_status=$?
	set -e
	[[ $post_runtime_status == 0 ]] || post_runtime='{}'
	[[ $post_cgroup_status == 0 ]] || post_cgroup='{}'
	post_capacity=$(jq -c '.response // {}' "$prefix-post-capacity.json" 2>/dev/null || printf '{}')
	if ((post_runtime_status == 0 && post_cgroup_status == 0 && post_capacity_query_status == 0)); then
		set +e
		jq -e --argjson before "$pre_cgroup" '
			.memory_max_bytes == 4294967296 and .memory_swap_max_bytes == 0 and .cgroup_path == $before.cgroup_path and
			.memory_events.high == $before.memory_events.high and .memory_events.max == $before.memory_events.max and
			.memory_events.oom == $before.memory_events.oom and .memory_events.oom_kill == $before.memory_events.oom_kill and
			.memory_events.oom_group_kill == $before.memory_events.oom_group_kill' <<<"$post_cgroup" >/dev/null
		resource_status=$?
		sender_wait_capacity_healthy "$target_sequence" <<<"$post_capacity" >/dev/null
		post_healthy_status=$?
		((resource_status == 0 && post_healthy_status == 0)) || resource_status=1
		set -e
	else
		resource_status=1
	fi

	jq -cn --argjson sequence "$target_sequence" --arg stage "$stage" --arg direction "$direction" --argjson repetition "$repetition" \
		--argjson pre_runtime "$pre_runtime" --argjson pre_cgroup "$pre_cgroup" --argjson pre_capacity "$pre_capacity" \
		--argjson post_runtime "$post_runtime" --argjson post_cgroup "$post_cgroup" --argjson post_capacity "$post_capacity" \
		--argjson transfer_status "$transfer_status" --argjson sender_pre_status "$sender_pre_status" \
		--argjson sender_pre_validation_status "$sender_pre_validation_status" --argjson measurement_start_valid "$measurement_start_valid" \
		--argjson sender_post_status "$sender_post_status" --argjson sender_validation_status "$sender_validation_status" \
		--argjson pre_resource_status "$pre_resource_status" --argjson pre_healthy_status "$pre_healthy_status" \
		--argjson post_runtime_status "$post_runtime_status" --argjson post_cgroup_status "$post_cgroup_status" \
		--argjson post_capacity_query_status "$post_capacity_query_status" --argjson post_healthy_status "$post_healthy_status" \
		--argjson resource_status "$resource_status" --slurpfile pre_sender "$prefix-pre-sender.json" \
		--slurpfile pre_sender_validation "$prefix-pre-validation.json" \
		--slurpfile post_sender "$prefix-post-sender.json" --slurpfile sender_validation "$prefix-validation.json" \
		--slurpfile pre_capacity_request "$prefix-pre-capacity.json" --slurpfile post_capacity_request "$prefix-post-capacity.json" \
		'{schema_version:1,diagnostic:"sender-duration",qualification_result:false,sequence:$sequence,stage:$stage,
		  direction:$direction,repetition:$repetition,interval_boundary:"pre-complete-drain-to-immediate-post-drain",
		  attribution_limit:"one-request-concurrency-one-full-lane-set-no-direct-request-stream-marker",
		  pre_runtime:$pre_runtime,pre_cgroup:$pre_cgroup,pre_server_capacity:$pre_capacity,
		  pre_capacity_request:($pre_capacity_request[0] // null),pre_sender:($pre_sender[0] // null),
		  sender_pre_validation:($pre_sender_validation[0] // null),measurement_start_valid:$measurement_start_valid,
		  post_runtime:$post_runtime,post_cgroup:$post_cgroup,post_server_capacity:$post_capacity,
		  post_capacity_request:($post_capacity_request[0] // null),post_sender:($post_sender[0] // null),
		  sender_validation:($sender_validation[0] // null),transfer_status:$transfer_status,
		  sender_pre_status:$sender_pre_status,sender_pre_validation_status:$sender_pre_validation_status,
		  sender_post_status:$sender_post_status,
		  sender_validation_status:$sender_validation_status,pre_resource_status:$pre_resource_status,
		  pre_healthy_status:$pre_healthy_status,post_runtime_status:$post_runtime_status,
		  post_cgroup_status:$post_cgroup_status,post_capacity_query_status:$post_capacity_query_status,
		  post_healthy_status:$post_healthy_status,resource_status:$resource_status,complete:true,
		  valid:($transfer_status == 0 and $measurement_start_valid and $sender_pre_status == 0 and $sender_post_status == 0 and
		         $sender_validation_status == 0 and $post_runtime_status == 0 and $post_cgroup_status == 0 and $resource_status == 0)}' \
		>> "$sender_wait_records"

	((transfer_status == 0)) || return "$transfer_status"
	((post_runtime_status == 0 && post_cgroup_status == 0 && resource_status == 0)) || return 1
	# Sender-only invalidity is retained for the final arm gate without changing
	# the remaining fixed transfer schedule.
	return 0
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
    taskset -c "$load_cpu_set" "$server_binary" -test.run '^TestProxyQualificationLoad$' -test.timeout 60s \
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
    --arg server_binary_sha256 "$server_binary_sha256" \
    --arg aggregate_agent_binary_sha256 "$aggregate_agent_binary_sha256" \
    --arg legacy_agent_binary_sha256 "$legacy_agent_binary_sha256" \
    --arg script_sha256 "$script_sha256" --arg server_source_ref "$server_source_ref" \
    --arg aggregate_agent_source_ref "$aggregate_agent_source_ref" --arg legacy_agent_source_ref "$legacy_agent_source_ref" '
      . + {measurement_stage:$stage,transport_mode:$mode,measured_protocol:$protocol,repetition:$repetition,
        configured_rtt_ms:$configured_rtt_ms,measured_rtt_ms:$measured_rtt_ms,
        agent_runtime_after:$agent_runtime,
        provenance:{server_source_ref:$server_source_ref,aggregate_agent_source_ref:$aggregate_agent_source_ref,
          legacy_agent_source_ref:$legacy_agent_source_ref,server_binary_sha256:$server_binary_sha256,
          aggregate_agent_binary_sha256:$aggregate_agent_binary_sha256,
          legacy_agent_binary_sha256:$legacy_agent_binary_sha256,harness_sha256:$script_sha256}}
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
    taskset -c "$load_cpu_set" "$server_binary" -test.run '^TestProxyQualificationLoad$' -test.timeout 45s \
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
    setsid taskset -c "$load_cpu_set" "$server_binary" -test.run '^TestProxyQualificationLoad$' -test.timeout 45s \
    >"$raw.log" 2>&1 &
  local load_pid=$!
  register_owned_group "$load_pid" canceled-upload-load
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
  local label=$1 mode=$2 agent_binary=$3 iteration=$4
  if ((pressure_mode)) && [[ $mode != aggregate-v1 ]]; then
    echo "pressure qualification requires aggregate-v1" >&2
    return 2
  fi
  local server_ready=$run_dir/$label-$iteration-server.ready.json server_stop=$run_dir/$label-$iteration-server.stop
  local management_ca=$run_dir/$label-$iteration-management-ca.pem
  local server_process_pid
	# The host arm supervisor creates the delegated finite scope. This process
	# joins it itself before exec, so no user-bus access is needed from either
	# nested user namespace. The host releases both bounded handshakes before any
	# agent registration, warmup, or timed transfer begins.
	setsid env \
		P2PSTREAM_WAN_READ_WAIT_SCOPE_ROLE=server \
		P2PSTREAM_WAN_READ_WAIT_SCOPE_NONCE="$server_scope_nonce" \
		P2PSTREAM_QUALIFICATION_ROLE=proxy-external-agent \
		P2PSTREAM_QUALIFICATION_READY="$server_ready" \
		P2PSTREAM_QUALIFICATION_STOP="$server_stop" \
		P2PSTREAM_QUALIFICATION_ORIGIN_URL="$origin_url" \
		P2PSTREAM_QUALIFICATION_CERT="$cert" \
		P2PSTREAM_QUALIFICATION_KEY="$key" \
		P2PSTREAM_QUALIFICATION_MANAGEMENT_CA_FILE="$management_ca" \
		P2PSTREAM_QUALIFICATION_WAN_PROTOCOL_DIAGNOSTIC=1 \
		P2PSTREAM_QUALIFICATION_WAN_COPY_ATTRIBUTION=1 \
		P2PSTREAM_QUALIFICATION_WAN_SENDER_WAIT=1 \
		P2PSTREAM_QUALIFICATION_WAN_SENDER_WAIT_SEQUENCES="$sender_wait_sequences" \
		P2PSTREAM_QUALIFICATION_WAN_SENDER_WAIT_BEARER="$sender_wait_bearer" \
		P2PSTREAM_QUALIFICATION_EXTERNAL_AGENTS=1 \
		P2PSTREAM_QUALIFICATION_BIND_ADDRESS=10.211.0.1 \
		GOMAXPROCS=4 \
		taskset -c "$server_cpu_set" "$server_scope_entrypoint" \
			"$server_scope_procs" "$server_scope_cgroup" "$server_scope_control_dir/server-preexec.json" \
			"$server_scope_control_dir/server-preexec.release" "$server_scope_nonce" \
			"$server_binary" -test.run '^TestExternalProxyQualificationServerProcess$' -test.timeout "${overall_timeout_seconds}s" \
		>"$run_dir/$label-$iteration-server.log" 2>&1 &
  server_process_pid=$!
  register_owned_group "$server_process_pid" "$label-$iteration-server"
  stops+=("$server_stop")
  wait_ready "$server_ready" "$server_process_pid"
  local server_pid public_url server_admin management_url public_id token
  server_pid=$(jq -r .pid "$server_ready")
  public_url=$(jq -r .url "$server_ready")
  server_admin=$(jq -r .admin_url "$server_ready")
  management_url=$(jq -r .management_url "$server_ready")
  public_id=$(jq -r '.agents[0].public_id' "$server_ready")
  token=$(jq -r '.agents[0].token' "$server_ready")
	python3 - "$server_scope_control_dir/server-app-ready.json" "$server_pid" "$server_ready" "$server_binary_sha256" "$script_sha256" <<'PY'
import hashlib, json, os, pathlib, sys
out, pid_text, ready_path, server_sha, runner_sha = sys.argv[1:]
ready = pathlib.Path(ready_path)
value = {
    "schema_version": 1,
    "server_local_pid": int(pid_text),
    "server_ready_path": str(ready),
    "server_ready_sha256": hashlib.sha256(ready.read_bytes()).hexdigest(),
    "server_binary_sha256": server_sha,
    "runner_sha256": runner_sha,
    "runner_namespaces": {name: os.readlink(f"/proc/self/ns/{name}") for name in ("user", "pid", "mnt", "net", "cgroup")},
}
with pathlib.Path(out).open("x", encoding="utf-8") as stream:
    json.dump(value, stream, indent=2, sort_keys=True)
    stream.write("\n")
PY
	for _ in $(seq 1 400); do
		[[ -e $server_scope_control_dir/server-app.release ]] && break
		kill -0 "$server_process_pid" 2>/dev/null || break
		sleep .05
	done
	[[ -e $server_scope_control_dir/server-app.release ]] || {
		echo "host server-scope supervisor did not release the application" >&2
		return 1
	}

  local agent_ready=$run_dir/$label-$iteration-agent.ready.json agent_stop=$run_dir/$label-$iteration-agent.stop
  local agent_process_pid agent_unit="p2p-qual-agent-$BASHPID-$iteration"
  if ((pressure_mode)); then
    assert_namespace_helper "$agent_namespace_pid" "$agent_namespace_instance"
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
      P2PSTREAM_QUALIFICATION_WAN_SENDER_WAIT=1 \
      P2PSTREAM_QUALIFICATION_WAN_SENDER_WAIT_SEQUENCES="$sender_wait_sequences" \
      P2PSTREAM_QUALIFICATION_WAN_SENDER_WAIT_BEARER="$sender_wait_bearer" \
      GOMAXPROCS=4 \
      taskset -c "$agent_cpu_set" "$agent_binary" -test.run '^TestExternalProxyQualificationAgentProcess$' -test.timeout "${overall_timeout_seconds}s" \
      >"$run_dir/$label-$iteration-agent.log" 2>&1 &
  else
    assert_namespace_helper "$agent_namespace_pid" "$agent_namespace_instance"
    setsid nsenter -t "$agent_namespace_pid" -n env \
      P2PSTREAM_QUALIFICATION_ROLE=agent-external \
      P2PSTREAM_QUALIFICATION_READY="$agent_ready" \
      P2PSTREAM_QUALIFICATION_STOP="$agent_stop" \
      P2PSTREAM_QUALIFICATION_MANAGEMENT_URL="$management_url" \
      P2PSTREAM_QUALIFICATION_MANAGEMENT_CA_FILE="$management_ca" \
      P2PSTREAM_QUALIFICATION_AGENT_PUBLIC_ID="$public_id" \
      P2PSTREAM_QUALIFICATION_AGENT_TOKEN="$token" \
      P2PSTREAM_QUALIFICATION_AGENT_TUNNELS=4 \
      P2PSTREAM_QUALIFICATION_WAN_SENDER_WAIT=1 \
      P2PSTREAM_QUALIFICATION_WAN_SENDER_WAIT_SEQUENCES="$sender_wait_sequences" \
      P2PSTREAM_QUALIFICATION_WAN_SENDER_WAIT_BEARER="$sender_wait_bearer" \
      GOMAXPROCS=4 \
      taskset -c "$agent_cpu_set" "$agent_binary" -test.run '^TestExternalProxyQualificationAgentProcess$' -test.timeout "${overall_timeout_seconds}s" \
      >"$run_dir/$label-$iteration-agent.log" 2>&1 &
  fi
  agent_process_pid=$!
  register_owned_group "$agent_process_pid" "$label-$iteration-agent"
  stops+=("$agent_stop")
  wait_ready "$agent_ready" "$agent_process_pid"
  local agent_pid agent_admin expected_bulk
  agent_pid=$(jq -r .pid "$agent_ready")
  agent_admin=$(jq -r .admin_url "$agent_ready")
  expected_bulk=3
  [[ $mode == aggregate-v1 ]] && expected_bulk=4
  wait_mode "$server_admin" "$mode" "$expected_bulk"

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
    stop_process "$label-$iteration-agent" "$agent_process_pid" "$agent_stop"
    stop_process "$label-$iteration-server" "$server_process_pid" "$server_stop"
    return
  fi

  for direction in download upload; do
    for ((warmup_round=1; warmup_round<=warmup_lane_rounds; warmup_round++)); do
			run_maybe_sender_wait_transfer "$label" "$mode" "$server_pid" "$public_url" "$server_admin" "$agent_pid" "$agent_admin" warmup "$direction" "$((warmup_mib << 20))" "$warmup_round"
      close_idle_agent_transports "$server_admin"
    done
  done
  if [[ $mode == aggregate-v1 ]]; then
    wait_aggregate_lanes_grown_and_quiescent "$server_admin" "$agent_admin" "$((required_pool_mib << 20))"
  fi
	run_maybe_sender_wait_transfer "$label" "$mode" "$server_pid" "$public_url" "$server_admin" "$agent_pid" "$agent_admin" retained-warmup download "$((warmup_mib << 20))" 1
	run_maybe_sender_wait_transfer "$label" "$mode" "$server_pid" "$public_url" "$server_admin" "$agent_pid" "$agent_admin" retained-warmup upload "$((warmup_mib << 20))" 1
  if ((pressure_mode)); then
    run_pressure_qualification "$label" "$server_pid" "$public_url" "$server_admin" "$agent_pid" "$agent_admin"
  else
    for ((repetition=1; repetition<=transfer_repetitions; repetition++)); do
      if ((repetition % 2)); then directions=(download upload); else directions=(upload download); fi
      for direction in "${directions[@]}"; do
				run_maybe_sender_wait_transfer "$label" "$mode" "$server_pid" "$public_url" "$server_admin" "$agent_pid" "$agent_admin" steady "$direction" "$((steady_mib << 20))" "$repetition"
      done
    done
    if ((smoke_mode)); then
      # The original twelve smoke rows stop after the first post-close visit to
      # each of four lane-specific transport keys. This non-gating thirteenth
      # row revisits the first key so the diagnostic can actually test reuse.
			run_maybe_sender_wait_transfer "$label" "$mode" "$server_pid" "$public_url" "$server_admin" "$agent_pid" "$agent_admin" reuse-probe download "$((steady_mib << 20))" 1
    fi
  fi

	local sender_terminal_status=0 sender_terminal_validation_status=0
	set +e
	wan_sender_wait_query agent-drain terminal 15 "$agent_admin" "$sender_wait_control_dir/terminal-sender.json"
	sender_terminal_status=$?
	python3 "$sender_wait_validator" --mode terminal --terminal "$sender_wait_control_dir/terminal-sender.json" \
		--sequence 15 --output "$sender_wait_control_dir/terminal-validation.json"
	sender_terminal_validation_status=$?
	set -e
	jq -cn --argjson status "$sender_terminal_status" --argjson validation_status "$sender_terminal_validation_status" \
		--slurpfile terminal "$sender_wait_control_dir/terminal-sender.json" \
		--slurpfile validation "$sender_wait_control_dir/terminal-validation.json" \
		'{schema_version:1,diagnostic:"sender-duration",action:"terminal",status:$status,
		  validation_status:$validation_status,response:($terminal[0] // null),validation:($validation[0] // null),
		  valid:($status == 0 and $validation_status == 0)}' \
		>> "$sender_wait_records"
	cgroup_snapshot "$server_pid" > "$server_scope_control_dir/server-cgroup-post-workload.json"
  stop_process "$label-$iteration-agent" "$agent_process_pid" "$agent_stop"
  stop_process "$label-$iteration-server" "$server_process_pid" "$server_stop"
}

run_cohort "current-server-aggregate-v1-$protocol" aggregate-v1 "$aggregate_agent_binary" 1
stop_process origin "$origin_pid" "$origin_stop"
stop_process agent-namespace "$agent_namespace_pid" "$agent_namespace_stop"
stop_process router-namespace "$router_namespace_pid" "$router_namespace_stop"
summary=${output%.jsonl}.summary.json
python3 - "$output" "$summary" "$required_pool_mib" "$smoke_mode" "$pressure_mode" "$tiny_response_mode" "$protocol" "$expected_leaf_sha256" "$copy_record_validator" "$sender_wait_small" <<'PY'
import importlib.util, json, statistics, sys

source, destination, required_pool_mib, smoke_mode, pressure_mode, tiny_response_mode, protocol, expected_fingerprint, copy_validator_path, sender_wait_small = sys.argv[1], sys.argv[2], int(sys.argv[3]), int(sys.argv[4]), int(sys.argv[5]), int(sys.argv[6]), sys.argv[7], sys.argv[8], sys.argv[9], int(sys.argv[10])
spec = importlib.util.spec_from_file_location("copy_record_validator", copy_validator_path)
copy_record_validator = importlib.util.module_from_spec(spec)
spec.loader.exec_module(copy_record_validator)
rows = [json.loads(line) for line in open(source, encoding="utf-8") if line.strip()]
if protocol in ("h1", "h2"):
    expected_major = 1 if protocol == "h1" else 2
    expected_alpn = "http/1.1" if protocol == "h1" else "h2"
    expected = [
        ("warmup", "download", repetition) for repetition in range(1, 5)
    ] + [
        ("warmup", "upload", repetition) for repetition in range(1, 5)
    ] + [
        ("retained-warmup", "download", 1),
        ("retained-warmup", "upload", 1),
    ]
    if smoke_mode:
        expected += [("steady", "download", 1), ("steady", "upload", 1), ("reuse-probe", "download", 1)]
    else:
        expected += [
            ("steady", "download", 1), ("steady", "upload", 1),
            ("steady", "upload", 2), ("steady", "download", 2),
            ("steady", "download", 3), ("steady", "upload", 3),
        ]
    expected_stages = {"warmup": 8, "retained-warmup": 2, "steady": 2 if smoke_mode else 6}
    if smoke_mode:
        expected_stages["reuse-probe"] = 1
    stage_counts = {stage: sum(row.get("measurement_stage") == stage for row in rows) for stage in expected_stages}
    expected_rows = sum(expected_stages.values())
    actual = [(row.get("measurement_stage"), row.get("transfer_direction"), row.get("repetition")) for row in rows]
    if len(rows) != expected_rows or stage_counts != expected_stages or actual != expected:
        raise SystemExit(f"diagnostic row schedule mismatch: rows={len(rows)} stages={stage_counts} actual={actual!r}")
    correctness = all(
        row.get("attempted") == 1 and row.get("useful_completed") == 1 and row.get("errors") == 0 and
        row.get("status_classes") == {"2xx": 1} and row.get("status_codes") == {"200": 1} and
        row.get("transport_mode") == "aggregate-v1" and row.get("public_protocol") == protocol and
        row.get("configured_payload_bytes") == ((4 if (smoke_mode or sender_wait_small) else 256) << 20) and
        row.get("response_bytes") == (((4 if (smoke_mode or sender_wait_small) else 256) << 20) if row.get("transfer_direction") == "download" else 32) and
        isinstance(row.get("wall_seconds"), (int, float)) and row.get("wall_seconds") > 0 and
        isinstance(row.get("useful_transfer_bytes_per_second"), (int, float)) and row.get("useful_transfer_bytes_per_second") > 0 and
        row.get("event_stats_captured") is True and
        row.get("persisted_event_delta") == 1 and row.get("dropped_event_delta", 0) == 0 and
        row.get("event_queue_after_drain", 0) == 0 and not row.get("event_drain_error")
        for row in rows)
    tls_complete = all(
        len(row.get("wan_response_tls", [])) == 1 and
        row["wan_response_tls"][0].get("http_major") == expected_major and
        row["wan_response_tls"][0].get("negotiated_alpn") == expected_alpn and
        row["wan_response_tls"][0].get("peer_leaf_der_sha256") == expected_fingerprint and
        row["wan_response_tls"][0].get("origin_connection_id") and
        not row["wan_response_tls"][0].get("validation_error")
        for row in rows)
    copy_issues_by_sequence = {}
    for expected_sequence, row in enumerate(rows, 1):
        found = copy_record_validator.validate_copy_envelope(
            row.get("response_copy_attribution", {}), expected_sequence,
            row.get("transfer_direction"), row.get("configured_payload_bytes"))
        if found:
            copy_issues_by_sequence[str(expected_sequence)] = found
    copy_complete = not copy_issues_by_sequence
    trace_rows = []
    trace_complete = True
    for expected_sequence, row in enumerate(rows, 1):
        envelope = row.get("upstream_transport_trace", {})
        matches = envelope.get("matches", [])
        # Sequence is retained in the raw trace and the runner query; method/path and
        # callback cardinality are the fail-closed correlation checks.
        if len(matches) == 1:
            trace = matches[0]
            expected_method = "GET" if row["transfer_direction"] == "download" else "POST"
            expected_path = "/agent/bulk" if row["transfer_direction"] == "download" else "/agent/bulk-upload"
            valid = (envelope.get("overflow") is False and envelope.get("store_count") == expected_sequence and
                     row.get("diagnostic_sequence") == expected_sequence and
                     envelope.get("sequence") == str(expected_sequence) and trace.get("sequence") == str(expected_sequence) and
                     trace.get("method") == expected_method and
                     trace.get("path") == expected_path and trace.get("callback_overflow") is False and
                     len(trace.get("got_conn", [])) == 1 and len(trace.get("put_idle_conn", [])) <= 1)
        else:
            trace = {}
            valid = False
        trace_complete = trace_complete and valid
        got = trace.get("got_conn", [{}])[0] if len(trace.get("got_conn", [])) == 1 else {}
        connection_parts = (got.get("connection_type"), got.get("local_address"), got.get("remote_address"))
        connection_key = "|".join(connection_parts) if all(isinstance(part, str) and part for part in connection_parts) else ""
        trace_rows.append({
            "stage": row["measurement_stage"], "direction": row["transfer_direction"],
            "repetition": row["repetition"], "valid": valid, "reused": got.get("reused"),
            "was_idle": got.get("was_idle"), "idle_time_ns": got.get("idle_time_ns"),
            "got_conn_observed_unix_ns": got.get("observed_unix_ns"), "connection_key": connection_key,
            "connection_type": got.get("connection_type"), "local_address": got.get("local_address"),
            "remote_address": got.get("remote_address"),
            "put_idle_conn": trace.get("put_idle_conn", []),
            "origin_connection_id": row["wan_response_tls"][0].get("origin_connection_id") if row.get("wan_response_tls") else None,
        })
    download_floor = 29_700_000
    download_rows = [row for row in rows if row["transfer_direction"] == "download" and row["measurement_stage"] != "reuse-probe"]
    floor_by_stage = {}
    for stage in ("warmup", "retained-warmup", "steady"):
        values = [row["useful_transfer_bytes_per_second"] for row in download_rows if row["measurement_stage"] == stage]
        floor_by_stage[stage] = {"samples": len(values), "minimum_bytes_per_second": min(values),
                                 "all_at_or_above_floor": all(value >= download_floor for value in values)}
    floor_pass = all(value["all_at_or_above_floor"] for value in floor_by_stage.values())
    floor_gate_pass = floor_pass or bool(smoke_mode) or bool(sender_wait_small)
    reuse_issues = []
    reuse_expectations = []
    first_visits = trace_rows[8:12]
    if len(first_visits) != 4:
        reuse_issues.append("missing four first post-close lane-key visits")
    first_by_key = {}
    for position, trace in enumerate(first_visits, 1):
        key = trace["connection_key"]
        put = trace["put_idle_conn"]
        successful_put = (len(put) == 1 and isinstance(put[0].get("observed_unix_ns"), int)
                          and put[0]["observed_unix_ns"] > 0 and not put[0].get("error"))
        expected_fresh = (key and key not in first_by_key and trace["reused"] is False
                          and trace["was_idle"] is False and trace["idle_time_ns"] == 0)
        if not expected_fresh:
            reuse_issues.append(f"sequence {position + 8} is not a distinct expected-fresh post-close key")
        if not successful_put:
            reuse_issues.append(f"sequence {position + 8} lacks a successful PutIdleConn timestamp")
        if key:
            first_by_key[key] = trace
        reuse_expectations.append({"sequence": position + 8, "kind": "first-post-close-visit", "lane_position": position,
                                   "connection_key": key, "satisfied": bool(expected_fresh and successful_put)})
    revisits = trace_rows[12:]
    for revisit_index, trace in enumerate(revisits):
        expected_first = first_visits[revisit_index % 4] if len(first_visits) == 4 else {}
        expected_key = expected_first.get("connection_key")
        prior_put = expected_first.get("put_idle_conn", [])
        got_ns = trace.get("got_conn_observed_unix_ns")
        put_ns = prior_put[0].get("observed_unix_ns") if len(prior_put) == 1 else None
        elapsed_idle_ns = got_ns - put_ns if isinstance(got_ns, int) and isinstance(put_ns, int) else None
        satisfied = bool(expected_key and trace["connection_key"] == expected_key and
                         trace["reused"] is True and trace["was_idle"] is True and
                         isinstance(trace["idle_time_ns"], int) and trace["idle_time_ns"] >= 0 and
                         isinstance(elapsed_idle_ns, int) and 0 <= elapsed_idle_ns < 30_000_000_000 and
                         trace["origin_connection_id"] == expected_first.get("origin_connection_id"))
        if not satisfied:
            reuse_issues.append(f"sequence {revisit_index + 13} did not reuse its bound post-close lane key")
        reuse_expectations.append({
            "sequence": revisit_index + 13, "kind": "same-key-revisit", "lane_position": revisit_index % 4 + 1,
            "connection_key": trace["connection_key"],
            "expected_connection_key": expected_key, "prior_put_idle_unix_ns": put_ns,
            "got_conn_unix_ns": got_ns, "elapsed_idle_ns": elapsed_idle_ns,
            "httptrace_idle_time_ns": trace.get("idle_time_ns"),
            "prior_origin_connection_id": expected_first.get("origin_connection_id"),
            "observed_origin_connection_id": trace.get("origin_connection_id"), "satisfied": satisfied,
        })
    expected_reuse = not reuse_issues and len(revisits) == (1 if smoke_mode else 4)
    if len(revisits) != (1 if smoke_mode else 4):
        reuse_issues.append(f"same-key revisit count {len(revisits)} changed")
        expected_reuse = False
    classification = "invalid" if not (correctness and tls_complete and trace_complete and copy_complete) else ("inconclusive-upstream-reuse" if not expected_reuse else "observational-complete")
    summary = {
        "diagnostic_only": True, "public_protocol": protocol, "classification": classification,
        "size_mode": "small" if sender_wait_small else ("smoke" if smoke_mode else "sustained"),
        "acceptance": {"correctness": correctness, "verified_tls_and_protocol_every_response": tls_complete,
                       "transport_trace_complete": trace_complete, "expected_upstream_reuse": expected_reuse,
                       "response_copy_attribution_complete": copy_complete,
                       "download_floor_every_phase": floor_gate_pass},
        "gates": {"download_bytes_per_second_floor": download_floor}, "download_by_phase": floor_by_stage,
        "transport_traces": trace_rows, "reuse_expectations": reuse_expectations, "reuse_issues": reuse_issues,
        "reuse_counts": {"reused": sum(trace["reused"] is True for trace in trace_rows),
                         "new": sum(trace["reused"] is False for trace in trace_rows),
                         "put_idle_absent": sum(not trace["put_idle_conn"] for trace in trace_rows)},
        "copy_attribution_issues_by_sequence": copy_issues_by_sequence,
        "copy_attribution_interpretation": "Diagnostic observer only. Upstream Read and downstream Write durations are compared separately with monotonic handler elapsed time. Unix timestamps are retained only for coarse correlation. Inter-read gaps include downstream Write time and bookkeeping and are never added as an independent component. POST observations cover only the 32-byte response copy, not upload request-body handling.",
        "interpretation": "Observational diagnostic only; throughput is not qualification evidence and medians do not establish health or causality. Warmup rows explicitly close idle transports. Sequences 9-12 are the first post-close visits to four concrete lane keys; only later same-key visits classify reuse, bound to the prior successful PutIdleConn timestamp and exact origin connection identity.",
    }
    with open(destination, "w", encoding="utf-8") as output_file:
        json.dump(summary, output_file, indent=2, sort_keys=True)
        output_file.write("\n")
    if classification != "observational-complete" or not all(summary["acceptance"].values()):
        raise SystemExit(f"WAN protocol diagnostic failed: {summary['acceptance']}")
    raise SystemExit(0)
if pressure_mode:
    result = next((row for row in rows if row.get("measurement_stage") == "pressure-result"), None)
    rejected = next((row for row in rows if row.get("measurement_stage") == "pressure-rejection"), None)
    control = next((row for row in rows if row.get("measurement_stage") == "pressure-control-roundtrip"), None)
    canceled = next((row for row in rows if row.get("measurement_stage") == "canceled-upload"), None)
    baseline = next((row for row in rows if row.get("measurement_stage") == "pressure-baseline"), None)
    recovered = next((row for row in rows if row.get("measurement_stage") == "pressure-recovered"), None)
    critical = next((row for row in rows if row.get("measurement_stage") == "pressure-critical"), None)
    if not result or not rejected or not control or not canceled or not baseline or not critical or not recovered:
        raise SystemExit("pressure qualification is missing required cancellation, pressure, control, rejection, or recovery rows")
    def receiving_pools(row, endpoint):
        if endpoint == "server":
            tunnels = row["server_runtime"].get("agent_tunnels", [])
            lanes = tunnels[0].get("lanes", []) if len(tunnels) == 1 else []
        else:
            lanes = row["agent_runtime"].get("tunnel_sessions", [])
        values = sorted(lane["receive_limit_bytes"] for lane in lanes
                        if lane.get("aggregate") is True and lane.get("lane", 0) > 0)
        if len(values) != 4:
            raise SystemExit(f"{row['measurement_stage']} {endpoint} receiving lane count = {len(values)}, want 4")
        return values
    baseline_server_pools = receiving_pools(baseline, "server")
    baseline_agent_pools = receiving_pools(baseline, "agent")
    recovered_server_pools = receiving_pools(recovered, "server")
    recovered_agent_pools = receiving_pools(recovered, "agent")
    required_pool_bytes = required_pool_mib << 20
    summary = {
        "acceptance": {
            "pressure_reached": result["pressure_reached"],
            "admission_rejected_under_pressure": rejected["rejected_pressure_after"] > rejected["rejected_pressure_before"],
            "functional_recovery": result["functional_recovery"],
            "control_progress_during_pressure": result["control_progress_during_pressure"],
            "ordinary_admission_closed_during_pressure":
                critical["agent_runtime"]["agent_capacity"]["ordinary_admission_limit"] == 0 and
                control["ordinary_admission_limit_before"] == 0 and
                control["ordinary_admission_limit_after"] == 0,
            "control_reservation_owned":
                control["control_reserved_slots"] > 0 and
                control["control_reserved_base_bytes"] > 0 and
                control["control_reserved_base_fds"] > 0,
            "control_slot_returned_to_baseline":
                control["control_in_use_before"] == 0 and control["control_in_use_after"] == 0,
            "control_did_not_consume_ordinary_pressure_rejection":
                control["agent_rejected_pressure_after"] == control["agent_rejected_pressure_before"],
            "canceled_upload_refunds_quiescent": canceled["agent_lane_refunds_quiescent"],
            "canceled_upload_reached_origin": canceled["origin_receiving_payload_observed"],
            "all_receiving_pools_exceeded_required_size_before_pressure":
                min(baseline_server_pools + baseline_agent_pools) > required_pool_bytes,
            "all_receiving_pools_retained_after_recovery":
                min(recovered_server_pools + recovered_agent_pools) > required_pool_bytes,
            "no_oom": all((row["agent_cgroup"]["memory_events"]["oom"] == 0 and
                           row["agent_cgroup"]["memory_events"]["oom_kill"] == 0 and
                           row["server_cgroup"]["memory_events"]["oom"] == 0 and
                           row["server_cgroup"]["memory_events"]["oom_kill"] == 0)
                          for row in rows if "agent_cgroup" in row and "server_cgroup" in row),
        },
        "pressure_ballast_mib": result["pressure_ballast_mib"],
        "baseline_control_roundtrip": result["baseline_control_roundtrip"],
        "pressure_control_roundtrip": result["pressure_control_roundtrip"],
        "recovery_control_roundtrip": result["recovery_control_roundtrip"],
        "control_reservation": {
            "reserved_slots": control["control_reserved_slots"],
            "reserved_base_bytes": control["control_reserved_base_bytes"],
            "reserved_base_fds": control["control_reserved_base_fds"],
            "in_use_before": control["control_in_use_before"],
            "in_use_immediate": control["control_in_use_immediate"],
            "in_use_after": control["control_in_use_after"],
            "ordinary_admission_limit_before": control["ordinary_admission_limit_before"],
            "ordinary_admission_limit_after": control["ordinary_admission_limit_after"],
        },
        "receiving_pools": {
            "required_strictly_greater_than_bytes": required_pool_bytes,
            "pre_pressure": {
                "server_bytes": baseline_server_pools,
                "server_min_bytes": min(baseline_server_pools),
                "server_max_bytes": max(baseline_server_pools),
                "agent_bytes": baseline_agent_pools,
                "agent_min_bytes": min(baseline_agent_pools),
                "agent_max_bytes": max(baseline_agent_pools),
                "server_cgroup_memory_current_bytes": baseline["server_cgroup"]["memory_current_bytes"],
                "agent_cgroup_memory_current_bytes": baseline["agent_cgroup"]["memory_current_bytes"],
            },
            "post_recovery": {
                "server_bytes": recovered_server_pools,
                "server_min_bytes": min(recovered_server_pools),
                "server_max_bytes": max(recovered_server_pools),
                "agent_bytes": recovered_agent_pools,
                "agent_min_bytes": min(recovered_agent_pools),
                "agent_max_bytes": max(recovered_agent_pools),
                "server_cgroup_memory_current_bytes": recovered["server_cgroup"]["memory_current_bytes"],
                "agent_cgroup_memory_current_bytes": recovered["agent_cgroup"]["memory_current_bytes"],
            },
        },
    }
    with open(destination, "w", encoding="utf-8") as output_file:
        json.dump(summary, output_file, indent=2, sort_keys=True)
        output_file.write("\n")
    if not all(summary["acceptance"].values()):
        raise SystemExit(f"pressure qualification failed acceptance: {summary['acceptance']}")
    raise SystemExit(0)
if tiny_response_mode:
    tiny = [row for row in rows if row.get("measurement_stage") == "tiny-response"]
    measured_rtts = sorted({row["measured_rtt_ms"] for row in tiny})
    configured_rtts = sorted({row["configured_rtt_ms"] for row in tiny})
    if len(measured_rtts) != 1 or len(configured_rtts) != 1:
        raise SystemExit(f"tiny-response RTT metadata changed within cohort: configured={configured_rtts}, measured={measured_rtts}")
    summary = {"acceptance": {}, "workloads": {},
               "network": {"configured_rtt_ms": configured_rtts[0], "measured_rtt_ms": measured_rtts[0],
                           "shaped_rate_bits_per_second": 1000000000},
               "gates": {"minimum_throughput_ratio": .90, "maximum_latency_ratio": 1.10}}
    failed = []
    for protocol in ("h1", "h2"):
        for concurrency in (16, 128):
            key = f"{protocol}-c{concurrency}"
            summary["workloads"][key] = {}
            by_mode = {}
            for mode in ("legacy", "aggregate-v1"):
                selected = sorted((row for row in tiny if row["transport_mode"] == mode and
                                   row["measured_protocol"] == protocol and row["concurrency"] == concurrency),
                                  key=lambda row: row["repetition"])
                if len(selected) != 3:
                    raise SystemExit(f"{key} {mode} sample count = {len(selected)}, want 3")
                by_mode[mode] = selected
                summary["workloads"][key][mode] = {
                    "samples": len(selected),
                    "useful_requests_per_second": [row["useful_requests_per_second"] for row in selected],
                    "median_useful_requests_per_second": statistics.median(row["useful_requests_per_second"] for row in selected),
                    "p95_ms": [row["p95_ms"] for row in selected],
                    "median_p95_ms": statistics.median(row["p95_ms"] for row in selected),
                    "p99_ms": [row["p99_ms"] for row in selected],
                    "median_p99_ms": statistics.median(row["p99_ms"] for row in selected),
                }
            candidate = summary["workloads"][key]["aggregate-v1"]
            baseline = summary["workloads"][key]["legacy"]
            throughput_ratio = candidate["median_useful_requests_per_second"] / baseline["median_useful_requests_per_second"]
            p95_ratio = candidate["median_p95_ms"] / baseline["median_p95_ms"]
            p99_ratio = candidate["median_p99_ms"] / baseline["median_p99_ms"]
            pairs = []
            for candidate_row, baseline_row in zip(by_mode["aggregate-v1"], by_mode["legacy"]):
                if candidate_row["repetition"] != baseline_row["repetition"]:
                    raise SystemExit(f"{key} repetition mismatch")
                pairs.append({
                    "repetition": candidate_row["repetition"],
                    "throughput_ratio": candidate_row["useful_requests_per_second"] / baseline_row["useful_requests_per_second"],
                    "p95_ratio": candidate_row["p95_ms"] / baseline_row["p95_ms"],
                    "p99_ratio": candidate_row["p99_ms"] / baseline_row["p99_ms"],
                })
            summary["workloads"][key]["candidate_to_legacy"] = {
                "median_throughput_ratio": throughput_ratio,
                "median_p95_ratio": p95_ratio,
                "median_p99_ratio": p99_ratio,
                "paired_ratios": pairs,
                "paired_throughput_ratio_range": [min(pair["throughput_ratio"] for pair in pairs), max(pair["throughput_ratio"] for pair in pairs)],
                "paired_throughput_ratio_population_stddev": statistics.pstdev(pair["throughput_ratio"] for pair in pairs),
            }
            throughput_pass = throughput_ratio >= .90
            p95_pass = p95_ratio <= 1.10
            p99_pass = p99_ratio <= 1.10
            summary["acceptance"][f"{key}_throughput"] = throughput_pass
            summary["acceptance"][f"{key}_p95"] = p95_pass
            summary["acceptance"][f"{key}_p99"] = p99_pass
            if not (throughput_pass and p95_pass and p99_pass):
                failed.append(key)
    with open(destination, "w", encoding="utf-8") as output_file:
        json.dump(summary, output_file, indent=2, sort_keys=True)
        output_file.write("\n")
    if failed:
        raise SystemExit(f"tiny-response qualification failed: {sorted(set(failed))}")
    raise SystemExit(0)
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

#!/usr/bin/env bash
# Freeze and own one diagnostic-only raw-TLS/current-Yamux transport-ceiling run.
set -Eeuo pipefail

runner=""
server_binary=""
agent_binary=""
source_ref=""
expected_payload_sha256=""
build_manifest=""
expected_runner_sha256=""
expected_server_sha256=""
expected_agent_sha256=""
expected_build_manifest_sha256=""
output_dir=""
preflight_only=0
fixture_smoke=0

while (($#)); do
  case "$1" in
    --runner) runner=$2; shift 2 ;;
    --server-binary) server_binary=$2; shift 2 ;;
    --agent-binary) agent_binary=$2; shift 2 ;;
    --source-ref) source_ref=$2; shift 2 ;;
    --expected-payload-sha256) expected_payload_sha256=$2; shift 2 ;;
    --build-manifest) build_manifest=$2; shift 2 ;;
    --expected-runner-sha256) expected_runner_sha256=$2; shift 2 ;;
    --expected-server-sha256) expected_server_sha256=$2; shift 2 ;;
    --expected-agent-sha256) expected_agent_sha256=$2; shift 2 ;;
    --expected-build-manifest-sha256) expected_build_manifest_sha256=$2; shift 2 ;;
    --output-dir) output_dir=$2; shift 2 ;;
    --preflight-only) preflight_only=1; shift ;;
    --fixture-smoke) fixture_smoke=1; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

is_sha256() { [[ $1 =~ ^[0-9a-f]{64}$ ]]; }
fixed_payload_sha256=ad818ce955253fd9030cbefa352527321b308cfc574cb4bbee9cbcd0d4930790
fixed_source_ref=40e1046c2f68ee3d545dfd582cd424c2f3604b2a
fixed_runner_sha256=564c216683e8c721dfc2264a9a919f3040a8c989d127b5d721a2af09882c0b77
fixed_server_sha256=98d67ff45e6f4f0da317b7929c9cf7dce704f3946bcf710c9f4a5ae66f254c67
fixed_agent_sha256=62a0ba54f0d62310e23607bf89a2738b951882529913c71be15a89d42849c9ac
fixed_manifest_sha256=8b9e6d5ab40f73fdab67a66f56406114c2bfc0f4c1f6c244101f8dbd854d5eb6
fixed_checksum_index_sha256=5539044a45f753a1641d08b902b9e801a5941d4db15302eef14e0ef12727714c
fixed_overlay_sha256=67d8434cdc2c616c89d2d60ea3cb2d57e37071278c8638942bc82581916e9037
fixed_eof_correction_sha256=8bdaac6f39e191b6848abfe765a37078e3bd86aae6d8f351d09a531077d651b1
fixed_configuration_sha256=9ac87851f1b88a92f56cedc5bcf9cd2e87113fc559de7a5c83feabf982265a2c
fixed_base_commit=ccb9c9cb683b555044033d68ff59112aa4bf21d9
fixed_go_binary=/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin/go
fixed_server_build_id='BYgcXD-SYd2g-bJNeoeD/Z6HZzcwwyYyfJOEJDzW8/92rA6hgivcwJ0wvL5nse/6puSapsJomQbQ5Hv2Lzh'
fixed_agent_build_id='JIKNYLZonqODnNvVxDU-/9eYLU6zYGSdifdYeNA8u/RDp6yBTRhftkNRCh8uhL/Ew0Gm_iHMiRl9wzJiJ0Y'
[[ -f $runner && -x $server_binary && -x $agent_binary && -f $build_manifest && $source_ref =~ ^[0-9a-f]{40}$ && -n $output_dir ]] || {
  echo "runner, server/agent binaries, source ref, build manifest and output dir are required" >&2
  exit 2
}
for digest in "$expected_payload_sha256" "$expected_runner_sha256" "$expected_server_sha256" \
  "$expected_agent_sha256" "$expected_build_manifest_sha256"; do
  is_sha256 "$digest" || { echo "all expected identities must be lowercase full SHA-256 values" >&2; exit 2; }
done
[[ $expected_payload_sha256 == "$fixed_payload_sha256" ]] || {
  echo "payload digest does not match the immutable 256 MiB transport-ceiling plan" >&2
  exit 2
}
[[ $source_ref == "$fixed_source_ref" && $expected_runner_sha256 == "$fixed_runner_sha256" &&
   $expected_server_sha256 == "$fixed_server_sha256" && $expected_agent_sha256 == "$fixed_agent_sha256" &&
   $expected_build_manifest_sha256 == "$fixed_manifest_sha256" ]] || {
  echo "requested identities do not match the immutable transport-ceiling bundle" >&2
  exit 2
}
for tool in find jq python3 realpath setsid sha256sum unshare; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 2; }
done
[[ $EUID -eq 0 ]] || { echo "the owned PID/network namespace fixture requires root" >&2; exit 2; }

runner=$(realpath "$runner")
server_binary=$(realpath "$server_binary")
agent_binary=$(realpath "$agent_binary")
build_manifest=$(realpath "$build_manifest")
[[ $(sha256sum "$runner" | awk '{print $1}') == "$expected_runner_sha256" ]] || { echo "runner hash mismatch" >&2; exit 2; }
[[ $(sha256sum "$server_binary" | awk '{print $1}') == "$expected_server_sha256" ]] || { echo "server binary hash mismatch" >&2; exit 2; }
[[ $(sha256sum "$agent_binary" | awk '{print $1}') == "$expected_agent_sha256" ]] || { echo "agent binary hash mismatch" >&2; exit 2; }
[[ $(sha256sum "$build_manifest" | awk '{print $1}') == "$expected_build_manifest_sha256" ]] || { echo "build manifest hash mismatch" >&2; exit 2; }

bundle_root=$(dirname "$build_manifest")
[[ -f $bundle_root/SHA256SUMS ]] || { echo "bundle SHA256SUMS is required" >&2; exit 2; }
[[ $(sha256sum "$bundle_root/SHA256SUMS" | awk '{print $1}') == "$fixed_checksum_index_sha256" ]] || {
  echo "bundle checksum index hash mismatch" >&2
  exit 2
}
(
  cd "$bundle_root"
  sha256sum -c SHA256SUMS >/dev/null
) || { echo "bundle checksum validation failed" >&2; exit 2; }

python3 - "$build_manifest" "$fixed_base_commit" "$fixed_source_ref" "$fixed_overlay_sha256" "$fixed_eof_correction_sha256" \
  "$fixed_payload_sha256" "$fixed_server_sha256" "$fixed_agent_sha256" \
  "$fixed_server_build_id" "$fixed_agent_build_id" "$fixed_go_binary" "$fixed_configuration_sha256" <<'PY'
import hashlib,json,re,sys
from pathlib import Path
(path,base,source_ref,overlay_sha,eof_sha,payload_sha,server_sha,agent_sha,
 server_build_id,agent_build_id,go_binary,configuration_sha)=sys.argv[1:]
value=json.loads(Path(path).read_text())
def require(condition,message):
    if not condition: raise SystemExit(message)
require(value.get("schema_version")==1 and value.get("kind")=="p2pstream-transport-ceiling-build-bundle-v1","manifest identity")
source=value.get("source")
require(isinstance(source,dict) and source.get("base_commit")==base and source.get("build_tag")=="p2pstream_transport_ceiling" and source.get("source_ref")==source_ref and source.get("qualification_tag_enabled") is False,"manifest source")
overlay=source.get("overlay")
expected_files={
 "internal/agent/transport_ceiling_qualification_test.go":"35c19cc375ce646a98b0f7de86ac2e15fcec02e87ffbdeb7de1a54e3ab9cdf91",
 "internal/server/transport_ceiling_qualification_test.go":"4fe6edede3e1a9e8745ee4661d95a46d98bcba1e612c936df3d38c35b72459f6",
 "internal/tunnel/transport_ceiling_qualification_linux.go":"a941816b642e8c750f71811bf09eef86b626b885b3c64f00bd71d9aefdf6083d",
 "internal/tunnel/transport_ceiling_qualification_test.go":"ef6819426f61e4410d76a20ec6e2428be360acbe882961c624ff9274f4cbb64b"}
require(overlay=={"path":"transport-ceiling.patch","sha256":overlay_sha,"files":expected_files},"manifest overlay")
require(source.get("eof_correction_review")=={"path":"eof-correction-review.patch","sha256":eof_sha},"manifest EOF correction review")
require(value.get("payload")=={"algorithm":"byte((index*131+17)%251)","bytes":268435456,"period_bytes":1024,"sha256":payload_sha},"manifest payload")
configuration=value.get("configuration")
require(isinstance(configuration,dict) and set(configuration)=={"server","agent"},"manifest configuration roles")
configuration_keys=None
for role in ("server","agent"):
    role_configuration=configuration.get(role)
    require(isinstance(role_configuration,dict) and set(role_configuration)=={"aggregate-yamux","raw-tls"},f"manifest {role} configuration variants")
    for variant in ("aggregate-yamux","raw-tls"):
        item=role_configuration.get(variant)
        require(isinstance(item,dict) and item,f"manifest {role}/{variant} configuration")
        if configuration_keys is None: configuration_keys=set(item)
        require(set(item)==configuration_keys,f"manifest {role}/{variant} configuration keys")
        require(all(isinstance(key,str) and key and (isinstance(config_value,(str,int,bool)) and not isinstance(config_value,float)) for key,config_value in item.items()),f"manifest {role}/{variant} configuration types")
configuration_bytes=json.dumps(configuration,sort_keys=True,separators=(",",":")).encode()
require(hashlib.sha256(configuration_bytes).hexdigest()==configuration_sha,"manifest pinned configuration")
toolchain=value.get("toolchain")
require(isinstance(toolchain,dict) and toolchain.get("go_binary")==go_binary and toolchain.get("go_version")=="go1.26.6" and toolchain.get("gotoolchain")=="local" and toolchain.get("gowork")=="off" and toolchain.get("goflags")=="" and toolchain.get("temporary_variables_on_owned_ext4") is True,"manifest toolchain")
expected={"server":("bin/transport-ceiling-server.test",server_sha,server_build_id),"agent":("bin/transport-ceiling-agent.test",agent_sha,agent_build_id)}
for role,(binary_path,digest,build_id) in expected.items():
    binary=value.get("binaries",{}).get(role)
    require(binary=={"path":binary_path,"sha256":digest,"build_id":build_id,"go_version":"go1.26.6","tags":["p2pstream_transport_ceiling"]},f"manifest {role} binary")
validation=value.get("validation")
require(isinstance(validation,dict) and validation.get("corrected_external_smoke_run") is False and validation.get("full_64_transfer_run") is False,"manifest pre-smoke state")
require(validation.get("agent_focused_tagged_normal",{}).get("count")==10 and validation["agent_focused_tagged_normal"].get("result")=="pass","manifest agent normal validation")
require(validation.get("agent_focused_tagged_race",{}).get("count")==5 and validation["agent_focused_tagged_race"].get("result")=="pass","manifest agent race validation")
carried=validation.get("carried_unchanged_helper_server",{})
require(carried.get("result")=="pass" and carried.get("prior_manifest_sha256")=="f5ab6117a1fb90992ba0efb9c57b3b85029358277bf31ee7a48c1c84207b9044" and carried.get("go_module_and_yamux_inputs_unchanged") is True,"manifest carried helper/server validation")
require(validation.get("agent_ordinary_tag_off_compile",{}).get("result")=="pass" and validation.get("patch_replay",{}).get("forward_exact") is True and validation["patch_replay"].get("reverse_exact") is True,"manifest compile/replay validation")
PY
[[ -x $fixed_go_binary ]] || { echo "pinned Go binary is unavailable" >&2; exit 2; }
[[ $($fixed_go_binary tool buildid "$server_binary") == "$fixed_server_build_id" ]] || { echo "server build ID mismatch" >&2; exit 2; }
[[ $($fixed_go_binary tool buildid "$agent_binary") == "$fixed_agent_build_id" ]] || { echo "agent build ID mismatch" >&2; exit 2; }

output_dir=$(realpath -m "$output_dir")
if [[ -e $output_dir ]]; then
  [[ -d $output_dir && -z $(find "$output_dir" -mindepth 1 -maxdepth 1 -print -quit) ]] || {
    echo "--output-dir must not exist or must be empty" >&2
    exit 2
  }
else
  mkdir -p "$output_dir"
fi

snapshot=$output_dir/snapshot
mkdir -p "$snapshot/scripts" "$snapshot/bundle"
cp "$runner" "$snapshot/scripts/qualify-proxy-agent-transport-ceiling.sh"
cp "$0" "$snapshot/scripts/qualify-proxy-agent-transport-ceiling-stability.sh"
cp "$bundle_root/SHA256SUMS" "$snapshot/bundle/SHA256SUMS"
while read -r digest relative; do
  [[ $digest =~ ^[0-9a-f]{64}$ && $relative != /* && $relative != *..* && -f $bundle_root/$relative ]] || {
    echo "unsafe or missing checksum-indexed bundle path: $relative" >&2
    exit 2
  }
  mkdir -p "$snapshot/bundle/$(dirname "$relative")"
  cp "$bundle_root/$relative" "$snapshot/bundle/$relative"
done < "$bundle_root/SHA256SUMS"
(
  cd "$snapshot/bundle"
  sha256sum -c SHA256SUMS >/dev/null
) || { echo "snapshotted bundle checksum validation failed" >&2; exit 2; }
chmod +x "$snapshot/scripts/qualify-proxy-agent-transport-ceiling.sh"
chmod +x "$snapshot/bundle/bin/transport-ceiling-server.test" "$snapshot/bundle/bin/transport-ceiling-agent.test"

cat > "$snapshot/scripts/launch-owned-pidns.py" <<'PY'
import ctypes
import os
import signal
import sys

if len(sys.argv) < 3:
    raise SystemExit("usage: launch-owned-pidns.py PARENT_PID COMMAND [ARG...]")
expected_parent = int(sys.argv[1])
if os.getppid() != expected_parent:
    raise SystemExit("wrapper parent changed before PR_SET_PDEATHSIG")
libc = ctypes.CDLL(None, use_errno=True)
if libc.prctl(1, signal.SIGKILL, 0, 0, 0) != 0:
    error = ctypes.get_errno()
    raise OSError(error, os.strerror(error))
if os.getppid() != expected_parent:
    raise SystemExit("wrapper parent changed while installing PR_SET_PDEATHSIG")
os.execvp(sys.argv[2], sys.argv[2:])
PY

runner_snapshot=$snapshot/scripts/qualify-proxy-agent-transport-ceiling.sh
wrapper_snapshot=$snapshot/scripts/qualify-proxy-agent-transport-ceiling-stability.sh
server_binary=$snapshot/bundle/bin/transport-ceiling-server.test
agent_binary=$snapshot/bundle/bin/transport-ceiling-agent.test
[[ $(sha256sum "$runner_snapshot" | awk '{print $1}') == "$expected_runner_sha256" ]] || { echo "snapshotted runner hash mismatch" >&2; exit 2; }
[[ $(sha256sum "$snapshot/bundle/build-manifest.json" | awk '{print $1}') == "$expected_build_manifest_sha256" ]] || { echo "snapshotted build manifest hash mismatch" >&2; exit 2; }
[[ $(sha256sum "$server_binary" | awk '{print $1}') == "$expected_server_sha256" ]] || { echo "snapshotted server binary hash mismatch" >&2; exit 2; }
[[ $(sha256sum "$agent_binary" | awk '{print $1}') == "$expected_agent_sha256" ]] || { echo "snapshotted agent binary hash mismatch" >&2; exit 2; }
runner_sha256=$(sha256sum "$runner_snapshot" | awk '{print $1}')
wrapper_sha256=$(sha256sum "$wrapper_snapshot" | awk '{print $1}')
shim_sha256=$(sha256sum "$snapshot/scripts/launch-owned-pidns.py" | awk '{print $1}')
manifest_sha256=$(sha256sum "$snapshot/bundle/build-manifest.json" | awk '{print $1}')
checksum_index_sha256=$(sha256sum "$snapshot/bundle/SHA256SUMS" | awk '{print $1}')
[[ $checksum_index_sha256 == "$fixed_checksum_index_sha256" ]] || { echo "snapshotted checksum index hash mismatch" >&2; exit 2; }
[[ $($fixed_go_binary tool buildid "$server_binary") == "$fixed_server_build_id" && $($fixed_go_binary tool buildid "$agent_binary") == "$fixed_agent_build_id" ]] || { echo "snapshotted binary build ID mismatch" >&2; exit 2; }

python3 - "$output_dir/preflight.json" "$runner_sha256" "$wrapper_sha256" "$shim_sha256" \
  "$expected_server_sha256" "$expected_agent_sha256" "$manifest_sha256" "$checksum_index_sha256" \
  "$source_ref" "$expected_payload_sha256" "$preflight_only" "$fixture_smoke" "$fixed_base_commit" "$fixed_overlay_sha256" "$fixed_eof_correction_sha256" "$fixed_server_build_id" "$fixed_agent_build_id" <<'PY'
import json,sys,time
(path,runner,wrapper,shim,server,agent,manifest,index,source_ref,payload,preflight_only,fixture_smoke,base,overlay,eof_correction,server_build_id,agent_build_id)=sys.argv[1:]
value={"schema_version":1,"scope":"fixture_smoke_only" if fixture_smoke=="1" else "diagnostic_only","preflight_only":preflight_only=="1",
       "fixture_smoke":fixture_smoke=="1",
       "workload_started":False,"acceptance_result":None,"captured_unix_nano":time.time_ns(),
       "source_ref":source_ref,"expected_payload_sha256":payload,
       "identities":{"runner_sha256":runner,"wrapper_sha256":wrapper,"pidns_shim_sha256":shim,
                     "server_binary_sha256":server,"agent_binary_sha256":agent,
                     "server_build_id":server_build_id,"agent_build_id":agent_build_id,
                     "build_manifest_sha256":manifest,"bundle_checksum_index_sha256":index,
                     "base_commit":base,"overlay_sha256":overlay,"eof_correction_sha256":eof_correction},
       "ownership":{"pid_namespace":True,"mount_proc":True,"network_namespace":True,
                    "parent_death_signal":"SIGKILL","unshare_kill_child":"SIGKILL"}}
open(path,"w").write(json.dumps(value,indent=2,sort_keys=True)+"\n")
PY

if ((preflight_only)); then
  python3 - "$output_dir/exit.json" "$fixture_smoke" <<'PY'
import json,sys,time
open(sys.argv[1],"w").write(json.dumps({"schema_version":1,"exit_code":0,"preflight_only":True,"fixture_smoke":sys.argv[2]=="1","workload_started":False,"timed_out":False,"interrupted":False,"finished_unix_nano":time.time_ns()},indent=2,sort_keys=True)+"\n")
PY
  exit 0
fi

owned_pid=""
finished=0
stop_owned() {
  local attempt
  [[ -n $owned_pid ]] || return 0
  kill -TERM -- "-$owned_pid" 2>/dev/null || true
  for ((attempt=0; attempt<100; attempt++)); do
    kill -0 -- "-$owned_pid" 2>/dev/null || break
    sleep .1
  done
  kill -KILL -- "-$owned_pid" 2>/dev/null || true
  wait "$owned_pid" 2>/dev/null || true
}
write_exit() {
  local status=$1 timed_out=$2 interrupted=$3
  python3 - "$output_dir/exit.json" "$status" "$timed_out" "$interrupted" "$fixture_smoke" <<'PY'
import json,sys,time
path,status,timed_out,interrupted,fixture_smoke=sys.argv[1:]
open(path,"w").write(json.dumps({"schema_version":1,"exit_code":int(status),"preflight_only":False,"fixture_smoke":fixture_smoke=="1","workload_started":True,"timed_out":timed_out=="1","interrupted":interrupted=="1","finished_unix_nano":time.time_ns()},indent=2,sort_keys=True)+"\n")
PY
}
on_signal() {
  local status=$1
  trap - INT TERM
  stop_owned
  write_exit "$status" 0 1
  finished=1
  exit "$status"
}
on_exit() {
  local status=$?
  ((finished)) || { stop_owned; write_exit "$status" 0 0; }
}
trap 'on_signal 130' INT
trap 'on_signal 143' TERM
trap on_exit EXIT

results=$output_dir/results.jsonl
runner_mode_args=()
((fixture_smoke)) && runner_mode_args+=(--fixture-smoke)
setsid python3 "$snapshot/scripts/launch-owned-pidns.py" "$$" \
  unshare --fork --pid --mount --mount-proc --net --kill-child=KILL \
  env P2PSTREAM_TRANSPORT_CEILING_INNER_FIXTURE=1 \
  "$runner_snapshot" \
    --server-binary "$server_binary" \
    --agent-binary "$agent_binary" \
    --source-ref "$source_ref" \
    --expected-payload-sha256 "$expected_payload_sha256" \
    --build-manifest "$snapshot/bundle/build-manifest.json" \
    "${runner_mode_args[@]}" \
    --output "$results" > "$output_dir/run.log" 2>&1 &
owned_pid=$!

timed_out=0
for ((elapsed=0; elapsed<1800; elapsed++)); do
  kill -0 -- "-$owned_pid" 2>/dev/null || break
  sleep 1
done
if kill -0 -- "-$owned_pid" 2>/dev/null; then
  timed_out=1
  stop_owned
  status=124
else
  set +e
  wait "$owned_pid"
  status=$?
  set -e
fi
owned_pid=""
write_exit "$status" "$timed_out" 0
finished=1
exit "$status"

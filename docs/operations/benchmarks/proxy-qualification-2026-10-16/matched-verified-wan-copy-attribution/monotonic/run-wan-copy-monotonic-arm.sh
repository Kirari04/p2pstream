#!/usr/bin/env bash
set -Eeuo pipefail

script_dir=$(cd "$(dirname "$0")" && pwd)
runner="$script_dir/generated/qualify-proxy-agent-growth-wan-copy-monotonic.sh"
validator="$script_dir/validate-wan-copy-monotonic-arm.py"
copy_record_validator="$script_dir/validate-copy-record.py"
approved_runner="$script_dir/../current-wan-prep/runner/qualify-proxy-agent-growth-owned.sh"
frozen_protocol_runner="$script_dir/../wan-protocol-source/generated/qualify-proxy-agent-growth-wan-protocol.sh"
frozen_copy_runner="$script_dir/../wan-copy-attribution-source/generated/qualify-proxy-agent-growth-wan-copy-attribution.sh"
wan_protocol_runner_patch="$script_dir/../wan-protocol-source/wan-protocol-runner.patch"
copy_runner_patch="$script_dir/../wan-copy-attribution-source/copy-attribution-runner.patch"
monotonic_runner_patch="$script_dir/monotonic-runner.patch"
go_patch="$script_dir/../wan-protocol-source/proxy-qualification-base.patch"
go_helper="$script_dir/../wan-protocol-source/proxy_qualification_wan_protocol_test.go"
copy_overlay="$script_dir/../wan-copy-attribution-source/copy-attribution-overlay.patch"
monotonic_observer_patch="$script_dir/monotonic-observer.patch"
monotonic_validator_patch="$script_dir/monotonic-validator-contract.patch"
monotonic_arm_validator_patch="$script_dir/monotonic-arm-validator.patch"
copy_design="$script_dir/DESIGN.md"
build_manifest="$script_dir/provenance/monotonic-build-manifest.json"
audited_build_manifest="$script_dir/provenance/audited-server-build-manifest.json"
final_source_attestation="$script_dir/provenance/final-source-binary-toolchain-attestation.json"
runner_reconstruction_attestation="$script_dir/provenance/runner-reconstruction-attestation.json"
integration_notes="$script_dir/INTEGRATION-NOTES.md"
decision_map="$script_dir/../current-wan-prep/COPY-TIMING-DECISION-MAP.md"
server_binary=""
aggregate_agent_binary=""
server_source_ref=""
aggregate_agent_source_ref=""
protocol=""
cert=""
key=""
expected_leaf_sha256=""
output_dir=""
smoke=0

while (($#)); do
  case "$1" in
    --server-binary) server_binary=$2; shift 2 ;;
    --aggregate-agent-binary) aggregate_agent_binary=$2; shift 2 ;;
    --server-source-ref) server_source_ref=$2; shift 2 ;;
    --aggregate-agent-source-ref) aggregate_agent_source_ref=$2; shift 2 ;;
    --protocol) protocol=$2; shift 2 ;;
    --cert) cert=$2; shift 2 ;;
    --key) key=$2; shift 2 ;;
    --expected-leaf-sha256) expected_leaf_sha256=$2; shift 2 ;;
    --output-dir) output_dir=$2; shift 2 ;;
    --smoke) smoke=1; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

[[ -x $server_binary && -x $aggregate_agent_binary && -x $runner && -x $validator && -x $copy_record_validator ]] || {
  echo "server, agent, runner, arm validator, and copy validator must be executable" >&2
  exit 2
}
[[ -n $server_source_ref && -n $aggregate_agent_source_ref && -n $output_dir ]] || {
  echo "source refs and output directory are required" >&2
  exit 2
}
[[ $protocol == h1 || $protocol == h2 ]] || { echo "protocol must be h1 or h2" >&2; exit 2; }
[[ -f $cert && -f $key && $expected_leaf_sha256 =~ ^[0-9a-f]{64}$ ]] || {
  echo "certificate, key, and lowercase DER fingerprint are required" >&2
  exit 2
}
server_binary=$(realpath "$server_binary")
aggregate_agent_binary=$(realpath "$aggregate_agent_binary")
approved_server_binary=$(realpath "$script_dir/build/bin/current-server-wan-copy-monotonic-audited.test")
cert=$(realpath "$cert")
key=$(realpath "$key")
for tool in cmp jq lscpu openssl patch python3 readlink setsid sha256sum taskset timeout unshare; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 2; }
done
[[ $(sha256sum "$approved_runner" | awk '{print $1}') == b29242b6891105fd6231fecd7739ae48e6909049d0583a184765839ad2827c5f ]] || {
  echo "approved lifecycle runner hash changed" >&2
  exit 2
}
[[ $(sha256sum "$frozen_protocol_runner" | awk '{print $1}') == 309d90622ad36f8f082bfd010ec85a7f87d13d80d9d8ce9a8b97ec883a12bad7 ]] || {
  echo "frozen WAN protocol runner hash changed" >&2
  exit 2
}
[[ $(sha256sum "$frozen_copy_runner" | awk '{print $1}') == 4c76336266bceaab9d2639a5d81e3ce272b4dc924d15154bb6ccbdea4b58f852 ]] || {
  echo "frozen copy-attribution runner hash changed" >&2
  exit 2
}
for name in P2PSTREAM_QUALIFICATION_PROFILE_DIR P2PSTREAM_QUALIFICATION_PROFILE_MODE; do
  [[ -z ${!name+x} ]] || { echo "$name must be unset" >&2; exit 2; }
done
server_binary_sha256=$(sha256sum "$server_binary" | awk '{print $1}')
agent_binary_sha256=$(sha256sum "$aggregate_agent_binary" | awk '{print $1}')
[[ $server_binary == "$approved_server_binary" ]] || {
  echo "server binary path is not the fresh audited diagnostic build" >&2
  exit 2
}
[[ $server_binary_sha256 == 0530cdc308bf06964f61d55ca730309e125cabd3c296ed3c22f0cfe218552013 && \
   $agent_binary_sha256 == 9e635d6e1e8fbd8651260eb081fb5fc7847438bc51c653a5f7785cbecde4a137 ]] || {
  echo "server or aggregate-agent binary is not the approved diagnostic build" >&2
  exit 2
}
[[ $(sha256sum "$build_manifest" | awk '{print $1}') == f505bcd62f1bd050f21f4ddc6d407b377ebb5039029ec66980439f050023ada3 ]] || {
  echo "monotonic build manifest hash changed" >&2
  exit 2
}
[[ $(sha256sum "$final_source_attestation" | awk '{print $1}') == 18a25434183eeecf08d3214eb6748c528a89fa2f74bc7c98854075eafb3ed37a ]] || {
  echo "final monotonic source attestation hash changed" >&2
  exit 2
}
[[ $(sha256sum "$monotonic_observer_patch" | awk '{print $1}') == a8cc867dc9e12681e74b6a51396941d0a03c5145450eabff2e0bae0d8e79965a &&
   $(sha256sum "$monotonic_validator_patch" | awk '{print $1}') == f1dafaabc985842bc7cbc18a199e79abdc709e9021f4c5d7695ad17eb5e90f63 &&
   $(sha256sum "$monotonic_runner_patch" | awk '{print $1}') == 23cb24cd59481005ae83e580a8d20dedc0c4e1167df01ffd3ec8c854a807263a &&
   $(sha256sum "$monotonic_arm_validator_patch" | awk '{print $1}') == fcae97fd6653cb438e5b85295844b394aa1108e0f66d064cdee5942b412d04f3 ]] || {
  echo "monotonic correction artifact hash changed" >&2
  exit 2
}
jq -e --arg server "$server_binary_sha256" --arg agent "$agent_binary_sha256" \
  --arg server_ref "$server_source_ref" --arg agent_ref "$aggregate_agent_source_ref" '
    .base_source_ref == $server_ref and .aggregate_agent_source_ref == $agent_ref and
    .sha256.server_test_binary == $server and .sha256.aggregate_agent_test_binary == $agent and
    .frozen_input.copy_attribution_overlay_sha256 == "4da0889228f80407806ac111187366ab06abd0ffd5d130d08d41407a8a02ff5f" and
    .sha256.monotonic_observer_patch == "a8cc867dc9e12681e74b6a51396941d0a03c5145450eabff2e0bae0d8e79965a" and
    .sha256.monotonic_validator_contract_patch == "f1dafaabc985842bc7cbc18a199e79abdc709e9021f4c5d7695ad17eb5e90f63" and
    .source_verification.reconstruction_status == 0 and .source_verification.source_pre_post_test_equal == true and
    .focused_tests.normal.status == 0 and .focused_tests.race.status == 0 and
    .focused_tests.copy_record_contracts.status == 0 and .build.status == 0 and .build.source_compare_status == 0 and
    .traffic_run == false
  ' "$build_manifest" >/dev/null || { echo "diagnostic build manifest does not bind the requested artifacts" >&2; exit 2; }
jq -e --arg server "$server_binary_sha256" --arg agent "$agent_binary_sha256" '
    .source_verification.frozen_copy_attribution_overlay_sha256 == "4da0889228f80407806ac111187366ab06abd0ffd5d130d08d41407a8a02ff5f" and
    .source_verification.monotonic_observer_patch_sha256 == "a8cc867dc9e12681e74b6a51396941d0a03c5145450eabff2e0bae0d8e79965a" and
    .source_verification.reconstruction_status == 0 and .toolchain.version == "go version go1.26.6 linux/amd64" and
    .binaries.server.sha256 == $server and .binaries.agent.sha256 == $agent and .diagnostic_only == true and .traffic_run == false
  ' "$final_source_attestation" >/dev/null || { echo "final source/binary/toolchain attestation does not bind the requested artifacts" >&2; exit 2; }
[[ $(sha256sum "$audited_build_manifest" | awk '{print $1}') == 0ba246408bdabdb31bad837d0163b9957c93cbe8888eef2ed3fd4c29d2fed38b ]] || {
  echo "audited server build manifest hash changed" >&2
  exit 2
}
jq -e --arg server "$server_binary_sha256" --arg path "build/bin/current-server-wan-copy-monotonic-audited.test" \
  --arg gomod "$script_dir/build/source/go.mod" '
    .base_source_ref == "8ac4c8df8237e37777ba2fac0da6f949da17bdea" and
    .frozen_copy_attribution_overlay_sha256 == "4da0889228f80407806ac111187366ab06abd0ffd5d130d08d41407a8a02ff5f" and
    .monotonic_observer_patch_sha256 == "a8cc867dc9e12681e74b6a51396941d0a03c5145450eabff2e0bae0d8e79965a" and
    .source_pre_post_equal == true and .build_status == 0 and .source_compare_status == 0 and
    .toolchain.path == "/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin/go" and
    .toolchain.sha256 == "29e6e0b8be61beb1489ceae62b304343566de8a1dc700af74bde7aeb9c80ad45" and
    .toolchain.version == "go version go1.26.6 linux/amd64" and .toolchain.pre_post_equal == true and
    .output.path == $path and .output.sha256 == $server and
    .metadata_inspection.go_env_captured_from_compile_working_directory == true and
    .metadata_inspection.gomod == $gomod and
    .test_rerun == false and .traffic_run == false
  ' "$audited_build_manifest" >/dev/null || { echo "audited server build manifest does not bind the requested artifact" >&2; exit 2; }
[[ $(sha256sum "$decision_map" | awk '{print $1}') == f6dc409926ca8178ba0da8ec262960a6b8e238d2a9d94dbe0afc9077f629c6f7 ]] || {
  echo "copy timing decision map hash changed" >&2
  exit 2
}
actual_leaf_sha256=$(openssl x509 -in "$cert" -outform DER | sha256sum | awk '{print $1}')
[[ $actual_leaf_sha256 == "$expected_leaf_sha256" ]] || { echo "certificate DER fingerprint mismatch" >&2; exit 2; }

output_dir=$(realpath -m "$output_dir")
allowed_root=$(realpath -m "$script_dir/runs")
[[ $output_dir == "$allowed_root"/* ]] || { echo "output must remain below $allowed_root" >&2; exit 2; }
[[ ! -e $output_dir ]] || { echo "output directory already exists: $output_dir" >&2; exit 2; }
mkdir -p "$output_dir/tmp"
export TMPDIR="$output_dir/tmp"
results="$output_dir/results.jsonl"
lifecycle="$output_dir/results.lifecycle.jsonl"
summary="$output_dir/results.summary.json"
outer="$output_dir/outer-namespaces.json"
supervisor="$output_dir/supervisor-lifecycle.json"
pre_hashes="$output_dir/pre-hashes.json"
post_hashes="$output_dir/post-hashes.json"
attestation="$output_dir/attestation.json"

write_hashes() {
  local destination=$1
  python3 - "$destination" "$0" "$runner" "$validator" "$copy_record_validator" "$approved_runner" "$frozen_protocol_runner" "$frozen_copy_runner" \
    "$wan_protocol_runner_patch" "$copy_runner_patch" "$monotonic_runner_patch" "$go_patch" "$go_helper" "$copy_overlay" \
    "$monotonic_observer_patch" "$monotonic_validator_patch" "$monotonic_arm_validator_patch" "$copy_design" "$build_manifest" "$audited_build_manifest" \
    "$final_source_attestation" "$runner_reconstruction_attestation" "$integration_notes" "$decision_map" \
    "$server_binary" "$aggregate_agent_binary" "$cert" "$key" "$server_source_ref" \
    "$aggregate_agent_source_ref" "$expected_leaf_sha256" <<'PY'
import hashlib, json, pathlib, sys
(destination, supervisor, runner, validator, copy_record_validator, approved, frozen_protocol_runner, frozen_copy_runner,
 wan_protocol_runner_patch, copy_runner_patch, monotonic_runner_patch, go_patch, go_helper, copy_overlay,
 monotonic_observer_patch, monotonic_validator_patch, monotonic_arm_validator_patch, copy_design, build_manifest, audited_build_manifest,
 final_source_attestation, runner_reconstruction_attestation, integration_notes, decision_map,
 server, agent, cert, key, server_ref, agent_ref, fingerprint) = sys.argv[1:]
def sha(path):
    digest = hashlib.sha256()
    with open(path, "rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()
value = {
    "schema_version": 1,
    "server_source_ref": server_ref,
    "aggregate_agent_source_ref": agent_ref,
    "expected_leaf_der_sha256": fingerprint,
    "sha256": {
        "supervisor": sha(supervisor), "runner": sha(runner), "validator": sha(validator),
        "copy_record_validator": sha(copy_record_validator), "approved_lifecycle_runner": sha(approved),
        "frozen_protocol_runner": sha(frozen_protocol_runner), "frozen_copy_runner": sha(frozen_copy_runner),
        "wan_protocol_runner_patch": sha(wan_protocol_runner_patch), "copy_runner_patch": sha(copy_runner_patch),
        "monotonic_runner_patch": sha(monotonic_runner_patch), "go_base_patch": sha(go_patch), "go_helper": sha(go_helper),
        "copy_attribution_overlay_patch": sha(copy_overlay), "copy_attribution_design": sha(copy_design),
        "monotonic_observer_patch": sha(monotonic_observer_patch), "monotonic_validator_patch": sha(monotonic_validator_patch),
        "monotonic_arm_validator_patch": sha(monotonic_arm_validator_patch),
        "diagnostic_build_manifest": sha(build_manifest),
        "audited_build_manifest": sha(audited_build_manifest),
        "final_source_attestation": sha(final_source_attestation), "integration_notes": sha(integration_notes),
        "runner_reconstruction_attestation": sha(runner_reconstruction_attestation),
        "copy_timing_decision_map": sha(decision_map),
        "server_binary": sha(server), "aggregate_agent_binary": sha(agent),
        "certificate_pem": sha(cert), "private_key_pem": sha(key),
    },
}
pathlib.Path(destination).write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
PY
}
write_hashes "$pre_hashes"

{
  uname -a
  taskset -pc $$
  lscpu
  stat -f -c 'tmpdir_filesystem=%T' "$TMPDIR"
  printf 'TMPDIR=%s\n' "$TMPDIR"
  env | LC_ALL=C sort | grep -E '^(GOFLAGS|P2PSTREAM_.*PROFILE|TMPDIR)=' || true
  printf 'self_cgroup='; tr '\n' ';' < /proc/self/cgroup; printf '\n'
  for name in cpu.max cpuset.cpus.effective memory.current memory.high memory.max memory.swap.max pids.current pids.max; do
    path=/sys/fs/cgroup/$name
    [[ -r $path ]] && printf '%s=%s\n' "$name" "$(<"$path")"
  done
  for path in /sys/class/thermal/thermal_zone*/temp /sys/class/hwmon/hwmon*/temp*_input; do
    [[ -r $path ]] && printf '%s=%s\n' "$path" "$(<"$path")"
  done
} > "$output_dir/host-settings.txt"

command=(
  "$runner"
  --server-binary "$server_binary"
  --aggregate-agent-binary "$aggregate_agent_binary"
  --output "$results"
  --runs 1
  --warmup-mib 256
  --steady-mib 256
  --transfer-repetitions 3
  --required-pool-mib 8
  --rtt-ms 80
  --server-source-ref "$server_source_ref"
  --aggregate-agent-source-ref "$aggregate_agent_source_ref"
  --protocol "$protocol"
  --cert "$cert"
  --key "$key"
  --expected-leaf-sha256 "$expected_leaf_sha256"
)
timeout_seconds=4800
if ((smoke)); then
  command+=(--smoke)
  timeout_seconds=1200
fi
printf '%q ' "${command[@]}" > "$output_dir/invocation.txt"
printf '\n' >> "$output_dir/invocation.txt"

runner_pid=""
forced=0
cleanup() {
  local status=$? attempt joined=false
  trap - EXIT INT TERM
  if [[ -n $runner_pid ]] && kill -0 -- "-$runner_pid" 2>/dev/null; then
    forced=1
    kill -TERM -- "-$runner_pid" 2>/dev/null || true
    for ((attempt=0; attempt<100; attempt++)); do
      kill -0 -- "-$runner_pid" 2>/dev/null || { joined=true; break; }
      sleep .05
    done
    [[ $joined == true ]] || kill -KILL -- "-$runner_pid" 2>/dev/null || true
    wait "$runner_pid" 2>/dev/null || true
  fi
  if ((forced)); then
    printf '{"forced_supervisor_cleanup":true,"invalidates_success":true}\n' > "$output_dir/supervisor-cleanup.json"
    ((status != 0)) || status=1
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

set +e
parent_user_ns=$(readlink /proc/self/ns/user)
parent_pid_ns=$(readlink /proc/self/ns/pid)
parent_mount_ns=$(readlink /proc/self/ns/mnt)
parent_net_ns=$(readlink /proc/self/ns/net)
setsid timeout --signal=TERM --kill-after=10s "${timeout_seconds}s" \
  unshare --user --map-root-user --pid --fork --kill-child=TERM --mount --mount-proc --net \
  bash -c '
    out=$1 parent_user=$2 parent_pid=$3 parent_mount=$4 parent_net=$5
    shift 5
    python3 - "$out" "$parent_user" "$parent_pid" "$parent_mount" "$parent_net" "$$" <<'"'"'PY'"'"'
import json, os, pathlib, sys
out, parent_user, parent_pid, parent_mount, parent_net, shell_pid = sys.argv[1:]
current = {name: os.readlink(f"/proc/self/ns/{name}") for name in ("user", "pid", "mnt", "net")}
nspid = next(line.split()[1:] for line in pathlib.Path("/proc/self/status").read_text().splitlines() if line.startswith("NSpid:"))
payload = {
    "schema_version": 1,
    "boot_id": pathlib.Path("/proc/sys/kernel/random/boot_id").read_text().strip(),
    "parent": {"user": parent_user, "pid": parent_pid, "mnt": parent_mount, "net": parent_net},
    "owned": current,
    "shell_pid": int(shell_pid),
    "shell_pid_is_one": int(shell_pid) == 1,
    "python_parent_pid": os.getppid(),
    "python_parent_is_one": os.getppid() == 1,
    "nspid": [int(value) for value in nspid],
    "all_private": all(current[name] != value for name, value in (("user", parent_user), ("pid", parent_pid), ("mnt", parent_mount), ("net", parent_net))),
}
pathlib.Path(out).write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n")
PY
    exec "$@"
  ' bash "$outer" "$parent_user_ns" "$parent_pid_ns" "$parent_mount_ns" "$parent_net_ns" "${command[@]}" \
  > "$output_dir/runner.stdout.log" 2> "$output_dir/runner.stderr.log" &
runner_pid=$!
wait "$runner_pid"
runner_status=$?
set -e
printf '%s\n' "$runner_status" > "$output_dir/runner.status"

group_joined=false
for ((attempt=0; attempt<100; attempt++)); do
  kill -0 -- "-$runner_pid" 2>/dev/null || { group_joined=true; break; }
  sleep .05
done
if [[ $group_joined == false ]]; then
  forced=1
  kill -TERM -- "-$runner_pid" 2>/dev/null || true
  sleep .1
  kill -KILL -- "-$runner_pid" 2>/dev/null || true
fi
printf '{"runner_status":%s,"group_joined":%s,"forced":%s,"invalidates_success":%s}\n' \
  "$runner_status" "$group_joined" "$([[ $forced == 0 ]] && printf false || printf true)" \
  "$([[ $forced == 0 ]] && printf false || printf true)" > "$supervisor"
runner_pid=""
write_hashes "$post_hashes"

set +e
"$validator" "$results" "$lifecycle" "$summary" "$outer" "$supervisor" "$pre_hashes" "$post_hashes" \
  "$attestation" "$protocol" "$smoke" > "$output_dir/validation.stdout.log" 2> "$output_dir/validation.stderr.log"
validation_status=$?
set -e
printf '%s\n' "$validation_status" > "$output_dir/validation.status"
((runner_status == 0 && forced == 0 && validation_status == 0))

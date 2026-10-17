#!/usr/bin/env bash
set -Eeuo pipefail

[[ ($# == 2 || ($# == 3 && $3 == --preflight-only)) && $1 == --output-dir ]] || {
  echo "usage: $0 --output-dir DIR [--preflight-only]" >&2
  exit 2
}
preflight_only=0
[[ ${3:-} != --preflight-only ]] || preflight_only=1
root=$(realpath "$(dirname "$0")/../../..")
prep=$root/tmp/wan-route-reuse-qualification-20261017
output_dir=$(realpath -m "$2")
[[ $output_dir == "$prep"/* ]] || { echo "output must remain under $prep" >&2; exit 2; }
[[ ! -e $output_dir ]] || { echo "output directory already exists: $output_dir" >&2; exit 2; }
mkdir -p "$output_dir"
mkdir -p "$output_dir/tmp"
export TMPDIR=$output_dir/tmp
results=$output_dir/results.jsonl
settings=$output_dir/host-settings.txt
outer_namespaces=$output_dir/outer-namespaces.json
runner=$prep/runner/qualify-proxy-agent-growth-owned.sh
validator=$prep/runner/validate-owned-h2-stability.py
for tool in awk lscpu python3 readlink setsid sha256sum taskset timeout unshare; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 2; }
done
# Both files remain absent until exact production source/build review.
[[ -f $prep/provenance/stability-manifest.json && -f $prep/REVIEWED-BUILD-RECEIPT-SHA256 && -f $prep/verify-runtime-inputs.py ]] || { echo "new production build/source receipt is pending review" >&2; exit 125; }
python3 -I -B "$prep/verify-runtime-inputs.py" "$prep/provenance/stability-manifest.json" > "$output_dir/verified-binding.txt" || { echo "new production tuple verification rejected" >&2; exit 125; }
mapfile -t binding < "$output_dir/verified-binding.txt"
[[ ${#binding[@]} == 5 ]] || { echo "exact reviewed binary/source tuple missing" >&2; exit 125; }
server_binary=${binding[0]}; aggregate_agent_binary=${binding[1]}; legacy_agent_binary=${binding[2]}
current_source_ref=${binding[3]}; legacy_source_ref=${binding[4]}
python3 -I -B "$prep/prove-default-transport.py" self "$output_dir/transport-defaults-host-pre.json"
for name in P2PSTREAM_QUALIFICATION_PROFILE_DIR P2PSTREAM_QUALIFICATION_PROFILE_MODE P2PSTREAM_QUALIFICATION_EARLY_SHARE P2PSTREAM_QUALIFICATION_EARLY_SHARE_PROOF_DIR P2PSTREAM_QUALIFICATION_DATA_CONTROLLER P2PSTREAM_IDLE_ARM_INDEX; do
  [[ -z ${!name+x} ]] || { echo "$name must be unset" >&2; exit 2; }
done

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
} > "$settings"

command=(
  "$runner"
  --server-binary "$server_binary"
  --aggregate-agent-binary "$aggregate_agent_binary"
  --legacy-agent-binary "$legacy_agent_binary"
  --output "$results"
  --runs 3
  --warmup-mib 256
  --steady-mib 256
  --transfer-repetitions 3
  --required-pool-mib 8
  --rtt-ms 80
  --server-source-ref "$current_source_ref"
  --aggregate-agent-source-ref "$current_source_ref"
  --legacy-agent-source-ref "$legacy_source_ref"
)
printf '%q ' "${command[@]}" > "$output_dir/invocation.txt"
printf '\n' >> "$output_dir/invocation.txt"
launch_command=("${command[@]}")
if ((preflight_only)); then
  launch_command=(bash -c 'exit 0')
fi

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
setsid timeout --signal=TERM --kill-after=10s 13200s \
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
  ' bash "$outer_namespaces" "$parent_user_ns" "$parent_pid_ns" "$parent_mount_ns" "$parent_net_ns" "${launch_command[@]}" \
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
  "$runner_status" "$group_joined" "$([[ $forced == 0 ]] && printf false || printf true)" "$([[ $forced == 0 ]] && printf false || printf true)" \
  > "$output_dir/supervisor-lifecycle.json"
runner_pid=""
((forced == 0)) || exit 1
if ((preflight_only)); then
  printf '%s\n' "$runner_status" > "$output_dir/preflight-only.status"
  exit "$runner_status"
fi

python3 -I -B "$prep/prove-default-transport.py" self "$output_dir/transport-defaults-host-post.json"
python3 -I -B "$prep/verify-runtime-inputs.py" "$prep/provenance/stability-manifest.json" > "$output_dir/verified-binding-post.txt"
cmp "$output_dir/verified-binding.txt" "$output_dir/verified-binding-post.txt"
set +e
"$validator" "$results" "${results%.jsonl}.lifecycle.jsonl" "$output_dir/h2-stability-attestation.json" "$outer_namespaces" "$output_dir/supervisor-lifecycle.json" "$prep/provenance/stability-manifest.json" \
  > "$output_dir/validation.stdout.log" 2> "$output_dir/validation.stderr.log"
validation_status=$?
python3 -I -B "$prep/prove-default-transport.py" validate "$output_dir" "$output_dir/transport-defaults-validation.json" > "$output_dir/transport-defaults-validation.stdout.log" 2> "$output_dir/transport-defaults-validation.stderr.log"
transport_defaults_status=$?
set -e
printf '%s\n' "$transport_defaults_status" > "$output_dir/transport-defaults-validation.status"
printf '%s\n' "$validation_status" > "$output_dir/validation.status"
final_status=$runner_status
((validation_status == 0)) || final_status=$validation_status
((transport_defaults_status == 0)) || final_status=$transport_defaults_status
printf '%s\n' "$final_status" > "$output_dir/wrapper.status"
results_hash=$(sha256sum "$results" | awk '{print $1}')
printf 'OWNED_WAN_STABILITY_FINAL runner_status=%s validation_status=%s wrapper_status=%s results=%q results_sha256=%s\n' \
  "$runner_status" "$validation_status" "$final_status" "$results" "$results_hash"
exit "$final_status"

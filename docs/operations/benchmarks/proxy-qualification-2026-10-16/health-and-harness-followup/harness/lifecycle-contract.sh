#!/usr/bin/env bash
# Bounded actual lifecycle probes. Uses only owned processes, cached image and
# a private Unix socket; does not compile binaries or run proxy/load traffic.
set -Eeuo pipefail
cd "$(dirname "$0")/../.."
artifact=$(realpath tmp/qualification-harness-fix-20261017)
probe_root=$(mktemp -d -p "$artifact" actual-lifecycle.XXXXXX)
socket_root=$(mktemp -d /tmp/p2pstream-lifecycle.XXXXXX)
children=()
containers=()
declare -A process_containers=()
extract() { awk -v name="$1" '$0 == name "() {" {copy=1} copy {print} copy && /^}/ {exit}' scripts/qualify-proxy-performance.sh; }
eval "$(extract record_process_exit)"
eval "$(extract stop_process)"
cleanup_probe() {
  local status=$? pid container
  trap - EXIT
  set +e
  for pid in "${children[@]}"; do kill -KILL "$pid" 2>/dev/null; done
  for container in "${containers[@]}"; do timeout -k 1 5 docker rm -f "$container" >/dev/null 2>&1; done
  rm -f "$socket_root/haproxy.sock"
  rmdir "$socket_root"
  printf 'probe_root=%s\n' "$probe_root"
  exit "$status"
}
trap cleanup_probe EXIT

for expected in 0 7; do
  bash -c 'exit "$1"' fixture "$expected" &
  pid=$!
  children+=("$pid")
  if stop_process "$pid" "$probe_root/host-$expected.stop"; then status=0; else status=$?; fi
  [[ $status == "$expected" ]]
  printf 'host_expected=%d observed=%d\n' "$expected" "$status"
done

# The production (unshortened) ten-second timeout must reject a forced stop
# even when a cooperative TERM handler returns zero.
bash -c 'trap "exit 0" TERM; while :; do sleep .05; done' &
pid=$!
children+=("$pid")
if stop_process "$pid" "$probe_root/forced-host.stop"; then status=0; else status=$?; fi
[[ $status == 1 ]]
jq -e '.shell_exit_status == 0 and .forced_cleanup == true and .natural_exit_verified == false' "$probe_root/forced-host.stop.exit.json" >/dev/null
printf 'forced_host_zero_exit_rejected=%d\n' "$status"

for expected in 0 7; do
  name=p2pstream-lifecycle-contract-$$-$expected
  stop="$probe_root/docker-$expected.stop"
  containers+=("$name")
  docker run --name "$name" --network none --user "$(id -u):$(id -g)" \
    --cpuset-cpus 12 --memory 64m --memory-swap 64m --pids-limit 32 \
    --mount "type=bind,source=$probe_root,target=/fixture" \
    debian:trixie-slim /bin/sh -c \
    'while [ ! -e "/fixture/docker-$1.stop" ]; do sleep .05; done; exit "$1"' fixture "$expected" \
    >"$probe_root/docker-$expected.stdout.log" 2>"$probe_root/docker-$expected.stderr.log" &
  pid=$!
  children+=("$pid")
  process_containers[$pid]=$name
  deadline=$((SECONDS + 10))
  while [[ $(timeout -k 1 2 docker inspect --format '{{.State.Running}}' "$name" 2>/dev/null || true) != true ]]; do
    ((SECONDS < deadline)) || { echo 'container startup timeout' >&2; exit 1; }
    sleep .05
  done
  if stop_process "$pid" "$stop"; then status=0; else status=$?; fi
  [[ $status == "$expected" ]]
  jq -e --argjson expected "$expected" '.shell_exit_status == $expected and .container_state.ExitCode == $expected and .forced_cleanup == false' "$stop.exit.json" >/dev/null
  if ((expected)); then
    timeout -k 1 5 docker rm "$name" >/dev/null
    for index in "${!containers[@]}"; do [[ ${containers[$index]} != "$name" ]] || unset 'containers[index]'; done
  fi
  ! timeout -k 1 2 docker inspect "$name" >/dev/null 2>&1
  printf 'docker_expected=%d observed=%d removed=true\n' "$expected" "$status"
done

cat >"$probe_root/haproxy.cfg" <<EOF
global
  nbthread 1
defaults
  mode http
  timeout connect 1s
  timeout client 1s
  timeout server 1s
listen lifecycle
  bind $socket_root/haproxy.sock
  http-request return status 200
EOF
taskset -c 12 haproxy -db -f "$probe_root/haproxy.cfg" >"$probe_root/haproxy.log" 2>&1 &
pid=$!
children+=("$pid")
deadline=$((SECONDS + 5))
while [[ ! -S $socket_root/haproxy.sock ]]; do
  kill -0 "$pid"
  ((SECONDS < deadline)) || exit 1
  sleep .05
done
kill -USR1 "$pid"
stop_process "$pid" "$probe_root/haproxy.stop"
jq -e '.shell_exit_status == 0 and .forced_cleanup == false and .natural_exit_verified == true' "$probe_root/haproxy.stop.exit.json" >/dev/null
printf 'haproxy_soft_stop=0\n'
printf 'tracked_children=%d tracked_containers=%d\n' "${#children[@]}" "${#containers[@]}"
[[ ${#children[@]} == 0 && ${#containers[@]} == 0 ]]

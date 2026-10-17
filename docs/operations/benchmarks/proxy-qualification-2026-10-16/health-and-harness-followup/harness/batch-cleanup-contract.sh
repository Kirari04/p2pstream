#!/usr/bin/env bash
set -Eeuo pipefail
cd "$(dirname "$0")/../.."
run_dir=$(mktemp -d -p tmp/qualification-harness-fix-20261017 actual-batch-cleanup.XXXXXX)
run_dir=$(realpath "$run_dir")
printf 'probe_root=%s\n' "$run_dir"
children=()
stops=()
containers=()
eval "$(awk '/^cleanup\(\) \{/ {copy=1} copy {print} copy && /^}/ {exit}' scripts/qualify-proxy-performance.sh)"
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
for number in 1 2; do
  name=p2pstream-batch-cleanup-contract-$$-$number
  containers+=("$name")
  printf '%s\n' "$name" >>"$run_dir/container-names.txt"
  docker run --name "$name" --network none --cpuset-cpus 12 --memory 64m --memory-swap 64m \
    --pids-limit 32 debian:trixie-slim /bin/sh -c 'sleep 60' >"$run_dir/$number.log" 2>&1 &
  children+=("$!")
  deadline=$((SECONDS + 10))
  while [[ $(timeout -k 1 2 docker inspect --format '{{.State.Running}}' "$name" 2>/dev/null || true) != true ]]; do
    ((SECONDS < deadline)) || exit 8
    sleep .05
  done
done
printf 'started_owned_containers=2\n'
exit 7

#!/usr/bin/env bash
set -Eeuo pipefail
mode=$1 token=$2 ready=$3
setsid bash -c 'exec -a "$1-descendant" sleep "$2"' _ "$token" "$([[ $mode == natural ]] && echo .15 || echo 60)" &
child=$!
printf '%s\n' ready > "$ready"
if [[ $mode == natural ]]; then
  wait "$child"
else
  sleep 60
fi

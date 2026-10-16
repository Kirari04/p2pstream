#!/usr/bin/env bash
set -Eeuo pipefail
declare -A owned_groups=()
source /tmp/p2pstream-stop-process-function.sh
root=$1
wait_group_gone() { local pgid=$1; for ((i=0;i<100;i++)); do kill -0 -- "-$pgid" 2>/dev/null || return 0; sleep .02; done; return 1; }
run_graceful() {
  local stop=$root/graceful-$BASHPID.stop
  setsid bash -c 'stop=$1; while [[ ! -e $stop ]]; do sleep .02; done' bash "$stop" &
  local pid=$!; owned_groups[$pid]=1
  stop_process "$pid" "$stop"
  wait_group_gone "$pid"
}
run_forced() {
  local stop=$root/forced-$BASHPID.stop
  setsid bash -c 'trap "" TERM; while :; do sleep 1; done' &
  local pid=$!; owned_groups[$pid]=1
  local status; if (stop_process "$pid" "$stop"); then status=0; else status=$?; fi
  unset 'owned_groups[$pid]'
  [[ $status == 1 ]]
  wait_group_gone "$pid"
}
run_surviving_child() {
  local stop=$root/survivor-$BASHPID.stop childfile=$root/survivor-$BASHPID.child
  setsid bash -c 'stop=$1; childfile=$2; (trap "" TERM; while :; do sleep 1; done) & echo $! > "$childfile"; while [[ ! -e $stop ]]; do sleep .02; done; exit 0' bash "$stop" "$childfile" &
  local pid=$!; owned_groups[$pid]=1
  while [[ ! -s $childfile ]]; do sleep .01; done
  local child; child=$(cat "$childfile")
  local status; if (stop_process "$pid" "$stop"); then status=0; else status=$?; fi
  unset 'owned_groups[$pid]'
  [[ $status == 1 ]]
  wait_group_gone "$pid"
  for ((i=0;i<100;i++)); do kill -0 "$child" 2>/dev/null || break; sleep .02; done
  ! kill -0 "$child" 2>/dev/null
}
run_graceful
run_forced
run_surviving_child
((${#owned_groups[@]} == 0))
printf 'graceful, forced, and child-survives-parent process-group cases PASS\n'

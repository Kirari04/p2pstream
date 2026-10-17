stop_process() {
  local pid=$1 stop=$2
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
    if kill -0 -- "-$pid" 2>/dev/null; then
      kill -KILL -- "-$pid" 2>/dev/null || true
    fi
    wait "$pid" 2>/dev/null || true
    unset 'owned_groups[$pid]'
    echo "owned process group $pid did not stop within its graceful shutdown deadline" >&2
    return 1
  fi
  local status
  set +e
  wait "$pid" 2>/dev/null
  status=$?
  set -e
  unset 'owned_groups[$pid]'
  ((status == 0)) || {
    echo "process $pid exited with status $status" >&2
    return 1
  }
}

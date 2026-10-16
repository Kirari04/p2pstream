#!/usr/bin/env bash
set -Eeuo pipefail
root=$(realpath "$1")
mkdir -p "$root/actual"
owned=""
cleanup() {
  [[ -n $owned ]] || return 0
  kill -TERM -- "-$owned" 2>/dev/null || true
  for _ in $(seq 1 100); do kill -0 -- "-$owned" 2>/dev/null || break; sleep .05; done
  kill -KILL -- "-$owned" 2>/dev/null || true
  wait "$owned" 2>/dev/null || true
}
trap cleanup EXIT INT TERM
setsid python3 "$root/launch-owned-pidns.py" "$$" \
  unshare --fork --pid --mount --mount-proc --net --kill-child=KILL \
  env P2PSTREAM_TRANSPORT_CEILING_INNER_FIXTURE=1 "$root/topology-harness.sh" "$root/actual" \
  >"$root/actual-harness.stdout" 2>"$root/actual-harness.stderr" &
owned=$!
status=124
for _ in $(seq 1 2400); do
  if ! kill -0 -- "-$owned" 2>/dev/null; then set +e; wait "$owned"; status=$?; set -e; owned=""; break; fi
  sleep .05
done
printf '%s\n' "$status" > "$root/actual-harness.status"
((status == 0))
trap - EXIT INT TERM

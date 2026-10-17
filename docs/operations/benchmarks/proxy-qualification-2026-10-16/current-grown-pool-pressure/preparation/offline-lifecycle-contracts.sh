#!/usr/bin/env bash
set -Eeuo pipefail
artifact=$(realpath "$(dirname "$0")")
runner=$artifact/proposal/scripts/qualify-proxy-agent-growth-pressure-owned.sh
case_root=$(mktemp -d -p "$artifact/contracts" lifecycle.XXXXXX)
extract() { awk -v name="$1" '$0 == name "() {" {copy=1} copy {print} copy && $0 == "}" {exit}' "$runner"; }
eval "$(extract register_owned_group)"
# Only fixture timing is shortened; the shipped proposal keeps 10/5/5 seconds.
eval "$(extract join_owned_group | sed 's/attempt<200/attempt<2/g; s/attempt<50/attempt<2/g')"
declare -A owned_groups=()
lifecycle_output=$case_root/lifecycle.jsonl
: >"$lifecycle_output"
trap 'for owner in "${!owned_groups[@]}"; do kill -KILL -- "-$owner" 2>/dev/null || true; done' EXIT
for expected in 0 7; do
  stop=$case_root/natural-$expected.stop
  setsid bash -c 'touch "$2"; while [[ ! -e $1 ]]; do sleep .01; done; exit "$3"' fixture "$stop" "$stop.ready" "$expected" &
  pid=$!
  register_owned_group "$pid" "natural-$expected"
  while [[ ! -e $stop.ready ]]; do sleep .01; done
  touch "$stop"
  if join_owned_group "natural-$expected" "$pid"; then status=0; else status=$?; fi
  [[ $status == "$expected" ]]
  printf 'natural_expected=%s observed=%s\n' "$expected" "$status"
done
setsid bash -c 'trap "exit 0" TERM; touch "$1"; while :; do sleep .02; done' fixture "$case_root/forced.ready" &
pid=$!
register_owned_group "$pid" forced-zero
while [[ ! -e $case_root/forced.ready ]]; do sleep .01; done
if join_owned_group forced-zero "$pid"; then echo 'forced zero was accepted' >&2; exit 1; fi
jq -es 'any(.[]; .label == "forced-zero" and .forced == true and .invalidates_success == true)' "$lifecycle_output" >/dev/null
printf 'forced_zero=rejected\n'
[[ ${#owned_groups[@]} == 0 ]]
printf 'case_root=%s\n' "$case_root"

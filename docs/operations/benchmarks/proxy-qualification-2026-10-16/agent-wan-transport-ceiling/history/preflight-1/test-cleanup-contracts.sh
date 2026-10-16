#!/usr/bin/env bash
set -Eeuo pipefail
root=$1 shim=$2 fake=$3
mkdir -p "$root"
: > "$root/cleanup-results.tsv"
wait_ready() { local f=$1 i; for ((i=0;i<100;i++)); do [[ -s $f ]] && return; sleep .02; done; return 1; }
no_token() { local token=$1 i; for ((i=0;i<100;i++)); do pgrep -f "$token" >/dev/null || return 0; sleep .02; done; return 1; }
launch() {
 local mode=$1 token=$2 ready=$3 childfile=$4
 setsid bash -c '
   shim=$1; fake=$2; mode=$3; token=$4; ready=$5
   python3 "$shim" "$BASHPID" unshare --user --map-root-user --fork --pid --mount-proc --kill-child=KILL "$fake" "$mode" "$token" "$ready"
 ' _ "$shim" "$fake" "$mode" "$token" "$ready" >/dev/null 2>&1 &
 launched_pid=$!
}
# Dead/changed parent is rejected before prctl/exec.
set +e
python3 "$shim" 999999999 /bin/true >"$root/dead-parent.log" 2>&1
status=$?
set -e
[[ $status -ne 0 ]]; printf 'dead-parent-before-prctl\tnonzero\t%s\n' "$status" >> "$root/cleanup-results.tsv"
# Natural join.
token=tc-natural-$$; ready=$root/natural.ready; childfile=$root/natural.child
launch natural "$token" "$ready" "$childfile"; wrapper=$launched_pid; wait_ready "$ready"; wait "$wrapper"; no_token "$token"
printf 'natural\t0\t0\n' >> "$root/cleanup-results.tsv"
# Timeout-style TERM then bounded KILL of only owned wrapper group.
token=tc-timeout-$$; ready=$root/timeout.ready; childfile=$root/timeout.child
launch timeout "$token" "$ready" "$childfile"; wrapper=$launched_pid; wait_ready "$ready"; kill -TERM -- "-$wrapper" 2>/dev/null || true
for ((i=0;i<50;i++)); do kill -0 -- "-$wrapper" 2>/dev/null || break; sleep .02; done
kill -KILL -- "-$wrapper" 2>/dev/null || true; wait "$wrapper" 2>/dev/null || true; no_token "$token"
printf 'timeout-owned-group\tno-survivors\tpass\n' >> "$root/cleanup-results.tsv"
# SIGTERM path, same owned group semantics.
token=tc-sigterm-$$; ready=$root/sigterm.ready; childfile=$root/sigterm.child
launch sigterm "$token" "$ready" "$childfile"; wrapper=$launched_pid; wait_ready "$ready"; kill -TERM -- "-$wrapper" 2>/dev/null || true
for ((i=0;i<50;i++)); do kill -0 -- "-$wrapper" 2>/dev/null || break; sleep .02; done
kill -KILL -- "-$wrapper" 2>/dev/null || true; wait "$wrapper" 2>/dev/null || true; no_token "$token"
printf 'sigterm-owned-group\tno-survivors\tpass\n' >> "$root/cleanup-results.tsv"
# Kill only the live wrapper parent. PDEATHSIG must kill shim/unshare, whose
# --kill-child must reap a descendant in a different process group.
token=tc-parentdeath-$$; ready=$root/parentdeath.ready; childfile=$root/parentdeath.child
launch parentdeath "$token" "$ready" "$childfile"; wrapper=$launched_pid; wait_ready "$ready"; child=$(pgrep -P "$wrapper" -n); [[ -n $child ]]; kill -KILL "$wrapper"; wait "$wrapper" 2>/dev/null || true
for ((i=0;i<100;i++)); do [[ ! -e /proc/$child ]] && break; sleep .02; done
[[ ! -e /proc/$child ]]; no_token "$token"
printf 'live-parent-death\tshim-and-descendant-gone\tpass\n' >> "$root/cleanup-results.tsv"
cat "$root/cleanup-results.tsv"

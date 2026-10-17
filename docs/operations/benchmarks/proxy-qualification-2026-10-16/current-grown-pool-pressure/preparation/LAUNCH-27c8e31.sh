#!/usr/bin/env bash
# Prepared command only. Execute once only after the coordinator grants the
# full pressure build/cohort window. Uses the reviewed v3 frozen derivative.
set -Eeuo pipefail
cd /home/user/.codex/worktrees/astra-proxy-performance/p2pstream
artifact=tmp/pressure-final-readiness-20261017
candidate_ref=27c8e31f3826ab5326d941b20644c1f56159d99d
output_root="$PWD/tmp/final-qualification/$candidate_ref/pressure-attempt-1"
go_binary=/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin/go
sha256sum -c "$artifact/OFFLINE-FROZEN-SOURCE-v3.sha256"
[[ ! -e $output_root ]] || { echo 'pressure attempt directory already exists' >&2; exit 2; }
mkdir -p "$(dirname "$output_root")"
set +e
timeout --signal=TERM --kill-after=35s 7200s taskset -c 0-15 \
  bash "$artifact/proposal/scripts/qualify-proxy-pressure-checkpoint.sh" \
  --candidate-ref "$candidate_ref" --output-root "$output_root" --go-binary "$go_binary" \
  >"$output_root.outer.stdout.log" 2>"$output_root.outer.stderr.log"
status=$?
printf '%s\n' "$status" >"$output_root.outer.exit-status"
exit "$status"

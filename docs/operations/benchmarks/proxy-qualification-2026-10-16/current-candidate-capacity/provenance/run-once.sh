#!/usr/bin/env bash
set -Eeuo pipefail
cd /home/user/.codex/worktrees/astra-proxy-performance/p2pstream
PREP="$PWD/tmp/independent-candidate-capacity-20261017"
QUAL_REF=27c8e31f3826ab5326d941b20644c1f56159d99d
QUAL_ROOT="$PWD/tmp/final-qualification/$QUAL_REF"
OUTPUT_ROOT="$QUAL_ROOT/capacity-independent-attempt-1"
[[ ! -e $OUTPUT_ROOT ]]
[[ $(sha256sum "$PREP/independent-capacity.sh" | awk '{print $1}') == 9c9f26d8ad2a77d9ae963b0c50c42a53e5dbb7e0250334770d9fc95a7f5a083c ]]
[[ $(sha256sum "$PREP/prepared-manifest.json" | awk '{print $1}') == c4d4d6bd9dae8d3d3860b0a487602923f837896b1f906340bc65c936b4cf0542 ]]
PINNED_GOROOT=/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64
export PATH="$PINNED_GOROOT/bin:/usr/local/go/bin:$PATH"
export GOTOOLCHAIN=local GOMAXPROCS=4 CGO_ENABLED=1 GOWORK=off GOFLAGS=''
[[ $(go version) == 'go version go1.26.6 linux/amd64' && $(go env GOROOT) == "$PINNED_GOROOT" ]]
[[ -z $(docker ps --filter name=p2pstream-qualification --format '{{.ID}}') ]]
LEDGER="$QUAL_ROOT/capacity-independent-attempt-1.launches.jsonl"
jq -nc --arg at "$(date -u +%FT%TZ)" --arg runner "$PREP/independent-capacity.sh" --arg out "$OUTPUT_ROOT" --arg ref "$QUAL_REF" '{state:"started",at:$at,source_ref:$ref,independent_absolute_candidate_capacity:true,clears_legacy_performance_comparison:false,clears_wan:false,authorization:"Coordinator approved frozen source/manifest and explicitly granted exactly one exclusive independent capacity cohort; no automatic retry or changes.",argv:["taskset","-c","0-15",$runner,"--candidate-ref",$ref,"--output-root",$out]}' >>"$LEDGER"
if taskset -c 0-15 "$PREP/independent-capacity.sh" --candidate-ref "$QUAL_REF" --output-root "$OUTPUT_ROOT" >"$QUAL_ROOT/capacity-independent-attempt-1.stdout.log" 2>"$QUAL_ROOT/capacity-independent-attempt-1.stderr.log"; then cohort_status=0; else cohort_status=$?; fi
jq -nc --arg at "$(date -u +%FT%TZ)" --argjson status "$cohort_status" '{state:"finished",at:$at,exit_status:$status,independent_absolute_candidate_capacity:true,clears_legacy_performance_comparison:false,clears_wan:false}' >>"$LEDGER"
printf 'independent_capacity_exit_status=%d\noutput=%s\n' "$cohort_status" "$OUTPUT_ROOT"
exit "$cohort_status"

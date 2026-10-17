#!/usr/bin/env bash
# Finite isolated experiment. Run only after a direct exclusive CPU handoff.
set -Eeuo pipefail
experiment=/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/shaper-bypass-experiment
candidate_dir=/tmp/p2pstream-shaper-guard.8mbrurzb
frozen_dir=/tmp/p2pstream-caddy-3ec137c.SoeRAa
baseline=$frozen_dir/server.test
candidate=$candidate_dir/server.test
mkdir -p "$experiment/logs"
cd "$candidate_dir"
# Existing semantic regressions, including selected shaping and late body reuse.
focused='Test(PublicTrafficShaper|TrafficShaper|PublicPolicyMatchCostFailure|PublicProxyContextReuse|PublicProxyResolutionPointerRemainsOwned)'
GOMAXPROCS=4 taskset -c 0-3 go test ./internal/server -run "$focused" -count=3 >"$experiment/logs/focused-normal.log" 2>&1
GOMAXPROCS=4 taskset -c 0-3 go test -race ./internal/server -run "$focused" -count=3 >"$experiment/logs/focused-race.log" 2>&1
GOMAXPROCS=4 taskset -c 0-3 go test -c -o "$candidate" ./internal/server >"$experiment/logs/build.log" 2>&1
cd "$frozen_dir"
# Three alternating baseline/candidate pairs, each followed by the same HA control.
scripts/qualify-proxy-performance.sh \
  --runs 3 --decisive-only --decisive-concurrency 128 \
  --duration-seconds 10 --warmup-seconds 2 \
  --scenario-filter h1-keepalive-duration --path-filter direct \
  --baseline-label frozen-3ec137c \
  --baseline-binary "$baseline" \
  --candidate-label shaper-bypass-guard \
  --candidate-binary "$candidate" --driver-binary "$baseline" \
  --output "$experiment/sustained-c128.jsonl" >"$experiment/logs/sustained-c128.log" 2>&1
# A distinct diagnostic profile; its timing is excluded from decisive evidence.
scripts/qualify-proxy-performance.sh \
  --runs 1 --decisive-only --decisive-concurrency 128 \
  --duration-seconds 10 --warmup-seconds 2 \
  --scenario-filter h1-keepalive-duration --path-filter direct \
  --candidate-label shaper-bypass-profile \
  --candidate-binary "$candidate" --driver-binary "$baseline" \
  --profile-dir "$experiment/profiles" --profile-mode cpu \
  --output "$experiment/profile-results.jsonl" >"$experiment/logs/profile.log" 2>&1
printf 'experiment=%s\n' "$experiment"
